package ir

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/tailscale/hujson"
	"github.com/tidwall/gjson"
)

func BytesToString(b []byte) string {
	return string(b)
}

// ErrInvalidJSON is returned when JSON parsing fails.
var ErrInvalidJSON = &json.UnmarshalTypeError{Value: "invalid json"}

// ExtractThoughtSignature extracts thought signature from a gjson.Result.
// Returns []byte as per SDK spec. Handles both camelCase and snake_case field names.
// ThoughtSignature is an opaque binary blob, returned as base64-encoded in JSON.
func ExtractThoughtSignature(part gjson.Result) []byte {
	var tsStr string
	if ts := part.Get("thoughtSignature").String(); ts != "" {
		tsStr = ts
	} else {
		tsStr = part.Get("thought_signature").String()
	}
	if tsStr == "" {
		return nil
	}
	// Return raw string as bytes - API returns base64 which we preserve as-is
	return []byte(tsStr)
}

// ParseAndValidateJSON parses and validates JSON in one operation.
// Returns the parsed result and nil error on success, or empty result and ErrInvalidJSON on failure.
func ParseAndValidateJSON(rawJSON []byte) (gjson.Result, error) {
	result := gjson.ParseBytes(rawJSON)
	if !result.Exists() || result.Type == gjson.Null {
		return gjson.Result{}, ErrInvalidJSON
	}
	return result, nil
}

func UnwrapAntigravityEnvelope(rawJSON []byte) (gjson.Result, bool) {
	rawStr := BytesToString(rawJSON)
	if responseWrapper := gjson.Get(rawStr, "response"); responseWrapper.Exists() {
		return responseWrapper, true
	}
	return gjson.Parse(rawStr), false
}

var bytePool = sync.Pool{
	New: func() any {
		b := make([]byte, 24) // OpenAI tool call ID length
		return &b
	},
}

func ParseOpenAIUsage(u gjson.Result) *Usage {
	if !u.Exists() {
		return nil
	}
	usage := &Usage{
		PromptTokens:     u.Get("prompt_tokens").Int() + u.Get("input_tokens").Int(),
		CompletionTokens: u.Get("completion_tokens").Int() + u.Get("output_tokens").Int(),
		TotalTokens:      u.Get("total_tokens").Int(),
	}
	if usage.TotalTokens == 0 {
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	if v := u.Get("input_tokens_details.cached_tokens"); v.Exists() {
		usage.CachedTokens = v.Int()
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &PromptTokensDetails{}
		}
		usage.PromptTokensDetails.CachedTokens = v.Int()
	} else if v := u.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
		usage.CachedTokens = v.Int()
	}

	if v := u.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
		usage.ThoughtsTokenCount = int32(v.Int())
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &CompletionTokensDetails{}
		}
		usage.CompletionTokensDetails.ReasoningTokens = v.Int()
	} else if v := u.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
		usage.ThoughtsTokenCount = int32(v.Int())
	}

	// Parse prompt_tokens_details
	if ptd := u.Get("prompt_tokens_details"); ptd.Exists() {
		promptDetails := &PromptTokensDetails{}
		if v := ptd.Get("cached_tokens"); v.Exists() {
			promptDetails.CachedTokens = v.Int()
		}
		if v := ptd.Get("audio_tokens"); v.Exists() {
			promptDetails.AudioTokens = v.Int()
		}
		if promptDetails.CachedTokens > 0 || promptDetails.AudioTokens > 0 {
			usage.PromptTokensDetails = promptDetails
		}
	}

	// Parse completion_tokens_details
	if ctd := u.Get("completion_tokens_details"); ctd.Exists() {
		completionDetails := &CompletionTokensDetails{}
		if v := ctd.Get("reasoning_tokens"); v.Exists() {
			completionDetails.ReasoningTokens = v.Int()
		}
		if v := ctd.Get("audio_tokens"); v.Exists() {
			completionDetails.AudioTokens = v.Int()
		}
		if v := ctd.Get("accepted_prediction_tokens"); v.Exists() {
			completionDetails.AcceptedPredictionTokens = v.Int()
		}
		if v := ctd.Get("rejected_prediction_tokens"); v.Exists() {
			completionDetails.RejectedPredictionTokens = v.Int()
		}
		if completionDetails.ReasoningTokens > 0 || completionDetails.AudioTokens > 0 ||
			completionDetails.AcceptedPredictionTokens > 0 || completionDetails.RejectedPredictionTokens > 0 {
			usage.CompletionTokensDetails = completionDetails
		}
	}

	return usage
}

