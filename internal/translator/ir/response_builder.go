package ir

import "github.com/nghyane/llm-mux/internal/json"

// ToClaudeToolID converts tool call ID to Claude format (toolu_...).
// Optimized: avoids allocation if already in correct format.
// Exported so it can be used by from_ir/claude.go
func ToClaudeToolID(id string) string {
	if len(id) > 5 && id[:6] == "toolu_" {
		return id // Already Claude format - fast path
	}
	if len(id) > 4 && id[:5] == "call_" {
		return "toolu_" + id[5:] // Replace call_ with toolu_
	}
	return "toolu_" + id
}

// ResponseBuilder helps construct provider-specific responses from IR messages
type ResponseBuilder struct {
	messages []Message
	usage    *Usage
	model    string
}

// NewResponseBuilder creates a new response builder
func NewResponseBuilder(messages []Message, usage *Usage, model string) *ResponseBuilder {
	return &ResponseBuilder{messages: messages, usage: usage, model: model}
}

// GetLastMessage returns the last message or nil if no messages exist
func (b *ResponseBuilder) GetLastMessage() *Message {
	if len(b.messages) == 0 {
		return nil
	}
	return &b.messages[len(b.messages)-1]
}

// HasContent returns true if the last message has any content or tool calls
func (b *ResponseBuilder) HasContent() bool {
	msg := b.GetLastMessage()
	return msg != nil && (len(msg.Content) > 0 || len(msg.ToolCalls) > 0)
}

// GetTextContent returns combined text content from the last message
func (b *ResponseBuilder) GetTextContent() string {
	if msg := b.GetLastMessage(); msg != nil {
		return CombineTextParts(*msg)
	}
	return ""
}

// GetReasoningContent returns combined reasoning content from the last message
func (b *ResponseBuilder) GetReasoningContent() string {
	if msg := b.GetLastMessage(); msg != nil {
		return CombineReasoningParts(*msg)
	}
	return ""
}

// GetToolCalls returns tool calls from the last message
func (b *ResponseBuilder) GetToolCalls() []ToolCall {
	if msg := b.GetLastMessage(); msg != nil {
		return msg.ToolCalls
	}
	return nil
}

// HasToolCalls returns true if the last message has any tool calls
func (b *ResponseBuilder) HasToolCalls() bool {
	return len(b.GetToolCalls()) > 0
}

// DetermineFinishReason determines the finish reason based on message content
func (b *ResponseBuilder) DetermineFinishReason() string {
	if len(b.GetToolCalls()) > 0 {
		return "tool_calls"
	}
	return "stop"
}

// BuildOpenAIToolCalls builds OpenAI-format tool calls array.
// Includes extra_content.google.thought_signature for Gemini 3 compatibility.
func (b *ResponseBuilder) BuildOpenAIToolCalls() []any {
	toolCalls := b.GetToolCalls()
	if len(toolCalls) == 0 {
		return nil
	}
	result := make([]any, len(toolCalls))
	for i, tc := range toolCalls {
		tcMap := map[string]any{
			"id":   tc.ID,
			"type": "function",
			"function": map[string]any{
				"name":      tc.Name,
				"arguments": tc.Args,
			},
		}
		// Inject thought_signature for Gemini 3 compatibility (OpenAI compat format)
		if len(tc.ThoughtSignature) > 0 {
			tcMap["extra_content"] = map[string]any{
				"google": map[string]any{
					"thought_signature": string(tc.ThoughtSignature),
				},
			}
		}
		result[i] = tcMap
	}
	return result
}

// BuildClaudeContentParts builds Claude-format content parts array.
func (b *ResponseBuilder) BuildClaudeContentParts() []any {
	msg := b.GetLastMessage()
	if msg == nil {
		return []any{}
	}

	// Pre-allocate with estimated capacity
	capacity := len(msg.Content) + len(msg.ToolCalls)
	parts := make([]any, 0, capacity)

	// Add reasoning/thinking content first
	for i := range msg.Content {
		part := &msg.Content[i]
		if part.Type == ContentTypeReasoning && part.Reasoning != "" {
			parts = append(parts, map[string]any{"type": "thinking", "thinking": part.Reasoning})
		}
	}

	// Add text content
	for i := range msg.Content {
		part := &msg.Content[i]
		if part.Type == ContentTypeText && part.Text != "" {
			parts = append(parts, map[string]any{"type": "text", "text": part.Text})
		}
	}

	// Add tool calls
	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]
		toolUse := map[string]any{
			"type":  "tool_use",
			"id":    ToClaudeToolID(tc.ID),
			"name":  tc.Name,
			"input": map[string]any{},
		}
		if tc.Args != "" && tc.Args != "{}" {
			var argsObj any
			if json.Unmarshal([]byte(tc.Args), &argsObj) == nil {
				toolUse["input"] = argsObj
			}
		}
		parts = append(parts, toolUse)
	}

	// Safety Check: Ensure response has content.
	// This must be checked LAST, after processing all potential content types (thinking, text, tools).
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"type": "text", "text": "I apologize, but I encountered an issue generating a response. Please try again."})
	}

	return parts
}

