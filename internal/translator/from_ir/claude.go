package from_ir

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// Claude user tracking (matches old translator behavior)
var (
	claudeUser    = ""
	claudeAccount = ""
	claudeSession = ""
)

func toClaudeToolID(id string) string { return ir.ToClaudeToolID(id) }

// ClaudeProvider handles conversion to Claude Messages API format.
type ClaudeProvider struct{}

// ClaudeStreamState tracks state for streaming response conversion.
type ClaudeStreamState struct {
	MessageID            string
	Model                string
	MessageStartSent     bool
	TextBlockStarted     bool
	CurrentBlockType     string // "text" or "thinking"
	TextBlockIndex       int    // Current block index
	HasToolCalls         bool
	HasTextContent       bool // Track if we emitted text (not just thinking)
	FinishSent           bool
	EstimatedInputTokens int64 // Pre-calculated input tokens for message_start
}

// NewClaudeStreamState creates a new streaming state tracker.
func NewClaudeStreamState() *ClaudeStreamState {
	return &ClaudeStreamState{TextBlockIndex: 0}
}

func (p *ClaudeProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	if claudeAccount == "" {
		u, _ := uuid.NewRandom()
		claudeAccount = u.String()
	}
	if claudeSession == "" {
		u, _ := uuid.NewRandom()
		claudeSession = u.String()
	}
	if claudeUser == "" {
		sum := sha256.Sum256([]byte(claudeAccount + claudeSession))
		claudeUser = hex.EncodeToString(sum[:])
	}
	userID := fmt.Sprintf("user_%s_account_%s_session_%s", claudeUser, claudeAccount, claudeSession)

	root := map[string]any{
		"model":      req.Model,
		"max_tokens": ir.ClaudeDefaultMaxTokens,
		"metadata":   map[string]any{"user_id": userID},
		"messages":   []any{},
	}

	if req.MaxTokens != nil {
		root["max_tokens"] = *req.MaxTokens
	}
	if req.Temperature != nil {
		root["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		root["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		root["top_k"] = *req.TopK
	}
	if len(req.StopSequences) > 0 {
		root["stop_sequences"] = req.StopSequences
	}

	if req.Thinking != nil {
		thinking := map[string]any{}
		budget := int32(0)
		if req.Thinking.ThinkingBudget != nil {
			budget = *req.Thinking.ThinkingBudget
		}
		if req.Thinking.IncludeThoughts && budget != 0 {
			thinking["type"] = "enabled"
			if budget > 0 {
				thinking["budget_tokens"] = budget
			}
		} else if budget == 0 && !req.Thinking.IncludeThoughts {
			thinking["type"] = "disabled"
		}
		if len(thinking) > 0 {
			root["thinking"] = thinking
		}
	}

	// Check if thinking is enabled for this request
	thinkingEnabled := false
	if req.Thinking != nil {
		// Relaxed check: if Thinking struct exists, we likely need to comply
		thinkingEnabled = true
	}
	// Force enable for known thinking models to ensure protocol compliance in history
	if ir.IsThinkingModel(req.Model) {
		thinkingEnabled = true
	}

	var messages []any
	for _, msg := range req.Messages {
		switch msg.Role {
		case ir.RoleSystem:
			if text := ir.CombineTextParts(msg); text != "" {
				root["system"] = text
			}
		case ir.RoleUser:
			if parts := buildClaudeContentParts(msg, false, false); len(parts) > 0 {
				msgObj := map[string]any{"role": ir.ClaudeRoleUser, "content": parts}
				// Add cache_control if present
				if msg.CacheControl != nil {
					cacheCtrl := map[string]any{"type": msg.CacheControl.Type}
					if msg.CacheControl.TTL != nil {
						cacheCtrl["ttl"] = *msg.CacheControl.TTL
					}
					msgObj["cache_control"] = cacheCtrl
				}
				messages = append(messages, msgObj)
			}
		case ir.RoleAssistant:
			// Pass thinkingEnabled to inject placeholder if needed
			if parts := buildClaudeContentParts(msg, isAssistantWithToolUse(msg), thinkingEnabled); len(parts) > 0 {
				msgObj := map[string]any{"role": ir.ClaudeRoleAssistant, "content": parts}
				// Add cache_control if present
				if msg.CacheControl != nil {
					cacheCtrl := map[string]any{"type": msg.CacheControl.Type}
					if msg.CacheControl.TTL != nil {
						cacheCtrl["ttl"] = *msg.CacheControl.TTL
					}
					msgObj["cache_control"] = cacheCtrl
				}
				messages = append(messages, msgObj)
			}
		case ir.RoleTool:
			for _, part := range msg.Content {
				if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
					toolResultBlock := map[string]any{
						"type":        ir.ClaudeBlockToolResult,
						"tool_use_id": part.ToolResult.ToolCallID,
					}
					// Add is_error if tool execution failed
					if part.ToolResult.IsError {
						toolResultBlock["is_error"] = true
					}
					// Check if we have images or files (need array content)
					hasMedia := len(part.ToolResult.Images) > 0 || len(part.ToolResult.Files) > 0
					if hasMedia {
						var content []any
						if part.ToolResult.Result != "" {
							content = append(content, map[string]any{"type": "text", "text": part.ToolResult.Result})
						}
						for _, img := range part.ToolResult.Images {
							if img.Data != "" {
								content = append(content, map[string]any{
									"type": ir.ClaudeBlockImage,
									"source": map[string]any{
										"type": "base64", "media_type": img.MimeType, "data": img.Data,
									},
								})
							} else if img.URL != "" {
								content = append(content, map[string]any{
									"type":   ir.ClaudeBlockImage,
									"source": map[string]any{"type": "url", "url": img.URL},
								})
							} else if img.FileID != "" {
								content = append(content, map[string]any{
									"type":   ir.ClaudeBlockImage,
									"source": map[string]any{"type": "file", "file_id": img.FileID},
								})
							}
						}
						for _, file := range part.ToolResult.Files {
							docBlock := map[string]any{"type": ir.ClaudeBlockDocument}
							if file.Filename != "" {
								docBlock["title"] = file.Filename
							}
							source := map[string]any{}
							if file.FileData != "" {
								source["type"], source["data"] = "base64", file.FileData
								if file.MimeType != "" {
									source["media_type"] = file.MimeType
								}
							} else if file.FileURL != "" {
								source["type"], source["url"] = "url", file.FileURL
							} else if file.FileID != "" {
								source["type"], source["file_id"] = "file", file.FileID
							}
							if len(source) > 0 {
								docBlock["source"] = source
								content = append(content, docBlock)
							}
						}
						toolResultBlock["content"] = content
					} else {
						toolResultBlock["content"] = part.ToolResult.Result
					}
					messages = append(messages, map[string]any{
						"role":    ir.ClaudeRoleUser,
						"content": []any{toolResultBlock},
					})
				}
			}
		}
	}
	root["messages"] = messages

	// Build tools array (function tools + built-in tools from metadata)
	var tools []any
	for _, t := range req.Tools {
		tool := map[string]any{"name": t.Name, "description": t.Description}
		if len(t.Parameters) > 0 {
			tool["input_schema"] = ir.CleanJsonSchemaForClaude(copyMap(t.Parameters))
		} else {
			tool["input_schema"] = map[string]any{
				"type": "object", "properties": map[string]any{}, "additionalProperties": false, "$schema": ir.JSONSchemaDraft202012,
			}
		}
		tools = append(tools, tool)
	}

	// Add built-in tools from Metadata
	if req.Metadata != nil {
		// web_search tool
		if gsConfig, ok := req.Metadata[ir.MetaGoogleSearch]; ok {
			webSearchTool := map[string]any{"name": "web_search"}
			// Use preserved original type or default
			if cfg, ok := gsConfig.(map[string]any); ok {
				if origType, ok := cfg["_original_type"].(string); ok && origType != "" {
					webSearchTool["type"] = origType
				} else {
					webSearchTool["type"] = "web_search_20250305"
				}
				if maxUses, ok := cfg["max_uses"]; ok {
					webSearchTool["max_uses"] = maxUses
				}
			} else {
				webSearchTool["type"] = "web_search_20250305"
			}
			tools = append(tools, webSearchTool)
		}

		// computer use tool
		if compConfig, ok := req.Metadata[ir.MetaClaudeComputer]; ok {
			computerTool := map[string]any{"name": "computer"}
			if cfg, ok := compConfig.(map[string]any); ok {
				if origType, ok := cfg["_original_type"].(string); ok && origType != "" {
					computerTool["type"] = origType
				} else {
					computerTool["type"] = "computer_20241022"
				}
				if dw, ok := cfg["display_width_px"]; ok {
					computerTool["display_width_px"] = dw
				}
				if dh, ok := cfg["display_height_px"]; ok {
					computerTool["display_height_px"] = dh
				}
				if dn, ok := cfg["display_number"]; ok {
					computerTool["display_number"] = dn
				}
			} else {
				computerTool["type"] = "computer_20241022"
			}
			tools = append(tools, computerTool)
		}

		// bash tool
		if bashConfig, ok := req.Metadata[ir.MetaClaudeBash]; ok {
			bashTool := map[string]any{"name": "bash"}
			if cfg, ok := bashConfig.(map[string]any); ok {
				if origType, ok := cfg["_original_type"].(string); ok && origType != "" {
					bashTool["type"] = origType
				} else {
					bashTool["type"] = "bash_20241022"
				}
			} else {
				bashTool["type"] = "bash_20241022"
			}
			tools = append(tools, bashTool)
		}

		// text editor tool
		if editorConfig, ok := req.Metadata[ir.MetaClaudeTextEditor]; ok {
			editorTool := map[string]any{"name": "str_replace_editor"}
			if cfg, ok := editorConfig.(map[string]any); ok {
				if origType, ok := cfg["_original_type"].(string); ok && origType != "" {
					editorTool["type"] = origType
				} else {
					editorTool["type"] = "text_editor_20241022"
				}
			} else {
				editorTool["type"] = "text_editor_20241022"
			}
			tools = append(tools, editorTool)
		}
	}

	if len(tools) > 0 {
		root["tools"] = tools
	}

	// Add MCP servers if present
	if len(req.MCPServers) > 0 {
		var mcpServers []any
		for _, srv := range req.MCPServers {
			mcpServer := map[string]any{
				"type": srv.Type,
				"url":  srv.URL,
				"name": srv.Name,
			}
			if srv.AuthorizationToken != "" {
				mcpServer["authorization_token"] = srv.AuthorizationToken
			}
			if srv.ToolConfiguration != nil {
				mcpServer["tool_configuration"] = srv.ToolConfiguration
			}
			mcpServers = append(mcpServers, mcpServer)
		}
		root["mcp_servers"] = mcpServers
	}

	if len(req.Metadata) > 0 {
		meta := root["metadata"].(map[string]any)
		for k, v := range req.Metadata {
			// Skip built-in tool metadata (already handled above)
			if k == ir.MetaGoogleSearch || k == ir.MetaClaudeComputer ||
				k == ir.MetaClaudeBash || k == ir.MetaClaudeTextEditor {
				continue
			}
			meta[k] = v
		}
	}

	return json.Marshal(root)
}

