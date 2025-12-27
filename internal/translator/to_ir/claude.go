package to_ir

import (
	"bytes"
	"strings"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/gjson"
)

func ParseClaudeRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	// URL format fix: remove "format":"uri" which causes issues with some backends
	rawJSON = bytes.Replace(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`), -1)

	parsed, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, err
	}

	req := &ir.UnifiedChatRequest{}

	req.Model = parsed.Get("model").String()

	// Generation Parameters
	if v := parsed.Get("max_tokens"); v.Exists() {
		i := int(v.Int())
		req.MaxTokens = &i
	}
	if v := parsed.Get("temperature"); v.Exists() {
		f := v.Float()
		req.Temperature = &f
	}
	if v := parsed.Get("top_p"); v.Exists() {
		f := v.Float()
		req.TopP = &f
	}
	if v := parsed.Get("top_k"); v.Exists() {
		i := int(v.Int())
		req.TopK = &i
	}
	if v := parsed.Get("stop_sequences"); v.Exists() && v.IsArray() {
		for _, s := range v.Array() {
			req.StopSequences = append(req.StopSequences, s.String())
		}
	}

	// System message
	if system := parsed.Get("system"); system.Exists() {
		var systemText string
		if system.Type == gjson.String {
			systemText = system.String()
		} else if system.IsArray() {
			var parts []string
			for _, part := range system.Array() {
				if part.Get("type").String() == "text" {
					parts = append(parts, part.Get("text").String())
				}
			}
			systemText = strings.Join(parts, "\n")
		}
		if systemText != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role: ir.RoleSystem, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: systemText}},
			})
		}
	}

	// Messages
	if messages := parsed.Get("messages"); messages.Exists() && messages.IsArray() {
		for _, m := range messages.Array() {
			req.Messages = append(req.Messages, parseClaudeMessage(m))
		}
	}

	// Tools
	if tools := parsed.Get("tools"); tools.Exists() && tools.IsArray() {
		for _, t := range tools.Array() {
			toolType := t.Get("type").String()
			toolName := t.Get("name").String()

			// Handle Claude built-in tools (web_search, computer, etc.)
			// Official format: {"type": "web_search_20250305", "name": "web_search", "max_uses": 5}
			// IMPORTANT: Only treat as Claude built-in if it has the versioned type AND no input_schema.
			if strings.HasPrefix(toolType, "web_search_") && !t.Get("input_schema").Exists() {
				if req.Metadata == nil {
					req.Metadata = make(map[string]any)
				}
				// Map to google_search for Gemini backend, preserve original type
				wsConfig := map[string]any{"_original_type": toolType}
				if maxUses := t.Get("max_uses"); maxUses.Exists() {
					wsConfig["max_uses"] = int(maxUses.Int())
				}
				req.Metadata[ir.MetaGoogleSearch] = wsConfig
				continue
			}

			// Claude computer use tool: {"type": "computer_20241022", "name": "computer", ...}
			if strings.HasPrefix(toolType, "computer_") && !t.Get("input_schema").Exists() {
				if req.Metadata == nil {
					req.Metadata = make(map[string]any)
				}
				// Store full tool config for passthrough, preserve original type
				toolConfig := map[string]any{"_original_type": toolType}
				if dw := t.Get("display_width_px"); dw.Exists() {
					toolConfig["display_width_px"] = int(dw.Int())
				}
				if dh := t.Get("display_height_px"); dh.Exists() {
					toolConfig["display_height_px"] = int(dh.Int())
				}
				if dn := t.Get("display_number"); dn.Exists() {
					toolConfig["display_number"] = int(dn.Int())
				}
				req.Metadata[ir.MetaClaudeComputer] = toolConfig
				continue
			}

			// Claude bash tool: {"type": "bash_20241022", "name": "bash"}
			if strings.HasPrefix(toolType, "bash_") && !t.Get("input_schema").Exists() {
				if req.Metadata == nil {
					req.Metadata = make(map[string]any)
				}
				req.Metadata[ir.MetaClaudeBash] = map[string]any{"_original_type": toolType}
				continue
			}

			// Claude text editor tool: {"type": "text_editor_20241022", "name": "str_replace_editor"}
			if strings.HasPrefix(toolType, "text_editor_") && !t.Get("input_schema").Exists() {
				if req.Metadata == nil {
					req.Metadata = make(map[string]any)
				}
				req.Metadata[ir.MetaClaudeTextEditor] = map[string]any{"_original_type": toolType}
				continue
			}

			// Regular function tool
			var params map[string]any
			if schema := t.Get("input_schema"); schema.Exists() && schema.IsObject() {
				if err := json.Unmarshal([]byte(schema.Raw), &params); err == nil {
					params = ir.CleanJsonSchema(params)
				}
			}
			if params == nil {
				params = make(map[string]any)
			}
			req.Tools = append(req.Tools, ir.ToolDefinition{
				Name: toolName, Description: t.Get("description").String(), Parameters: params,
			})
		}
	}

	// MCP Servers
	if mcpServers := parsed.Get("mcp_servers"); mcpServers.Exists() && mcpServers.IsArray() {
		for _, srv := range mcpServers.Array() {
			mcpServer := ir.MCPServer{
				Type: srv.Get("type").String(),
				URL:  srv.Get("url").String(),
				Name: srv.Get("name").String(),
			}
			if token := srv.Get("authorization_token"); token.Exists() {
				mcpServer.AuthorizationToken = token.String()
			}
			if toolConfig := srv.Get("tool_configuration"); toolConfig.Exists() && toolConfig.IsObject() {
				var cfg map[string]any
				if err := json.Unmarshal([]byte(toolConfig.Raw), &cfg); err == nil {
					mcpServer.ToolConfiguration = cfg
				}
			}
			req.MCPServers = append(req.MCPServers, mcpServer)
		}
	}

	// Thinking/Reasoning config
	if thinking := parsed.Get("thinking"); thinking.Exists() && thinking.IsObject() {
		if thinking.Get("type").String() == "enabled" {
			req.Thinking = &ir.ThinkingConfig{IncludeThoughts: true}
			if budget := thinking.Get("budget_tokens"); budget.Exists() {
				b := int32(budget.Int())
				req.Thinking.ThinkingBudget = &b
			} else if budget := thinking.Get("budgetTokens"); budget.Exists() {
				b := int32(budget.Int())
				req.Thinking.ThinkingBudget = &b
			}
		} else if thinking.Get("type").String() == "disabled" {
			zero := int32(0)
			req.Thinking = &ir.ThinkingConfig{IncludeThoughts: false, ThinkingBudget: &zero}
		}
		// Note: nil Thinking means auto/default behavior
	}

	// Metadata
	if metadata := parsed.Get("metadata"); metadata.Exists() && metadata.IsObject() {
		var meta map[string]any
		if err := json.Unmarshal([]byte(metadata.Raw), &meta); err == nil {
			req.Metadata = meta
		}
	}

	return req, nil
}

func parseClaudeMessage(m gjson.Result) ir.Message {
	roleStr := m.Get("role").String()
	role := ir.RoleUser
	if roleStr == "assistant" {
		role = ir.RoleAssistant
	}

	msg := ir.Message{Role: role}
	content := m.Get("content")

	// Parse cache_control if present
	if cc := m.Get("cache_control"); cc.Exists() && cc.IsObject() {
		msg.CacheControl = &ir.CacheControl{
			Type: cc.Get("type").String(),
		}
		if ttl := cc.Get("ttl"); ttl.Exists() {
			ttlVal := ttl.Int()
			msg.CacheControl.TTL = &ttlVal
		}
	}

	if content.Type == gjson.String {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content.String()})
		return msg
	}

	if content.IsArray() {
		for _, block := range content.Array() {
			switch block.Get("type").String() {
			case "text":
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: block.Get("text").String()})
			case "thinking":
				// Claude Extended Thinking: Parse thinking text and signature
				var sig []byte
				if s := block.Get("signature").String(); s != "" {
					sig = []byte(s)
				}
				msg.Content = append(msg.Content, ir.ContentPart{
					Type:             ir.ContentTypeReasoning,
					Reasoning:        block.Get("thinking").String(),
					ThoughtSignature: sig,
				})
			case "reasoning":
				// OpenCode SDK / Vercel AI SDK "reasoning" block

				var sig []byte
				if s := block.Get("signature").String(); s != "" {
					sig = []byte(s)
				}
				msg.Content = append(msg.Content, ir.ContentPart{
					Type:             ir.ContentTypeReasoning,
					Reasoning:        block.Get("text").String(),
					ThoughtSignature: sig,
				})
			case "image":
				// Claude image block supports base64, url, and file sources
				if source := block.Get("source"); source.Exists() {
					sourceType := source.Get("type").String()
					switch sourceType {
					case "base64":
						msg.Content = append(msg.Content, ir.ContentPart{
							Type:  ir.ContentTypeImage,
							Image: &ir.ImagePart{MimeType: source.Get("media_type").String(), Data: source.Get("data").String()},
						})
					case "url":
						msg.Content = append(msg.Content, ir.ContentPart{
							Type:  ir.ContentTypeImage,
							Image: &ir.ImagePart{URL: source.Get("url").String()},
						})
					case "file":
						// Claude Files API reference
						msg.Content = append(msg.Content, ir.ContentPart{
							Type:  ir.ContentTypeImage,
							Image: &ir.ImagePart{FileID: source.Get("file_id").String()},
						})
					}
				}
			case "document":
				// Claude document block (PDF, etc.)
				// Supports: base64, url, file (file ID)
				if source := block.Get("source"); source.Exists() {
					sourceType := source.Get("type").String()
					fp := &ir.FilePart{
						Filename: block.Get("title").String(),
						MimeType: source.Get("media_type").String(),
					}
					switch sourceType {
					case "base64":
						fp.FileData = source.Get("data").String()
					case "url":
						fp.FileURL = source.Get("url").String()
					case "file":
						// Claude file ID reference
						fp.FileID = source.Get("file_id").String()
					}
					if fp.FileData != "" || fp.FileURL != "" || fp.FileID != "" {
						msg.Content = append(msg.Content, ir.ContentPart{
							Type: ir.ContentTypeFile,
							File: fp,
						})
					}
				}
			case "tool_use":
				toolID := block.Get("id").String()
				inputRaw := block.Get("input").Raw
				if inputRaw == "" {
					inputRaw = "{}"
				}
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
					ID: toolID, Name: block.Get("name").String(), Args: inputRaw,
				})
			case "tool_result":
				resultContent := block.Get("content")
				toolResultID := block.Get("tool_use_id").String()

				toolResult := &ir.ToolResultPart{
					ToolCallID: toolResultID,
					IsError:    block.Get("is_error").Bool(),
				}

				if resultContent.Type == gjson.String {
					toolResult.Result = resultContent.String()
				} else if resultContent.IsArray() {
					var textParts []string
					for _, part := range resultContent.Array() {
						partType := part.Get("type").String()
						switch partType {
						case "text":
							textParts = append(textParts, part.Get("text").String())
						case "image":
							if source := part.Get("source"); source.Exists() {
								sourceType := source.Get("type").String()
								img := &ir.ImagePart{}
								switch sourceType {
								case "base64":
									img.MimeType = source.Get("media_type").String()
									img.Data = source.Get("data").String()
								case "url":
									img.URL = source.Get("url").String()
								case "file":
									img.FileID = source.Get("file_id").String()
								}
								if img.Data != "" || img.URL != "" || img.FileID != "" {
									toolResult.Images = append(toolResult.Images, img)
								}
							}
						case "document":
							if source := part.Get("source"); source.Exists() {
								sourceType := source.Get("type").String()
								file := &ir.FilePart{
									Filename: part.Get("title").String(),
									MimeType: source.Get("media_type").String(),
								}
								switch sourceType {
								case "base64":
									file.FileData = source.Get("data").String()
								case "url":
									file.FileURL = source.Get("url").String()
								case "file":
									file.FileID = source.Get("file_id").String()
								}
								if file.FileData != "" || file.FileURL != "" || file.FileID != "" {
									toolResult.Files = append(toolResult.Files, file)
								}
							}
						}
					}
					toolResult.Result = strings.Join(textParts, "\n")
				} else {
					toolResult.Result = resultContent.Raw
				}

				msg.Content = append(msg.Content, ir.ContentPart{
					Type:       ir.ContentTypeToolResult,
					ToolResult: toolResult,
				})
			case "mcp_tool_use":
				// MCP tool use from Claude - treat like regular tool call with server info in metadata
				toolID := block.Get("id").String()
				inputRaw := block.Get("input").Raw
				if inputRaw == "" {
					inputRaw = "{}"
				}
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
					ID:   toolID,
					Name: block.Get("name").String(),
					Args: inputRaw,
				})
				// Note: server_name is tracked implicitly - when rebuilding for Claude,
				// we detect MCP tools by the mcptoolu_ prefix in the ID
			case "mcp_tool_result":
				// MCP tool result - treat like regular tool result
				resultContent := block.Get("content")
				var resultStr string
				if resultContent.Type == gjson.String {
					resultStr = resultContent.String()
				} else if resultContent.IsArray() {
					var parts []string
					for _, part := range resultContent.Array() {
						if part.Get("type").String() == "text" {
							parts = append(parts, part.Get("text").String())
						}
					}
					resultStr = strings.Join(parts, "\n")
				} else {
					resultStr = resultContent.Raw
				}
				msg.Content = append(msg.Content, ir.ContentPart{
					Type:       ir.ContentTypeToolResult,
					ToolResult: &ir.ToolResultPart{ToolCallID: block.Get("tool_use_id").String(), Result: resultStr},
				})
			}
		}
	}

	return msg
}

func ParseClaudeResponse(rawJSON []byte) ([]ir.Message, *ir.Usage, error) {
	parsed, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, nil, err
	}

	var usage *ir.Usage
	if u := parsed.Get("usage"); u.Exists() {
		usage = ir.ParseClaudeUsage(u)
	}

	content := parsed.Get("content")
	if !content.Exists() || !content.IsArray() {
		return nil, usage, nil
	}

	msg := ir.Message{Role: ir.RoleAssistant}

	for _, block := range content.Array() {
		ir.ParseClaudeContentBlock(block, &msg)
	}

	if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 {
		return []ir.Message{msg}, usage, nil
	}
	return nil, usage, nil
}

func ParseClaudeChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	data := ir.ExtractSSEData(rawJSON)
	if len(data) == 0 {
		return nil, nil
	}
	parsed, err := ir.ParseAndValidateJSON(data)
	if err != nil {
		return nil, nil // Ignore invalid chunks in streaming
	}

	switch parsed.Get("type").String() {
	case "content_block_delta":
		return ir.ParseClaudeStreamDelta(parsed), nil
	case "message_delta":
		return ir.ParseClaudeMessageDelta(parsed), nil
	case "message_stop":
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}}, nil
	case "error":
		return []ir.UnifiedEvent{{Type: ir.EventTypeError, Error: &ClaudeAPIError{Message: parsed.Get("error.message").String()}}}, nil
	}
	return nil, nil
}

// ClaudeAPIError represents an error from Claude API
type ClaudeAPIError struct {
	Message string
}

func (e *ClaudeAPIError) Error() string {
	return e.Message
}
