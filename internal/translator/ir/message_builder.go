package ir

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// CombineTextAndReasoning extracts both text and reasoning in a single pass.
func CombineTextAndReasoning(msg Message) (text, reasoning string) {
	// Fast path: count parts first to avoid allocations for single-part messages
	textCount, reasoningCount := 0, 0
	var singleText, singleReasoning string
	for _, part := range msg.Content {
		switch part.Type {
		case ContentTypeText:
			if part.Text != "" {
				textCount++
				singleText = part.Text
			}
		case ContentTypeReasoning:
			if part.Reasoning != "" {
				reasoningCount++
				singleReasoning = part.Reasoning
			}
		}
	}

	// Fast path: single part, no allocation needed
	if textCount <= 1 && reasoningCount <= 1 {
		if textCount == 1 {
			text = singleText
		}
		if reasoningCount == 1 {
			reasoning = singleReasoning
		}
		return
	}

	// Slow path: multiple parts, use builders
	var textBuilder, reasoningBuilder strings.Builder
	if textCount > 1 {
		textBuilder.Grow(textCount * 256) // Estimate 256 bytes per part
	}
	if reasoningCount > 1 {
		reasoningBuilder.Grow(reasoningCount * 512)
	}

	for _, part := range msg.Content {
		switch part.Type {
		case ContentTypeText:
			if part.Text != "" {
				if textCount > 1 {
					textBuilder.WriteString(part.Text)
				}
			}
		case ContentTypeReasoning:
			if part.Reasoning != "" {
				if reasoningCount > 1 {
					reasoningBuilder.WriteString(part.Reasoning)
				}
			}
		}
	}

	if textCount > 1 {
		text = textBuilder.String()
	} else if textCount == 1 {
		text = singleText
	}
	if reasoningCount > 1 {
		reasoning = reasoningBuilder.String()
	} else if reasoningCount == 1 {
		reasoning = singleReasoning
	}
	return
}