func DefaultGeminiSafetySettings() []map[string]string {
	return []map[string]string{
		{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
	}
}

func CleanJsonSchema(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	delete(schema, "strict")
	delete(schema, "input_examples")
	delete(schema, "$schema")
	delete(schema, "additionalProperties")
	return schema
}

func GenToolCallID() string {
	return fmt.Sprintf("call_%s", generateAlphanumeric(24))
}

func generateAlphanumeric(length int) string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	var b []byte
	if length == 24 {
		bp := bytePool.Get().(*[]byte)
		b = *bp
		defer bytePool.Put(bp)
	} else {
		b = make([]byte, length)
	}

	_, _ = rand.Read(b)
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return string(b)
}

func GenClaudeToolCallID() string {
	return fmt.Sprintf("toolu_%s", generateAlphanumeric(20))
}

// Tool ID Conversion Functions
// These normalize tool call IDs between providers for consistent handling.

// FromKiroToolID converts Kiro/Amazon Q tool ID format to standard format.
// tooluse_XXX -> call_XXX
func FromKiroToolID(id string) string {
	if strings.HasPrefix(id, "tooluse_") {
		return strings.Replace(id, "tooluse_", "call_", 1)
	}
	return id
}

// FromClaudeToolID converts Claude tool ID format to standard format.
// toolu_XXX -> call_XXX
func FromClaudeToolID(id string) string {
	if strings.HasPrefix(id, "toolu_") {
		return strings.Replace(id, "toolu_", "call_", 1)
	}
	return id
}

// ToKiroToolID converts standard tool ID format to Kiro format.
// call_XXX -> tooluse_XXX
func ToKiroToolID(id string) string {
	if strings.HasPrefix(id, "call_") {
		return strings.Replace(id, "call_", "tooluse_", 1)
	}
	return id
}

// GenerateUUID generates a UUID v4 string using pooled buffers to reduce allocations.
func GenerateUUID() string {
	bp := GetUUIDBuf()
	b := *bp
	defer PutUUIDBuf(bp)

	_, _ = rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40 // Version 4
	b[8] = (b[8] & 0x3f) | 0x80 // Variant

	// Use a pre-sized buffer to avoid fmt.Sprintf allocations
	buf := make([]byte, 36)
	hexEncode(buf[0:8], b[0:4])
	buf[8] = '-'
	hexEncode(buf[9:13], b[4:6])
	buf[13] = '-'
	hexEncode(buf[14:18], b[6:8])
	buf[18] = '-'
	hexEncode(buf[19:23], b[8:10])
	buf[23] = '-'
	hexEncode(buf[24:36], b[10:16])
	return string(buf)
}

const hexChars = "0123456789abcdef"

// hexEncode encodes src bytes to dst as lowercase hex.
func hexEncode(dst, src []byte) {
	for i, v := range src {
		dst[i*2] = hexChars[v>>4]
		dst[i*2+1] = hexChars[v&0x0f]
	}
}

func SanitizeText(s string) string {
	if s == "" || (utf8.ValidString(s) && !hasProblematicChars(s)) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			i += size
			continue
		}
		b.WriteRune(r)
		i += size
	}
	return b.String()
}

func hasProblematicChars(s string) bool {
	for _, r := range s {
		if r == 0 || (r < 0x20 && r != '\t' && r != '\n' && r != '\r') {
			return true
		}
	}
	return false
}

func MapGeminiFinishReason(geminiReason string) FinishReason {
	switch strings.ToUpper(geminiReason) {
	case "STOP", "FINISH_REASON_UNSPECIFIED", "UNKNOWN":
		return FinishReasonStop
	case "MAX_TOKENS":
		return FinishReasonMaxTokens
	case "SAFETY", "RECITATION":
		return FinishReasonContentFilter
	case "MALFORMED_FUNCTION_CALL":
		return FinishReasonToolCalls
	default:
		return FinishReasonUnknown
	}
}

func ParseMalformedFunctionCall(finishMessage string) (string, string, bool) {
	idx := strings.Index(finishMessage, ": call:")
	if idx != -1 {
		idx += 2
	} else if strings.HasPrefix(finishMessage, "call:") {
		idx = 0
	} else {
		idx = strings.LastIndex(finishMessage, "call:")
		if idx == -1 {
			return "", "", false
		}
	}

	callPart := finishMessage[idx:]

	rest := callPart[5:] // skip "call:"

	colonIdx := strings.Index(rest, ":")
	if colonIdx == -1 {
		return "", "", false
	}
	rest = rest[colonIdx+1:] // skip "default_api:"

	braceIdx := strings.Index(rest, "{")
	if braceIdx == -1 {
		return "", "", false
	}

	funcName := rest[:braceIdx]
	argsRaw := rest[braceIdx:]

	depth := 0
	endIdx := -1
	for i, c := range argsRaw {
		if c == '{' {
			depth++
		} else if c == '}' {
			depth--
			if depth == 0 {
				endIdx = i + 1
				break
			}
		}
	}
	if endIdx == -1 {
		return "", "", false
	}
	argsRaw = argsRaw[:endIdx]

	argsJSON := convertMalformedArgsToJSON(argsRaw)

	return funcName, argsJSON, true
}