func (p *ClaudeProvider) ParseResponse(responseJSON []byte) ([]ir.Message, *ir.Usage, error) {
	parsed, err := ir.ParseAndValidateJSON(responseJSON)
	if err != nil {
		return nil, nil, err
	}
	usage := ir.ParseClaudeUsage(parsed.Get("usage"))

	content := parsed.Get("content")
	if !content.Exists() || !content.IsArray() {
		return nil, usage, nil
	}

	msg := ir.Message{Role: ir.RoleAssistant}
	for _, block := range content.Array() {
		ir.ParseClaudeContentBlock(block, &msg)
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil, usage, nil
	}
	return []ir.Message{msg}, usage, nil
}

func (p *ClaudeProvider) ParseStreamChunk(chunkJSON []byte) ([]ir.UnifiedEvent, error) {
	return p.ParseStreamChunkWithState(chunkJSON, nil)
}

func (p *ClaudeProvider) ParseStreamChunkWithState(chunkJSON []byte, state *ir.ClaudeStreamParserState) ([]ir.UnifiedEvent, error) {
	data := ir.ExtractSSEData(chunkJSON)
	if len(data) == 0 {
		return nil, nil
	}
	parsed, err := ir.ParseAndValidateJSON(data)
	if err != nil {
		return nil, nil // Ignore invalid chunks in streaming
	}

	switch parsed.Get("type").String() {
	case ir.ClaudeSSEContentBlockStart:
		return ir.ParseClaudeContentBlockStart(parsed, state), nil
	case ir.ClaudeSSEContentBlockDelta:
		if state != nil {
			return ir.ParseClaudeStreamDeltaWithState(parsed, state), nil
		}
		return ir.ParseClaudeStreamDelta(parsed), nil
	case ir.ClaudeSSEContentBlockStop:
		return ir.ParseClaudeContentBlockStop(parsed, state), nil
	case ir.ClaudeSSEMessageDelta:
		return ir.ParseClaudeMessageDelta(parsed), nil
	case ir.ClaudeSSEMessageStop:
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}}, nil
	case ir.ClaudeSSEError:
		msg := parsed.Get("error.message").String()
		if msg == "" {
			msg = "Unknown Claude API error"
		}
		return []ir.UnifiedEvent{{Type: ir.EventTypeError, Error: fmt.Errorf("%s", msg)}}, nil
	}
	return nil, nil
}