// BuildGeminiContentParts builds Gemini-format content parts array
// Pre-allocates slice capacity based on message content to reduce allocations.
func (b *ResponseBuilder) BuildGeminiContentParts() []any {
	msg := b.GetLastMessage()
	if msg == nil {
		return []any{}
	}

	// Pre-allocate with estimated capacity
	capacity := len(msg.Content) + len(msg.ToolCalls)
	parts := make([]any, 0, capacity)

	// Process all content parts in order to preserve original sequence
	for i := range msg.Content {
		part := &msg.Content[i]
		switch part.Type {
		case ContentTypeReasoning:
			if part.Reasoning != "" {
				p := map[string]any{"text": part.Reasoning, "thought": true}
				if len(part.ThoughtSignature) > 0 {
					p["thoughtSignature"] = string(part.ThoughtSignature)
				}
				parts = append(parts, p)
			}
		case ContentTypeText:
			if part.Text != "" {
				p := map[string]any{"text": part.Text}
				if len(part.ThoughtSignature) > 0 {
					p["thoughtSignature"] = string(part.ThoughtSignature)
				}
				parts = append(parts, p)
			}
		case ContentTypeImage:
			if part.Image != nil && part.Image.Data != "" {
				parts = append(parts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": part.Image.MimeType,
						"data":     part.Image.Data,
					},
				})
			}
		case ContentTypeExecutableCode:
			if part.CodeExecution != nil && part.CodeExecution.Code != "" {
				parts = append(parts, map[string]any{
					"executableCode": map[string]any{
						"language": part.CodeExecution.Language,
						"code":     part.CodeExecution.Code,
					},
				})
			}
		case ContentTypeCodeResult:
			if part.CodeExecution != nil {
				parts = append(parts, map[string]any{
					"codeExecutionResult": map[string]any{
						"outcome": part.CodeExecution.Outcome,
						"output":  part.CodeExecution.Output,
					},
				})
			}
		}
	}

	// Add tool calls as functionCall parts
	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]
		fcPart := map[string]any{
			"functionCall": map[string]any{
				"name": tc.Name,
				"args": ParseToolCallArgs(tc.Args),
			},
		}
		// Include thoughtSignature at part level (required by Gemini 3 for multi-turn)
		if len(tc.ThoughtSignature) > 0 {
			fcPart["thoughtSignature"] = string(tc.ThoughtSignature)
		}
		parts = append(parts, fcPart)
	}

	return parts
}

// BuildUsageMap builds a usage statistics map with detailed token breakdown
func (b *ResponseBuilder) BuildUsageMap() map[string]any {
	if b.usage == nil {
		return nil
	}
	usageMap := map[string]any{
		"prompt_tokens":     b.usage.PromptTokens,
		"completion_tokens": b.usage.CompletionTokens,
		"total_tokens":      b.usage.TotalTokens,
	}

	// Add prompt_tokens_details if available
	if b.usage.PromptTokensDetails != nil {
		promptDetails := make(map[string]any)
		if b.usage.PromptTokensDetails.CachedTokens > 0 {
			promptDetails["cached_tokens"] = b.usage.PromptTokensDetails.CachedTokens
		}
		if b.usage.PromptTokensDetails.AudioTokens > 0 {
			promptDetails["audio_tokens"] = b.usage.PromptTokensDetails.AudioTokens
		}
		if len(promptDetails) > 0 {
			usageMap["prompt_tokens_details"] = promptDetails
		}
	}

	// Add completion_tokens_details if available
	if b.usage.CompletionTokensDetails != nil {
		completionDetails := make(map[string]any)
		if b.usage.CompletionTokensDetails.ReasoningTokens > 0 {
			completionDetails["reasoning_tokens"] = b.usage.CompletionTokensDetails.ReasoningTokens
		}
		if b.usage.CompletionTokensDetails.AudioTokens > 0 {
			completionDetails["audio_tokens"] = b.usage.CompletionTokensDetails.AudioTokens
		}
		if b.usage.CompletionTokensDetails.AcceptedPredictionTokens > 0 {
			completionDetails["accepted_prediction_tokens"] = b.usage.CompletionTokensDetails.AcceptedPredictionTokens
		}
		if b.usage.CompletionTokensDetails.RejectedPredictionTokens > 0 {
			completionDetails["rejected_prediction_tokens"] = b.usage.CompletionTokensDetails.RejectedPredictionTokens
		}
		if len(completionDetails) > 0 {
			usageMap["completion_tokens_details"] = completionDetails
		}
	}

	return usageMap
}