// Input: {path:"src/server",count:123,flag:true}
// Output: {"path":"src/server","count":123,"flag":true}
func convertMalformedArgsToJSON(argsRaw string) string {
	if argsRaw == "{}" || argsRaw == "" {
		return "{}"
	}

	// hujson handles: unquoted keys, trailing commas, comments, etc.
	standardized, err := hujson.Standardize([]byte(argsRaw))
	if err != nil {
		return convertMalformedArgsToJSONFallback(argsRaw)
	}

	return string(standardized)
}

func convertMalformedArgsToJSONFallback(argsRaw string) string {
	var result strings.Builder
	result.Grow(len(argsRaw) + 20)

	inString := false
	escaped := false

	for i := 0; i < len(argsRaw); i++ {
		c := argsRaw[i]

		if escaped {
			result.WriteByte(c)
			escaped = false
			continue
		}

		if c == '\\' && inString {
			result.WriteByte(c)
			escaped = true
			continue
		}

		if c == '"' {
			inString = !inString
			result.WriteByte(c)
			continue
		}

		if inString {
			result.WriteByte(c)
			continue
		}

		// Outside string - look for unquoted keys
		if c == '{' || c == ',' {
			result.WriteByte(c)
			for i+1 < len(argsRaw) && (argsRaw[i+1] == ' ' || argsRaw[i+1] == '\t' || argsRaw[i+1] == '\n') {
				i++
			}
			if i+1 < len(argsRaw) && argsRaw[i+1] != '"' && argsRaw[i+1] != '}' {
				keyStart := i + 1
				keyEnd := keyStart
				for keyEnd < len(argsRaw) && argsRaw[keyEnd] != ':' && argsRaw[keyEnd] != ' ' {
					keyEnd++
				}
				if keyEnd < len(argsRaw) && keyStart < keyEnd {
					key := argsRaw[keyStart:keyEnd]
					result.WriteByte('"')
					result.WriteString(key)
					result.WriteByte('"')
					i = keyEnd - 1 // -1 because loop will increment
				}
			}
			continue
		}

		result.WriteByte(c)
	}

	return result.String()
}

func MapClaudeFinishReason(claudeReason string) FinishReason {
	switch claudeReason {
	case "end_turn":
		return FinishReasonStop // Claude "end_turn" = normal completion = IR "stop"
	case "stop_sequence":
		return FinishReasonStopSequence
	case "max_tokens":
		return FinishReasonMaxTokens
	case "tool_use":
		return FinishReasonToolCalls
	default:
		return FinishReasonUnknown
	}
}

func MapOpenAIFinishReason(openaiReason string) FinishReason {
	switch openaiReason {
	case "stop":
		return FinishReasonStop
	case "length":
		return FinishReasonMaxTokens // OpenAI "length" = IR "max_tokens"
	case "tool_calls", "function_call":
		return FinishReasonToolCalls
	case "content_filter":
		return FinishReasonContentFilter
	default:
		return FinishReasonUnknown
	}
}

func MapFinishReasonToOpenAI(reason FinishReason) string {
	switch reason {
	case FinishReasonMaxTokens:
		return "length"
	case FinishReasonToolCalls:
		return "tool_calls"
	case FinishReasonContentFilter:
		return "content_filter"
	case FinishReasonStopSequence, FinishReasonStop:
		return "stop"
	default:
		return "stop"
	}
}

func MapStandardRole(role string) Role {
	switch role {
	case "system":
		return RoleSystem
	case "assistant":
		return RoleAssistant
	case "tool":
		return RoleTool
	default:
		return RoleUser
	}
}


// schemaCache caches cleaned schemas to avoid repeated processing.
// Uses sync.Map for concurrent access without locks on read path.
var schemaCache sync.Map

// schemaHasher computes a hash for schema caching.
func schemaHasher(schema map[string]any) string {
	// Simple hash based on type and properties keys
	if schema == nil {
		return ""
	}
	var b strings.Builder
	if t, ok := schema["type"].(string); ok {
		b.WriteString(t)
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		for k := range props {
			b.WriteString(k)
		}
	}
	return b.String()
}