// ToClaudeSSE converts event to Claude SSE format.
// Optimized: uses pooled builders and fast path for text deltas.
func ToClaudeSSE(event ir.UnifiedEvent, model, messageID string, state *ClaudeStreamState) ([]byte, error) {
	// Get pooled builder
	result := ir.GetStringBuilder()
	defer ir.PutStringBuilder(result)

	if state != nil && !state.MessageStartSent {
		state.MessageStartSent = true
		state.Model, state.MessageID = model, messageID

		// Use pre-calculated input tokens if available (from local tokenizer)
		inputTokens := state.EstimatedInputTokens
		result.WriteString(formatSSE(ir.ClaudeSSEMessageStart, map[string]any{
			"type": ir.ClaudeSSEMessageStart,
			"message": map[string]any{
				"id": messageID, "type": "message", "role": ir.ClaudeRoleAssistant,
				"content": []any{}, "model": model, "stop_reason": nil, "stop_sequence": nil,
				"usage": map[string]any{
					"input_tokens":  inputTokens,
					"output_tokens": int64(1), // Match Claude API: first output token
					// Include cache fields for schema completeness (will be updated in message_delta)
					"cache_creation_input_tokens": int64(0),
					"cache_read_input_tokens":     int64(0),
				},
			},
		}))
	}

	switch event.Type {
	case ir.EventTypeToken:
		emitTextDeltaTo(result, event.Content, state)
	case ir.EventTypeReasoning:
		emitThinkingDeltaTo(result, event.Reasoning, event.ThoughtSignature, state)
	case ir.EventTypeToolCall:
		if event.ToolCall != nil {
			emitToolCallTo(result, event.ToolCall, state)
		}
	case ir.EventTypeFinish:
		if state != nil && state.FinishSent {
			return nil, nil
		}
		if state != nil {
			state.FinishSent = true
		}
		emitFinishTo(result, event.Usage, state)
	case ir.EventTypeError:
		result.WriteString(formatSSE(ir.ClaudeSSEError, map[string]any{
			"type": ir.ClaudeSSEError, "error": map[string]any{"type": "api_error", "message": errMsg(event.Error)},
		}))
	}

	if result.Len() == 0 {
		return nil, nil
	}
	// Copy to avoid retaining pooled buffer
	return []byte(result.String()), nil
}