// CombineTextParts combines all text content parts from a message.
// Optimized to avoid allocations for single-part messages.
func CombineTextParts(msg Message) string {
	// Fast path: count parts first
	count := 0
	var single string
	for _, part := range msg.Content {
		if part.Type == ContentTypeText && part.Text != "" {
			count++
			single = part.Text
			if count > 1 {
				break // Need builder anyway
			}
		}
	}

	if count == 0 {
		return ""
	}
	if count == 1 {
		return single
	}

	// Multiple parts: use builder
	var b strings.Builder
	b.Grow(count * 256)
	for _, part := range msg.Content {
		if part.Type == ContentTypeText && part.Text != "" {
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

// CombineReasoningParts combines all reasoning content parts from a message.
// Optimized to avoid allocations for single-part messages.
func CombineReasoningParts(msg Message) string {
	// Fast path: count parts first
	count := 0
	var single string
	for _, part := range msg.Content {
		if part.Type == ContentTypeReasoning && part.Reasoning != "" {
			count++
			single = part.Reasoning
			if count > 1 {
				break
			}
		}
	}

	if count == 0 {
		return ""
	}
	if count == 1 {
		return single
	}

	// Multiple parts: use builder
	var b strings.Builder
	b.Grow(count * 512)
	for _, part := range msg.Content {
		if part.Type == ContentTypeReasoning && part.Reasoning != "" {
			b.WriteString(part.Reasoning)
		}
	}
	return b.String()
}

// BuildToolCallMap creates a map of tool call ID to function name.
func BuildToolCallMap(messages []Message) map[string]string {
	m := make(map[string]string)
	for _, msg := range messages {
		if msg.Role == RoleAssistant {
			for _, tc := range msg.ToolCalls {
				m[tc.ID] = tc.Name
			}
		}
	}
	return m
}

// BuildToolResultsMap creates a map of tool call ID to result part.
func BuildToolResultsMap(messages []Message) map[string]*ToolResultPart {
	m := make(map[string]*ToolResultPart)
	for _, msg := range messages {
		// Check RoleTool (OpenAI format) and RoleUser (Claude format has tool_result in user messages)
		if msg.Role == RoleTool || msg.Role == RoleUser {
			for _, part := range msg.Content {
				if part.Type == ContentTypeToolResult && part.ToolResult != nil {
					m[part.ToolResult.ToolCallID] = part.ToolResult
				}
			}
		}
	}
	return m
}

// BuildToolMaps creates both tool call ID→name map and tool results map in a single pass.
func BuildToolMaps(messages []Message) (map[string]string, map[string]*ToolResultPart) {
	idToName := make(map[string]string)
	results := make(map[string]*ToolResultPart)

	for _, msg := range messages {
		switch msg.Role {
		case RoleAssistant:
			for _, tc := range msg.ToolCalls {
				idToName[tc.ID] = tc.Name
			}
		case RoleTool, RoleUser:
			for _, part := range msg.Content {
				if part.Type == ContentTypeToolResult && part.ToolResult != nil {
					results[part.ToolResult.ToolCallID] = part.ToolResult
				}
			}
		}
	}
	return idToName, results
}

// ValidateAndNormalizeJSON ensures a string is valid JSON, wrapping it if not.
func ValidateAndNormalizeJSON(s string) string {
	if s == "" {
		return "{}"
	}
	if !json.Valid([]byte(s)) {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return s
}

// ParseToolCallArgs parses tool call arguments, tolerating barewords.
func ParseToolCallArgs(argsJSON string) map[string]any {
	trimmed := strings.TrimSpace(argsJSON)
	if trimmed == "" || trimmed == "{}" {
		return map[string]any{}
	}
	var argsObj map[string]any
	if json.Unmarshal([]byte(trimmed), &argsObj) == nil {
		return argsObj
	}
	if tolerant := tolerantParseJSONMap(trimmed); len(tolerant) > 0 {
		return tolerant
	}
	return map[string]any{}
}

// tolerantParseJSONMap attempts to parse JSON-like string with barewords.
func tolerantParseJSONMap(s string) map[string]any {
	start, end := strings.Index(s, "{"), strings.LastIndex(s, "}")
	if start == -1 || end == -1 || start >= end {
		return map[string]any{}
	}
	runes := []rune(s[start+1 : end])
	n, i := len(runes), 0
	result := make(map[string]any)

	for i < n {
		// Skip whitespace/commas
		for i < n && isSpaceOrComma(runes[i]) {
			i++
		}
		if i >= n {
			break
		}

		// Expect quoted key
		if runes[i] != '"' {
			for i < n && runes[i] != ',' {
				i++
			}
			continue
		}

		keyToken, nextIdx := parseJSONStringRunes(runes, i)
		if nextIdx == -1 {
			break
		}
		keyName := jsonStringTokenToRawString(keyToken)
		i = nextIdx

		// Skip to colon
		for i < n && isSpace(runes[i]) {
			i++
		}
		if i >= n || runes[i] != ':' {
			break
		}
		i++ // skip ':'

		// Skip to value
		for i < n && isSpace(runes[i]) {
			i++
		}
		if i >= n {
			break
		}

		// Parse value
		var value any
		switch runes[i] {
		case '"':
			valToken, ni := parseJSONStringRunes(runes, i)
			if ni == -1 {
				value, i = "", n
			} else {
				value, i = jsonStringTokenToRawString(valToken), ni
			}
		case '{', '[':
			seg, ni := captureBracketed(runes, i)
			if ni == -1 {
				i = n
			} else {
				var anyVal any
				if json.Unmarshal([]byte(seg), &anyVal) == nil {
					value = anyVal
				} else {
					value = seg
				}
				i = ni
			}
		default:
			j := i
			for j < n && runes[j] != ',' {
				j++
			}
			token := strings.TrimSpace(string(runes[i:j]))
			if token == "true" {
				value = true
			} else if token == "false" {
				value = false
			} else if token == "null" {
				value = nil
			} else if numVal, ok := tryParseNumber(token); ok {
				value = numVal
			} else {
				value = token
			}
			i = j
		}
		result[keyName] = value

		// Skip trailing
		for i < n && isSpace(runes[i]) {
			i++
		}
		if i < n && runes[i] == ',' {
			i++
		}
	}
	return result
}

func isSpace(r rune) bool {
	return r == ' ' || r == '\n' || r == '\r' || r == '\t'
}

func isSpaceOrComma(r rune) bool {
	return isSpace(r) || r == ','
}

func parseJSONStringRunes(runes []rune, start int) (string, int) {
	if start >= len(runes) || runes[start] != '"' {
		return "", -1
	}
	i, escaped := start+1, false
	for i < len(runes) {
		if runes[i] == '\\' && !escaped {
			escaped = true
			i++
			continue
		}
		if runes[i] == '"' && !escaped {
			return string(runes[start : i+1]), i + 1
		}
		escaped = false
		i++
	}
	return string(runes[start:]), -1
}

func jsonStringTokenToRawString(token string) string {
	var s string
	if json.Unmarshal([]byte(token), &s) == nil {
		return s
	}
	if len(token) >= 2 && token[0] == '"' && token[len(token)-1] == '"' {
		return token[1 : len(token)-1]
	}
	return token
}

func captureBracketed(runes []rune, i int) (string, int) {
	if i >= len(runes) {
		return "", -1
	}
	startRune := runes[i]
	var endRune rune
	switch startRune {
	case '{':
		endRune = '}'
	case '[':
		endRune = ']'
	default:
		return "", -1
	}

	depth, j, inString, escaped := 1, i+1, false, false
	for j < len(runes) && depth > 0 {
		r := runes[j]
		if inString {
			if r == '\\' && !escaped {
				escaped = true
			} else {
				if r == '"' && !escaped {
					inString = false
				}
				escaped = false
			}
		} else {
			switch r {
			case '"':
				inString = true
			case startRune:
				depth++
			case endRune:
				depth--
			}
		}
		j++
	}
	if depth != 0 {
		return "", -1
	}
	return string(runes[i:j]), j
}

func tryParseNumber(s string) (any, bool) {
	var intVal int64
	if _, err := fmt.Sscanf(s, "%d", &intVal); err == nil && fmt.Sprintf("%d", intVal) == s {
		return intVal, true
	}
	var floatVal float64
	if _, err := fmt.Sscanf(s, "%f", &floatVal); err == nil {
		return floatVal, true
	}
	return nil, false
}

// ParseOpenAIStyleToolCalls parses tool_calls array in OpenAI/Ollama format.
func ParseOpenAIStyleToolCalls(toolCalls []gjson.Result) []ToolCall {
	var result []ToolCall
	for _, tc := range toolCalls {
		if tc.Get("type").String() == "function" {
			result = append(result, ToolCall{
				ID:   tc.Get("id").String(),
				Name: tc.Get("function.name").String(),
				Args: tc.Get("function.arguments").String(),
			})
		}
	}
	return result
}

// These helpers provide unified handling for all reasoning/thinking formats:
// - xAI Grok: reasoning_content, reasoning_details[]
// - OpenAI o1/o3: reasoning_text, reasoning_opaque
// - Claude: thinking, signature
// - GitHub Copilot: cot_summary, cot_id

// ReasoningFields holds parsed reasoning content and signature from any format.
type ReasoningFields struct {
	Text      string // The reasoning/thinking text content
	Signature string // The signature/opaque/id field
}

// ParseReasoningFromJSON extracts reasoning content from any supported format.
func ParseReasoningFromJSON(data gjson.Result) ReasoningFields {
	var rf ReasoningFields

	// Parse reasoning text from multiple formats (priority order)
	if rc := data.Get("reasoning_content"); rc.Exists() && rc.String() != "" {
		rf.Text = rc.String() // xAI Grok
	} else if rt := data.Get("reasoning_text"); rt.Exists() && rt.String() != "" {
		rf.Text = rt.String() // OpenAI o1/o3
	} else if th := data.Get("thinking"); th.Exists() && th.String() != "" {
		rf.Text = th.String() // Claude
	} else if cs := data.Get("cot_summary"); cs.Exists() && cs.String() != "" {
		rf.Text = cs.String() // GitHub Copilot
	}

	// Parse xAI reasoning_details array (may override text and provide signature)
	if rd := data.Get("reasoning_details"); rd.Exists() && rd.IsArray() {
		for _, detail := range rd.Array() {
			if detail.Get("type").String() == "reasoning.summary" {
				if summary := detail.Get("summary").String(); summary != "" {
					rf.Text = summary
					if format := detail.Get("format").String(); format != "" {
						rf.Signature = format // e.g., "xai-responses-v1"
					}
				}
			}
		}
	}

	// Parse signature from multiple formats (if not already set from reasoning_details)
	if rf.Signature == "" {
		if ro := data.Get("reasoning_opaque"); ro.Exists() && ro.String() != "" {
			rf.Signature = ro.String() // OpenAI o1/o3
		} else if sig := data.Get("signature"); sig.Exists() && sig.String() != "" {
			rf.Signature = sig.String() // Claude
		} else if cid := data.Get("cot_id"); cid.Exists() && cid.String() != "" {
			rf.Signature = cid.String() // GitHub Copilot
		}
	}

	return rf
}

// BuildReasoningDelta creates a delta map with all reasoning format fields populated.
// because some clients (like Cursor) use these fields to detect "thinking" models and show the UI.
func BuildReasoningDelta(reasoning, signature string) map[string]any {
	// Use a default signature if none provided - this is critical for Cursor to show thinking UI
	if signature == "" {
		signature = "thinking"
	}
	delta := map[string]any{
		"role":              "assistant",
		"reasoning_content": reasoning, // xAI Grok
		"reasoning_text":    reasoning, // OpenAI o1/o3
		"thinking":          reasoning, // Claude
		"cot_summary":       reasoning, // GitHub Copilot
		"signature":         signature, // Claude
		"reasoning_opaque":  signature, // OpenAI o1/o3
		"cot_id":            signature, // GitHub Copilot
	}
	return delta
}

// AddReasoningToMessage adds all reasoning format fields to a message map.
func AddReasoningToMessage(msg map[string]any, reasoning, signature string) {
	if reasoning == "" {
		return
	}
	msg["reasoning_content"] = reasoning // xAI Grok
	msg["reasoning_text"] = reasoning    // OpenAI o1/o3
	msg["thinking"] = reasoning          // Claude
	msg["cot_summary"] = reasoning       // GitHub Copilot

	// xAI reasoning_details array format
	msg["reasoning_details"] = []any{
		map[string]any{
			"type":    "reasoning.summary",
			"summary": reasoning,
			"format":  "xai-responses-v1",
			"index":   0,
		},
	}

	if signature != "" {
		msg["reasoning_opaque"] = signature // OpenAI o1/o3
		msg["signature"] = signature        // Claude
		msg["cot_id"] = signature           // GitHub Copilot
	}
}

// GetFirstReasoningSignature extracts the first ThoughtSignature from message content parts.
func GetFirstReasoningSignature(msg Message) string {
	for _, part := range msg.Content {
		if part.Type == ContentTypeReasoning && part.ThoughtSignature != "" {
			return part.ThoughtSignature
		}
	}
	return ""
}