// geminiUnsupportedFields is a pre-computed set of JSON Schema fields that Gemini API doesn't support.
// Using a map for O(1) lookup instead of slice iteration.
var geminiUnsupportedFields = map[string]struct{}{
	// Reference/Definition keywords
	"$ref": {}, "$defs": {}, "definitions": {}, "$id": {}, "$anchor": {}, "$dynamicRef": {}, "$dynamicAnchor": {},
	// Schema metadata
	"$schema": {}, "$vocabulary": {}, "$comment": {},
	// Number validation keywords - the primary cause of this bug
	"exclusiveMinimum": {}, "exclusiveMaximum": {},
	"minimum": {}, "maximum": {}, "multipleOf": {},
	// String validation keywords
	"minLength": {}, "maxLength": {}, "pattern": {},
	// Array validation keywords
	"minItems": {}, "maxItems": {}, "uniqueItems": {}, "minContains": {}, "maxContains": {},
	// Object validation keywords
	"minProperties": {}, "maxProperties": {},
	// Conditional keywords
	"if": {}, "then": {}, "else": {}, "dependentSchemas": {}, "dependentRequired": {},
	// Unevaluated keywords
	"unevaluatedItems": {}, "unevaluatedProperties": {},
	// Content keywords
	"contentEncoding": {}, "contentMediaType": {}, "contentSchema": {},
	// Deprecated keywords
	"dependencies": {},
	// Composition keywords that may cause issues
	"allOf": {}, "anyOf": {}, "oneOf": {}, "not": {},
}

// claudeUnsupportedFields is a pre-computed set of JSON Schema fields that Claude API doesn't support.
// Using a map for O(1) lookup instead of slice iteration.
var claudeUnsupportedFields = map[string]struct{}{
	// Composition keywords
	"anyOf": {}, "oneOf": {}, "allOf": {}, "not": {},
	// Snake_case variants
	"any_of": {}, "one_of": {}, "all_of": {},
	// Reference keywords
	"$ref": {}, "$defs": {}, "definitions": {}, "$id": {}, "$anchor": {}, "$dynamicRef": {}, "$dynamicAnchor": {},
	// Schema metadata
	"$schema": {}, "$vocabulary": {}, "$comment": {},
	// Conditional keywords
	"if": {}, "then": {}, "else": {}, "dependentSchemas": {}, "dependentRequired": {},
	// Unevaluated keywords
	"unevaluatedItems": {}, "unevaluatedProperties": {},
	// Content keywords
	"contentEncoding": {}, "contentMediaType": {}, "contentSchema": {},
	// Deprecated keywords
	"dependencies": {},
	// Array validation keywords
	"minItems": {}, "maxItems": {}, "uniqueItems": {}, "minContains": {}, "maxContains": {},
	// String validation keywords
	"minLength": {}, "maxLength": {}, "pattern": {}, "format": {},
	// Number validation keywords
	"minimum": {}, "maximum": {}, "exclusiveMinimum": {}, "exclusiveMaximum": {}, "multipleOf": {},
	// Object validation keywords
	"minProperties": {}, "maxProperties": {},
	// Default values - Claude officially doesn't support in input_schema
	"default": {},
}

// geminiSchemaCache caches cleaned schemas for Gemini to avoid repeated processing.
var geminiSchemaCache sync.Map

func CleanJsonSchemaForGemini(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}

	// Check cache first
	cacheKey := schemaHasher(schema)
	if cached, ok := geminiSchemaCache.Load(cacheKey); ok && cacheKey != "" {
		return cached.(map[string]any)
	}

	schema = CleanJsonSchema(schema)
	cleanSchemaForGeminiRecursive(schema)

	// Cache the result
	if cacheKey != "" {
		geminiSchemaCache.Store(cacheKey, schema)
	}

	return schema
}

