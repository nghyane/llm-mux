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

// ExtractThoughtSignatureString extracts thought signature as string (for backward compatibility).
func ExtractThoughtSignatureString(part gjson.Result) string {
	if ts := part.Get("thoughtSignature").String(); ts != "" {
		return ts
	}
	return part.Get("thought_signature").String()
}

// ValidateJSON checks if the provided bytes are valid JSON.
// Returns ErrInvalidJSON if validation fails.
func ValidateJSON(rawJSON []byte) error {
	if !gjson.ValidBytes(rawJSON) {
		return ErrInvalidJSON
	}
	return nil
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

func EstimateTokenCount(text string) int {
	if text == "" {
		return 0
	}
	return (utf8.RuneCountInString(text) + 2) / 3
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

func MapEffortToBudget(effort string) (int, bool) {
	switch effort {
	case "none":
		return 0, false
	case "low", "minimal":
		return 1024, true
	case "medium":
		return 8192, true
	case "high":
		return 32768, true
	case "xhigh":
		return 65536, true
	default:
		return -1, true
	}
}

func MapBudgetToEffort(budget int, defaultForZero string) string {
	if budget <= 0 {
		return defaultForZero
	}
	if budget <= 1024 {
		return "low"
	}
	if budget <= 8192 {
		return "medium"
	}
	return "high"
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
		return FinishReasonEndTurn
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
		return FinishReasonLength
	case "tool_calls", "function_call":
		return FinishReasonToolCalls
	case "content_filter":
		return FinishReasonContentFilter
	default:
		return FinishReasonUnknown
	}
}

// MapOpenAIFinishReasonExtended maps OpenAI finish reasons with extended reason types
func MapOpenAIFinishReasonExtended(openaiReason string) FinishReason {
	switch openaiReason {
	case "stop":
		return FinishReasonStop
	case "length":
		return FinishReasonMaxTokens
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
	case FinishReasonLength, FinishReasonMaxTokens:
		return "length"
	case FinishReasonToolCalls:
		return "tool_calls"
	case FinishReasonContentFilter:
		return "content_filter"
	case FinishReasonStopSequence, FinishReasonEndTurn:
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

func MapFinishReasonToClaude(reason FinishReason) string {
	switch reason {
	case FinishReasonEndTurn:
		return "end_turn"
	case FinishReasonStopSequence:
		return "stop_sequence"
	case FinishReasonMaxTokens:
		return "max_tokens"
	case FinishReasonToolCalls:
		return "tool_use"
	default:
		return "end_turn"
	}
}

func MapFinishReasonToGemini(reason FinishReason) string {
	switch reason {
	case FinishReasonStop:
		return "STOP"
	case FinishReasonLength, FinishReasonMaxTokens:
		return "MAX_TOKENS"
	case FinishReasonStopSequence, FinishReasonEndTurn:
		return "STOP"
	case FinishReasonToolCalls:
		return "STOP"
	case FinishReasonContentFilter:
		return "SAFETY"
	default:
		return "OTHER"
	}
}

// HasThoughtSignatureOnly checks if a Gemini part contains only thoughtSignature.
func HasThoughtSignatureOnly(thoughtSig, thoughtSigSnake, text, functionCall, inlineData, inlineDataSnake any) bool {
	getString := func(r any) string {
		if r == nil {
			return ""
		}
		if sg, ok := r.(interface{ String() string }); ok {
			return sg.String()
		}
		return ""
	}
	exists := func(r any) bool {
		if r == nil {
			return false
		}
		if ec, ok := r.(interface{ Exists() bool }); ok {
			return ec.Exists()
		}
		return false
	}

	hasThoughtSig := (exists(thoughtSig) && getString(thoughtSig) != "") ||
		(exists(thoughtSigSnake) && getString(thoughtSigSnake) != "")

	if !hasThoughtSig {
		return false
	}

	return !(exists(text) || exists(functionCall) || exists(inlineData) || exists(inlineDataSnake))
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

	// Fields that Claude doesn't support in JSON Schema
	// Based on JSON Schema draft 2020-12 compatibility
	unsupportedFields := []string{
		// Composition keywords that Claude doesn't support
		"anyOf", "oneOf", "allOf", "not",
		// Snake_case variants
		"any_of", "one_of", "all_of",
		// Reference keywords
		"$ref", "$defs", "definitions", "$id", "$anchor", "$dynamicRef", "$dynamicAnchor",
		// Schema metadata
		"$schema", "$vocabulary", "$comment",
		// Conditional keywords
		"if", "then", "else", "dependentSchemas", "dependentRequired",
		// Unevaluated keywords
		"unevaluatedItems", "unevaluatedProperties",
		// Content keywords
		"contentEncoding", "contentMediaType", "contentSchema",
		// Deprecated keywords
		"dependencies",
		// Array validation keywords that may not be supported
		"minItems", "maxItems", "uniqueItems", "minContains", "maxContains",
		// String validation keywords that may cause issues
		"minLength", "maxLength", "pattern", "format",
		// Number validation keywords
		"minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf",
		// Object validation keywords that may cause issues
		"minProperties", "maxProperties",
		// Default values - Claude officially doesn't support in input_schema
		"default",
	}

	for _, field := range unsupportedFields {
		delete(schema, field)
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