// ToClaudeResponse converts messages to complete Claude response.
func ToClaudeResponse(messages []ir.Message, usage *ir.Usage, model, messageID string) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model)
	content := builder.BuildClaudeContentParts()

	response := map[string]any{
		"id": messageID, "type": "message", "role": ir.ClaudeRoleAssistant,
		"content": content, "model": model, "stop_reason": ir.ClaudeStopEndTurn,
	}
	if builder.HasToolCalls() {
		response["stop_reason"] = ir.ClaudeStopToolUse
	}
	if usage != nil {
		// Claude spec: output_tokens includes both regular output and thinking/reasoning tokens
		outputTokens := usage.CompletionTokens + int64(usage.ThoughtsTokenCount)
		usageMap := map[string]any{"input_tokens": usage.PromptTokens, "output_tokens": outputTokens}
		if usage.CacheCreationInputTokens > 0 {
			usageMap["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
		}
		// Check CacheReadInputTokens first, then fall back to PromptTokensDetails.CachedTokens
		if usage.CacheReadInputTokens > 0 {
			usageMap["cache_read_input_tokens"] = usage.CacheReadInputTokens
		} else if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
			usageMap["cache_read_input_tokens"] = usage.PromptTokensDetails.CachedTokens
		}
		response["usage"] = usageMap
	}
	return json.Marshal(response)
}