// cleanSchemaForGeminiRecursive recursively removes JSON Schema fields that Gemini API doesn't support.
func cleanSchemaForGeminiRecursive(schema map[string]any) {
	if schema == nil {
		return
	}

	// Lowercase type fields for consistency
	if typeVal, ok := schema["type"].(string); ok {
		schema["type"] = strings.ToLower(typeVal)
	}

	// Delete unsupported fields using O(1) map lookup
	for key := range schema {
		if _, unsupported := geminiUnsupportedFields[key]; unsupported {
			delete(schema, key)
		}
	}

	// Recursively clean nested objects in properties
	if properties, ok := schema["properties"].(map[string]any); ok {
		for _, prop := range properties {
			if propMap, ok := prop.(map[string]any); ok {
				cleanSchemaForGeminiRecursive(propMap)
			}
		}
	}

	// Clean items - can be object or array
	if items := schema["items"]; items != nil {
		switch v := items.(type) {
		case map[string]any:
			cleanSchemaForGeminiRecursive(v)
		case []any:
			for _, item := range v {
				if itemMap, ok := item.(map[string]any); ok {
					cleanSchemaForGeminiRecursive(itemMap)
				}
			}
		}
	}

	if prefixItems, ok := schema["prefixItems"].([]any); ok {
		for _, item := range prefixItems {
			if itemMap, ok := item.(map[string]any); ok {
				cleanSchemaForGeminiRecursive(itemMap)
			}
		}
	}

	if addProps, ok := schema["additionalProperties"].(map[string]any); ok {
		cleanSchemaForGeminiRecursive(addProps)
	}

	if patternProps, ok := schema["patternProperties"].(map[string]any); ok {
		for _, prop := range patternProps {
			if propMap, ok := prop.(map[string]any); ok {
				cleanSchemaForGeminiRecursive(propMap)
			}
		}
	}

	if propNames, ok := schema["propertyNames"].(map[string]any); ok {
		cleanSchemaForGeminiRecursive(propNames)
	}

	if contains, ok := schema["contains"].(map[string]any); ok {
		cleanSchemaForGeminiRecursive(contains)
	}
}

func CleanJsonSchemaForClaude(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}

	// Check cache first
	cacheKey := schemaHasher(schema)
	if cached, ok := schemaCache.Load(cacheKey); ok && cacheKey != "" {
		return cached.(map[string]any)
	}

	schema = CleanJsonSchema(schema)
	cleanSchemaForClaudeRecursive(schema)
	schema["additionalProperties"] = false
	schema["$schema"] = JSONSchemaDraft202012

	// Cache the result
	if cacheKey != "" {
		schemaCache.Store(cacheKey, schema)
	}

	return schema
}

// cleanSchemaForClaudeRecursive recursively removes JSON Schema fields that Claude API doesn't support.
// Claude uses JSON Schema draft 2020-12 but doesn't support all features.
// See: https://docs.anthropic.com/en/docs/build-with-claude/tool-use
func cleanSchemaForClaudeRecursive(schema map[string]any) {
	if schema == nil {
		return
	}

	// CRITICAL: Convert "const" to "enum" before deletion
	// Claude doesn't support "const" but supports "enum" with single value
	// This preserves discriminator semantics (e.g., Pydantic Literal types)
	if constVal, ok := schema["const"]; ok {
		schema["enum"] = []any{constVal}
		delete(schema, "const")
	}

	// Lowercase type fields for consistency
	if typeVal, ok := schema["type"].(string); ok {
		schema["type"] = strings.ToLower(typeVal)
	}

	// Delete unsupported fields using O(1) map lookup
	for key := range schema {
		if _, unsupported := claudeUnsupportedFields[key]; unsupported {
			delete(schema, key)
		}
	}

	// Recursively clean nested objects in properties
	if properties, ok := schema["properties"].(map[string]any); ok {
		for key, prop := range properties {
			if propMap, ok := prop.(map[string]any); ok {
				cleanSchemaForClaudeRecursive(propMap)
				properties[key] = propMap
			}
		}
	}

	// Clean items - can be object or array
	if items := schema["items"]; items != nil {
		switch v := items.(type) {
		case map[string]any:
			cleanSchemaForClaudeRecursive(v)
		case []any:
			for i, item := range v {
				if itemMap, ok := item.(map[string]any); ok {
					cleanSchemaForClaudeRecursive(itemMap)
					v[i] = itemMap
				}
			}
		}
	}

	if prefixItems, ok := schema["prefixItems"].([]any); ok {
		for i, item := range prefixItems {
			if itemMap, ok := item.(map[string]any); ok {
				cleanSchemaForClaudeRecursive(itemMap)
				prefixItems[i] = itemMap
			}
		}
	}

	if addProps, ok := schema["additionalProperties"].(map[string]any); ok {
		cleanSchemaForClaudeRecursive(addProps)
	}

	if patternProps, ok := schema["patternProperties"].(map[string]any); ok {
		for key, prop := range patternProps {
			if propMap, ok := prop.(map[string]any); ok {
				cleanSchemaForClaudeRecursive(propMap)
				patternProps[key] = propMap
			}
		}
	}

	if propNames, ok := schema["propertyNames"].(map[string]any); ok {
		cleanSchemaForClaudeRecursive(propNames)
	}

	if contains, ok := schema["contains"].(map[string]any); ok {
		cleanSchemaForClaudeRecursive(contains)
	}
}
