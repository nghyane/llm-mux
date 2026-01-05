package ir

// ToClaudeToolID converts tool call ID to Claude format (toolu_...).
// Optimized: avoids allocation if already in correct format.
// Exported so it can be used by from_ir/claude.go
func ToClaudeToolID(id string) string {
	if len(id) >= 6 && id[:6] == "toolu_" {
		return id // Already Claude format - fast path
	}
	if len(id) >= 5 && id[:5] == "call_" {
		return "toolu_" + id[5:] // Replace call_ with toolu_
	}
	return "toolu_" + id
}

// ResponseBuilder helps construct provider-specific responses from IR messages
type ResponseBuilder struct {
	messages        []Message
	usage           *Usage
	model           string
	thinkingEnabled bool
}

// NewResponseBuilder creates a new response builder
func NewResponseBuilder(messages []Message, usage *Usage, model string, thinkingEnabled bool) *ResponseBuilder {
	return &ResponseBuilder{messages: messages, usage: usage, model: model, thinkingEnabled: thinkingEnabled}
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

	// Use the consolidated function with includeToolCalls=true and thinkingEnabled=b.thinkingEnabled for responses
	parts := BuildClaudeContentParts(*msg, true, b.thinkingEnabled)

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

// BuildClaudeContentParts builds Claude-format content parts array from a message.
// includeToolCalls: whether to include tool calls in the output
// thinkingEnabled: whether thinking content is enabled (affects placeholder logic)
func BuildClaudeContentParts(msg Message, includeToolCalls bool, thinkingEnabled bool) []any {
	// Pre-allocate with estimated capacity
	capacity := len(msg.Content)
	if includeToolCalls {
		capacity += len(msg.ToolCalls)
	}
	parts := make([]any, 0, capacity)

	// Check if we have thinking content and text/tool content
	hasThinking := false
	hasNonThinkingContent := false
	for i := range msg.Content {
		switch msg.Content[i].Type {
		case ContentTypeReasoning:
			// Consider thinking present if we have text OR a valid signature
			if msg.Content[i].Reasoning != "" || len(msg.Content[i].ThoughtSignature) > 0 {
				hasThinking = true
			}
		case ContentTypeRedactedThinking:
			if msg.Content[i].RedactedData != "" {
				hasThinking = true
			}
		case ContentTypeText, ContentTypeImage, ContentTypeFile, ContentTypeToolResult:
			hasNonThinkingContent = true
		}
	}

	// NOTE: Thinking placeholder injection is handled at translator_wrapper level
	// by disabling thinking when history has tool_use without thinking blocks.
	// Claude Vertex API requires valid cryptographic signatures that we cannot fake.

	for i := range msg.Content {
		p := &msg.Content[i]
		switch p.Type {
		case ContentTypeReasoning:
			// CRITICAL: Skip thinking blocks when thinking is disabled
			// Claude API rejects requests with thinking blocks in history when thinking is disabled
			if !thinkingEnabled {
				continue
			}
			// CRITICAL: Skip thinking blocks without valid signature
			// Vertex/Claude API requires valid cryptographic signatures - we cannot fake them
			// Thinking blocks without signatures will cause 400 error: "thinking.signature: Field required"
			if !IsValidThoughtSignature(p.ThoughtSignature) {
				continue
			}
			if p.Reasoning != "" {
				thinkingBlock := map[string]any{"type": ClaudeBlockThinking, "thinking": p.Reasoning}
				thinkingBlock["signature"] = string(p.ThoughtSignature)
				parts = append(parts, thinkingBlock)
			}
		case ContentTypeRedactedThinking:
			// CRITICAL: Skip redacted thinking blocks when thinking is disabled
			if !thinkingEnabled {
				continue
			}
			if p.RedactedData != "" {
				parts = append(parts, map[string]any{
					"type": ClaudeBlockRedactedThinking,
					"data": p.RedactedData,
				})
			}
		case ContentTypeText:
			if p.Text != "" {
				textBlock := map[string]any{"type": ClaudeBlockText, "text": p.Text}
				if len(p.Citations) > 0 {
					citations := make([]any, len(p.Citations))
					for i, c := range p.Citations {
						citation := map[string]any{
							"type": c.Type,
						}
						// Index fields - always include (0 is valid for first document/char/page/block)
						// These are required for char_location, page_location, content_block_location types
						switch c.Type {
						case "char_location":
							citation["document_index"] = c.DocumentIndex
							citation["start_char_index"] = c.StartCharIndex
							citation["end_char_index"] = c.EndCharIndex
						case "page_location":
							citation["document_index"] = c.DocumentIndex
							citation["start_page_number"] = c.StartPageNumber
							citation["end_page_number"] = c.EndPageNumber
						case "content_block_location":
							citation["document_index"] = c.DocumentIndex
							citation["start_block_index"] = c.StartBlockIndex
							citation["end_block_index"] = c.EndBlockIndex
						case "web_search_result_location":
							// Web search uses encrypted_index
							if c.EncryptedIndex != "" {
								citation["encrypted_index"] = c.EncryptedIndex
							}
						case "search_result_location":
							citation["search_result_index"] = c.SearchResultIndex
							citation["start_block_index"] = c.StartBlockIndex
							citation["end_block_index"] = c.EndBlockIndex
						}
						// Optional string fields - only include if non-empty
						if c.URL != "" {
							citation["url"] = c.URL
						}
						if c.Title != "" {
							citation["title"] = c.Title
						}
						if c.FileID != "" {
							citation["file_id"] = c.FileID
						}
						if c.CitedText != "" {
							citation["cited_text"] = c.CitedText
						}
						if c.DocumentTitle != "" {
							citation["document_title"] = c.DocumentTitle
						}
						if c.Source != "" {
							citation["source"] = c.Source
						}
						citations[i] = citation
					}
					textBlock["citations"] = citations
				}
				parts = append(parts, textBlock)
			}
		case ContentTypeImage:
			if p.Image != nil {
				imgBlock := map[string]any{"type": ClaudeBlockImage}
				if p.Image.Data != "" {
					// Base64-encoded image
					imgBlock["source"] = map[string]any{
						"type":       "base64",
						"media_type": p.Image.MimeType,
						"data":       p.Image.Data,
					}
				} else if p.Image.URL != "" {
					// URL-referenced image
					imgBlock["source"] = map[string]any{
						"type": "url",
						"url":  p.Image.URL,
					}
				} else if p.Image.FileID != "" {
					// Claude Files API reference
					imgBlock["source"] = map[string]any{
						"type":    "file",
						"file_id": p.Image.FileID,
					}
				}
				if _, hasSource := imgBlock["source"]; hasSource {
					parts = append(parts, imgBlock)
				}
			}
		case ContentTypeFile:
			if p.File != nil {
				// Claude document block
				docBlock := map[string]any{"type": ClaudeBlockDocument}
				if p.File.Filename != "" {
					docBlock["title"] = p.File.Filename
				}
				source := map[string]any{}
				if p.File.FileData != "" {
					source["type"] = "base64"
					source["data"] = p.File.FileData
					// Use stored MimeType or default to application/pdf
					if p.File.MimeType != "" {
						source["media_type"] = p.File.MimeType
					} else {
						source["media_type"] = "application/pdf"
					}
				} else if p.File.FileURL != "" {
					source["type"] = "url"
					source["url"] = p.File.FileURL
				} else if p.File.FileID != "" {
					source["type"] = "file"
					source["file_id"] = p.File.FileID
				}
				if len(source) > 0 {
					docBlock["source"] = source
					parts = append(parts, docBlock)
				}
			}
		case ContentTypeToolResult:
			if p.ToolResult != nil {
				// Build the tool_result block
				toolResultBlock := map[string]any{
					"type":        ClaudeBlockToolResult,
					"tool_use_id": p.ToolResult.ToolCallID,
				}
				// Add is_error if tool execution failed
				if p.ToolResult.IsError {
					toolResultBlock["is_error"] = true
				}

				// Check if we have images or files
				hasMedia := len(p.ToolResult.Images) > 0 || len(p.ToolResult.Files) > 0

				if hasMedia {
					// Build content array with text, images, and documents
					var content []any

					// Add text content if present
					if p.ToolResult.Result != "" {
						content = append(content, map[string]any{
							"type": "text",
							"text": p.ToolResult.Result,
						})
					}

					// Add images if present
					for _, img := range p.ToolResult.Images {
						if img.Data != "" {
							content = append(content, map[string]any{
								"type": ClaudeBlockImage,
								"source": map[string]any{
									"type":       "base64",
									"media_type": img.MimeType,
									"data":       img.Data,
								},
							})
						} else if img.URL != "" {
							content = append(content, map[string]any{
								"type": ClaudeBlockImage,
								"source": map[string]any{
									"type": "url",
									"url":  img.URL,
								},
							})
						} else if img.FileID != "" {
							content = append(content, map[string]any{
								"type": ClaudeBlockImage,
								"source": map[string]any{
									"type":    "file",
									"file_id": img.FileID,
								},
							})
						}
					}

					// Add files/documents if present
					for _, file := range p.ToolResult.Files {
						docBlock := map[string]any{"type": ClaudeBlockDocument}
						if file.Filename != "" {
							docBlock["title"] = file.Filename
						}
						source := map[string]any{}
						if file.FileData != "" {
							source["type"] = "base64"
							source["data"] = file.FileData
							if file.MimeType != "" {
								source["media_type"] = file.MimeType
							}
						} else if file.FileURL != "" {
							source["type"] = "url"
							source["url"] = file.FileURL
						} else if file.FileID != "" {
							source["type"] = "file"
							source["file_id"] = file.FileID
						}
						if len(source) > 0 {
							docBlock["source"] = source
							content = append(content, docBlock)
						}
					}

					toolResultBlock["content"] = content
				} else {
					// No images/files, use simple string content
					toolResultBlock["content"] = p.ToolResult.Result
				}

				parts = append(parts, toolResultBlock)
			}
		}
	}
	if includeToolCalls {
		for i := range msg.ToolCalls {
			tc := &msg.ToolCalls[i]
			toolUse := map[string]any{"type": ClaudeBlockToolUse, "id": ToClaudeToolID(tc.ID), "name": tc.Name}
			toolUse["input"] = ParseToolCallArgs(tc.Args)
			parts = append(parts, toolUse)
		}
	}

	// Client requirement: Response must have text or tool calls, not just thinking
	// If we only have thinking content (no text, no tool calls), add text block with space
	if hasThinking && !hasNonThinkingContent && len(msg.ToolCalls) == 0 {
		parts = append(parts, map[string]any{"type": ClaudeBlockText, "text": " "})
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

func HasThinkingParts(msg Message) bool {
	for i := range msg.Content {
		switch msg.Content[i].Type {
		case ContentTypeReasoning:
			if msg.Content[i].Reasoning != "" || len(msg.Content[i].ThoughtSignature) > 0 {
				return true
			}
		case ContentTypeRedactedThinking:
			if msg.Content[i].RedactedData != "" {
				return true
			}
		}
	}
	return false
}