func buildClaudeContentParts(msg ir.Message, includeToolCalls bool, thinkingEnabled bool) []any {
	// Pre-allocate with estimated capacity
	capacity := len(msg.Content)
	if includeToolCalls {
		capacity += len(msg.ToolCalls)
	}
	parts := make([]any, 0, capacity)

	// Check if we have thinking content and text/tool content
	hasThinking := false
	hasTextOrImage := false
	for i := range msg.Content {
		switch msg.Content[i].Type {
		case ir.ContentTypeReasoning:
			if msg.Content[i].Reasoning != "" {
				hasThinking = true
			}
		case ir.ContentTypeText, ir.ContentTypeImage:
			hasTextOrImage = true
		}
	}

	for i := range msg.Content {
		p := &msg.Content[i]
		switch p.Type {
		case ir.ContentTypeReasoning:
			if p.Reasoning != "" {
				thinkingBlock := map[string]any{"type": ir.ClaudeBlockThinking, "thinking": p.Reasoning}
				if len(p.ThoughtSignature) > 0 {
					thinkingBlock["signature"] = string(p.ThoughtSignature)
				}
				parts = append(parts, thinkingBlock)
			}
		case ir.ContentTypeText:
			if p.Text != "" {
				parts = append(parts, map[string]any{"type": ir.ClaudeBlockText, "text": p.Text})
			}
		case ir.ContentTypeImage:
			if p.Image != nil {
				imgBlock := map[string]any{"type": ir.ClaudeBlockImage}
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
		case ir.ContentTypeFile:
			if p.File != nil {
				// Claude document block
				docBlock := map[string]any{"type": ir.ClaudeBlockDocument}
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
		case ir.ContentTypeToolResult:
			if p.ToolResult != nil {
				// Build the tool_result block
				toolResultBlock := map[string]any{
					"type":        ir.ClaudeBlockToolResult,
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
								"type": ir.ClaudeBlockImage,
								"source": map[string]any{
									"type":       "base64",
									"media_type": img.MimeType,
									"data":       img.Data,
								},
							})
						} else if img.URL != "" {
							content = append(content, map[string]any{
								"type": ir.ClaudeBlockImage,
								"source": map[string]any{
									"type": "url",
									"url":  img.URL,
								},
							})
						} else if img.FileID != "" {
							content = append(content, map[string]any{
								"type": ir.ClaudeBlockImage,
								"source": map[string]any{
									"type":    "file",
									"file_id": img.FileID,
								},
							})
						}
					}

					// Add files/documents if present
					for _, file := range p.ToolResult.Files {
						docBlock := map[string]any{"type": ir.ClaudeBlockDocument}
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
			toolUse := map[string]any{"type": ir.ClaudeBlockToolUse, "id": toClaudeToolID(tc.ID), "name": tc.Name}
			toolUse["input"] = ir.ParseToolCallArgs(tc.Args)
			parts = append(parts, toolUse)
		}
	}

	// Client requirement: Response must have text or tool calls, not just thinking
	// If we only have thinking content (no text, no tool calls), add text block with space
	if hasThinking && !hasTextOrImage && len(msg.ToolCalls) == 0 {
		parts = append(parts, map[string]any{"type": ir.ClaudeBlockText, "text": " "})
	}

	return parts
}

// sseBuffer wraps a byte slice for pooling (pointer type for sync.Pool).
type sseBuffer struct {
	data []byte
}

// sseBufferPool provides reusable buffers for SSE formatting.
var sseBufferPool = sync.Pool{
	New: func() any {
		return &sseBuffer{data: make([]byte, 0, 512)}
	},
}

func formatSSE(eventType string, data any) string {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return ""
	}

	// Calculate required size: "event: " + eventType + "\ndata: " + json + "\n\n"
	size := 7 + len(eventType) + 7 + len(jsonData) + 2

	// Get buffer from pool
	bufWrapper := sseBufferPool.Get().(*sseBuffer)
	buf := bufWrapper.data[:0]

	// Grow if needed
	if cap(buf) < size {
		buf = make([]byte, 0, size)
	}

	// Build SSE message
	buf = append(buf, "event: "...)
	buf = append(buf, eventType...)
	buf = append(buf, "\ndata: "...)
	buf = append(buf, jsonData...)
	buf = append(buf, "\n\n"...)

	result := string(buf)

	// Return buffer to pool
	bufWrapper.data = buf[:0]
	sseBufferPool.Put(bufWrapper)

	return result
}

