package ir

import (
	stdjson "encoding/json"
	"strings"

	"github.com/nghyane/llm-mux/internal/json"
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
		case ContentTypeRedactedThinking:
			if part.RedactedData != "" {
				reasoningCount++
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
		if (part.Type == ContentTypeReasoning && part.Reasoning != "") ||
			(part.Type == ContentTypeRedactedThinking && part.RedactedData != "") {
			count++
			if part.Type == ContentTypeReasoning {
				single = part.Reasoning
			}
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
		// Note: ContentTypeRedactedThinking is counted but not included in combined string
		// as it contains encrypted data, not readable text
	}
	return b.String()
}

// BuildToolCallMap creates a map of tool call ID to function name.
func BuildToolCallMap(messages []Message) map[string]string {
	m := make(map[string]string, 8)
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
	m := make(map[string]*ToolResultPart, 8)
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
// Handles legacy format where client doesn't provide IDs (ID is empty or equals Name).
func BuildToolMaps(messages []Message) (map[string]string, map[string]*ToolResultPart) {
	idToName := make(map[string]string, 8)
	results := make(map[string]*ToolResultPart, 8)

	// For legacy format (no IDs), we need FIFO matching by name
	// Track: name → queue of generated IDs
	nameToIDs := make(map[string][]string, 8)

	for _, msg := range messages {
		switch msg.Role {
		case RoleAssistant:
			for i := range msg.ToolCalls {
				tc := &msg.ToolCalls[i]
				// Legacy format: ID is empty or equals Name
				if tc.ID == "" || tc.ID == tc.Name {
					tc.ID = GenToolCallID()
				}
				idToName[tc.ID] = tc.Name
				nameToIDs[tc.Name] = append(nameToIDs[tc.Name], tc.ID)
			}
		case RoleTool, RoleUser:
			for i := range msg.Content {
				part := &msg.Content[i]
				if part.Type == ContentTypeToolResult && part.ToolResult != nil {
					tr := part.ToolResult
					// Legacy format: ToolCallID is a tool name, match to pending call
					originalID := tr.ToolCallID
					if queue := nameToIDs[originalID]; len(queue) > 0 {
						tr.ToolCallID = queue[0]
						nameToIDs[originalID] = queue[1:]
					}
					results[tr.ToolCallID] = tr
				}
			}
		}
	}
	return idToName, results
}

var emptyJSONObject = stdjson.RawMessage("{}")

func ArgsAsRaw(args string) stdjson.RawMessage {
	trimmed := strings.TrimSpace(args)
	if trimmed == "" || trimmed == "{}" {
		return emptyJSONObject
	}
	if !json.Valid([]byte(trimmed)) {
		b, _ := json.Marshal(trimmed)
		return stdjson.RawMessage(b)
	}
	return stdjson.RawMessage(trimmed)
}

// ParseOpenAIStyleToolCalls parses tool_calls array in OpenAI/Ollama format.
func ParseOpenAIStyleToolCalls(toolCalls []gjson.Result) []ToolCall {
	result := make([]ToolCall, 0, len(toolCalls))
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
// Returns string representation for backward compatibility with Claude API.
func GetFirstReasoningSignature(msg Message) string {
	for _, part := range msg.Content {
		if part.Type == ContentTypeReasoning && len(part.ThoughtSignature) > 0 {
			return string(part.ThoughtSignature)
		}
	}
	return ""
}