// emitTextDeltaTo writes text delta SSE to builder (zero-alloc for builder).
func emitTextDeltaTo(result *strings.Builder, text string, state *ClaudeStreamState) {
	idx := 0
	if state != nil {
		state.HasTextContent = true // Mark that we have text content
		// Switch block if needed
		if state.TextBlockStarted && state.CurrentBlockType != ir.ClaudeBlockText {
			result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{
				"type": ir.ClaudeSSEContentBlockStop, "index": state.TextBlockIndex,
			}))
			state.TextBlockStarted = false
			state.TextBlockIndex++
		}

		idx = state.TextBlockIndex
		if !state.TextBlockStarted {
			state.TextBlockStarted = true
			state.CurrentBlockType = ir.ClaudeBlockText
			result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{
				"type": ir.ClaudeSSEContentBlockStart, "index": idx,
				"content_block": map[string]any{"type": ir.ClaudeBlockText, "text": ""},
			}))
		}
	}
	result.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{
		"type": ir.ClaudeSSEContentBlockDelta, "index": idx,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}))
}

// emitThinkingDeltaTo writes thinking delta SSE to builder.
// If signature is non-empty, also emits signature_delta event.
func emitThinkingDeltaTo(result *strings.Builder, thinking string, signature []byte, state *ClaudeStreamState) {
	idx := 0
	if state != nil {
		// Switch block if needed
		if state.TextBlockStarted && state.CurrentBlockType != ir.ClaudeBlockThinking {
			result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{
				"type": ir.ClaudeSSEContentBlockStop, "index": state.TextBlockIndex,
			}))
			state.TextBlockStarted = false
			state.TextBlockIndex++
		}

		idx = state.TextBlockIndex
		if !state.TextBlockStarted {
			state.TextBlockStarted = true
			state.CurrentBlockType = ir.ClaudeBlockThinking
			result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{
				"type": ir.ClaudeSSEContentBlockStart, "index": idx,
				"content_block": map[string]any{"type": ir.ClaudeBlockThinking, "thinking": ""},
			}))
		}
	}

	// Emit thinking_delta if we have thinking content
	if thinking != "" {
		result.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{
			"type": ir.ClaudeSSEContentBlockDelta, "index": idx,
			"delta": map[string]any{"type": "thinking_delta", "thinking": thinking},
		}))
	}

	// Emit signature_delta if we have a signature (SDK expects this as separate event)
	if len(signature) > 0 {
		result.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{
			"type": ir.ClaudeSSEContentBlockDelta, "index": idx,
			"delta": map[string]any{"type": "signature_delta", "signature": string(signature)},
		}))
	}
}

// emitToolCallTo writes tool call SSE to builder.
func emitToolCallTo(result *strings.Builder, tc *ir.ToolCall, state *ClaudeStreamState) {
	// If tool call has signature and we have a thinking block open, emit signature_delta first
	// This handles Gemini where signature arrives with functionCall, not with thinking text
	if state != nil && state.TextBlockStarted && state.CurrentBlockType == ir.ClaudeBlockThinking && len(tc.ThoughtSignature) > 0 {
		result.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{
			"type": ir.ClaudeSSEContentBlockDelta, "index": state.TextBlockIndex,
			"delta": map[string]any{"type": "signature_delta", "signature": string(tc.ThoughtSignature)},
		}))
	}

	// Close any open content block (text or thinking)
	if state != nil && state.TextBlockStarted {
		result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{
			"type": ir.ClaudeSSEContentBlockStop, "index": state.TextBlockIndex,
		}))
		state.TextBlockStarted = false
		state.TextBlockIndex++
		state.CurrentBlockType = ""
	}

	idx := 0
	if state != nil {
		state.HasToolCalls = true
		idx = state.TextBlockIndex
		state.TextBlockIndex++
	}

	result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{
		"type": ir.ClaudeSSEContentBlockStart, "index": idx,
		"content_block": map[string]any{"type": ir.ClaudeBlockToolUse, "id": toClaudeToolID(tc.ID), "name": tc.Name, "input": map[string]any{}},
	}))

	args := tc.Args
	if args == "" {
		args = "{}"
	}
	result.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{
		"type": ir.ClaudeSSEContentBlockDelta, "index": idx,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": args},
	}))
	result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": idx}))
}

// emitFinishTo writes finish SSE to builder.
func emitFinishTo(result *strings.Builder, usage *ir.Usage, state *ClaudeStreamState) {
	// Client requirement: If we only emitted thinking (no text, no tool calls), inject empty text block
	if state != nil && !state.HasTextContent && !state.HasToolCalls {
		// Close thinking block if open
		if state.TextBlockStarted {
			result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{
				"type": ir.ClaudeSSEContentBlockStop, "index": state.TextBlockIndex,
			}))
			state.TextBlockStarted = false
			state.TextBlockIndex++
		}
		// Emit text block with space (some clients reject truly empty text)
		idx := state.TextBlockIndex
		result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{
			"type": ir.ClaudeSSEContentBlockStart, "index": idx,
			"content_block": map[string]any{"type": ir.ClaudeBlockText, "text": ""},
		}))
		result.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{
			"type": ir.ClaudeSSEContentBlockDelta, "index": idx,
			"delta": map[string]any{"type": "text_delta", "text": " "},
		}))
		result.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{
			"type": ir.ClaudeSSEContentBlockStop, "index": idx,
		}))
	}

	stopReason := ir.ClaudeStopEndTurn
	if state != nil && state.HasToolCalls {
		stopReason = ir.ClaudeStopToolUse
	}
	delta := map[string]any{"type": ir.ClaudeSSEMessageDelta, "delta": map[string]any{"stop_reason": stopReason}}
	if usage != nil {
		// Claude spec: output_tokens includes both regular output and thinking/reasoning tokens
		outputTokens := usage.CompletionTokens + int64(usage.ThoughtsTokenCount)
		usageMap := map[string]any{"input_tokens": usage.PromptTokens, "output_tokens": outputTokens}
		if usage.CacheCreationInputTokens > 0 {
			usageMap["cache_creation_input_tokens"] = usage.CacheCreationInputTokens
		}
		// Check CacheReadInputTokens first, then fall back to PromptTokensDetails.CachedTokens
		if usage.CacheReadInputTokens > 0 {
			usageMap["cache_read_input_tokens"] = usage.CacheReadInputTokens
		} else if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
			usageMap["cache_read_input_tokens"] = usage.PromptTokensDetails.CachedTokens
		}
		delta["usage"] = usageMap
	}
	result.WriteString(formatSSE(ir.ClaudeSSEMessageDelta, delta))
	result.WriteString(formatSSE(ir.ClaudeSSEMessageStop, map[string]any{"type": ir.ClaudeSSEMessageStop}))
}

func errMsg(err error) string {
	if err != nil {
		return err.Error()
	}
	return "Unknown error"
}

func copyMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		if nested, ok := v.(map[string]any); ok {
			result[k] = copyMap(nested)
		} else if arr, ok := v.([]any); ok {
			newArr := make([]any, len(arr))
			for i, item := range arr {
				if nestedMap, ok := item.(map[string]any); ok {
					newArr[i] = copyMap(nestedMap)
				} else {
					newArr[i] = item
				}
			}
			result[k] = newArr
		} else {
			result[k] = v
		}
	}
	return result
}

// isAssistantWithToolUse checks if the assistant message contains tool calls.
func isAssistantWithToolUse(msg ir.Message) bool {
	return len(msg.ToolCalls) > 0
}
