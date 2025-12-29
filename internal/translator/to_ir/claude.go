package to_ir

import (
	"bytes"
	"strings"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/gjson"
)

func ParseClaudeRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	rawJSON = bytes.ReplaceAll(rawJSON, []byte(`"url":{"type":"string","format":"uri",`), []byte(`"url":{"type":"string",`))

	parsed, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, err
	}

	req := &ir.UnifiedChatRequest{
		Model: parsed.Get("model").String(),
	}

	if v := parsed.Get("max_tokens"); v.Exists() {
		req.MaxTokens = ir.Ptr(int(v.Int()))
	}
	if v := parsed.Get("temperature"); v.Exists() {
		req.Temperature = ir.Ptr(v.Float())
	}
	if v := parsed.Get("top_p"); v.Exists() {
		req.TopP = ir.Ptr(v.Float())
	}
	if v := parsed.Get("top_k"); v.Exists() {
		req.TopK = ir.Ptr(int(v.Int()))
	}
	for _, s := range parsed.Get("stop_sequences").Array() {
		req.StopSequences = append(req.StopSequences, s.String())
	}

	if system := parsed.Get("system"); system.Exists() {
		var text string
		if system.Type == gjson.String {
			text = system.String()
		} else {
			var parts []string
			for _, p := range system.Array() {
				if p.Get("type").String() == "text" {
					parts = append(parts, p.Get("text").String())
				}
			}
			text = strings.Join(parts, "\n")
		}
		if text != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role:    ir.RoleSystem,
				Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: text}},
			})
		}
	}

	for _, m := range parsed.Get("messages").Array() {
		req.Messages = append(req.Messages, parseClaudeMessage(m))
	}

	req.Metadata = make(map[string]any)
	for _, t := range parsed.Get("tools").Array() {
		toolType := t.Get("type").String()
		toolName := t.Get("name").String()

		if !t.Get("input_schema").Exists() {
			if strings.HasPrefix(toolType, "web_search_") {
				wsConfig := map[string]any{"_original_type": toolType}
				if v := t.Get("max_uses"); v.Exists() {
					wsConfig["max_uses"] = int(v.Int())
				}
				req.Metadata[ir.MetaGoogleSearch] = wsConfig
				continue
			}
			if strings.HasPrefix(toolType, "computer_") {
				conf := map[string]any{"_original_type": toolType}
				if v := t.Get("display_width_px"); v.Exists() {
					conf["display_width_px"] = int(v.Int())
				}
				if v := t.Get("display_height_px"); v.Exists() {
					conf["display_height_px"] = int(v.Int())
				}
				if v := t.Get("display_number"); v.Exists() {
					conf["display_number"] = int(v.Int())
				}
				req.Metadata[ir.MetaClaudeComputer] = conf
				continue
			}
			if strings.HasPrefix(toolType, "bash_") {
				req.Metadata[ir.MetaClaudeBash] = map[string]any{"_original_type": toolType}
				continue
			}
			if strings.HasPrefix(toolType, "text_editor_") {
				req.Metadata[ir.MetaClaudeTextEditor] = map[string]any{"_original_type": toolType}
				continue
			}
		}

		var params map[string]any
		if schema := t.Get("input_schema"); schema.Exists() {
			if err := json.Unmarshal([]byte(schema.Raw), &params); err == nil {
				params = ir.CleanJsonSchema(params)
			}
		}
		if params == nil {
			params = make(map[string]any)
		}
		req.Tools = append(req.Tools, ir.ToolDefinition{
			Name:        toolName,
			Description: t.Get("description").String(),
			Parameters:  params,
		})
	}

	for _, srv := range parsed.Get("mcp_servers").Array() {
		mcp := ir.MCPServer{
			Type:               srv.Get("type").String(),
			URL:                srv.Get("url").String(),
			Name:               srv.Get("name").String(),
			AuthorizationToken: srv.Get("authorization_token").String(),
		}
		if cfg := srv.Get("tool_configuration"); cfg.IsObject() {
			json.Unmarshal([]byte(cfg.Raw), &mcp.ToolConfiguration)
		}
		req.MCPServers = append(req.MCPServers, mcp)
	}

	if thinking := parsed.Get("thinking"); thinking.IsObject() {
		if thinking.Get("type").String() == "enabled" {
			budget := int32(thinking.Get("budget_tokens").Int())
			if budget == 0 {
				budget = int32(thinking.Get("budgetTokens").Int())
			}
			req.Thinking = &ir.ThinkingConfig{IncludeThoughts: true, ThinkingBudget: &budget}
		} else if thinking.Get("type").String() == "disabled" {
			req.Thinking = &ir.ThinkingConfig{IncludeThoughts: false, ThinkingBudget: ir.Ptr(int32(0))}
		}
	}

	if tc := parsed.Get("tool_choice"); tc.Exists() {
		if tc.IsObject() {
			switch tc.Get("type").String() {
			case "auto":
				req.ToolChoice = "auto"
			case "any":
				req.ToolChoice = "required"
				if v := tc.Get("disable_parallel_tool_use"); v.Exists() {
					req.ParallelToolCalls = ir.Ptr(!v.Bool())
				}
			case "tool":
				req.ToolChoice = "function"
				req.ToolChoiceFunction = tc.Get("name").String()
				if v := tc.Get("disable_parallel_tool_use"); v.Exists() {
					req.ParallelToolCalls = ir.Ptr(!v.Bool())
				}
			case "none":
				req.ToolChoice = "none"
			}
		} else {
			switch s := tc.String(); s {
			case "any":
				req.ToolChoice = "required"
			default:
				req.ToolChoice = s
			}
		}
	}

	if meta := parsed.Get("metadata"); meta.IsObject() {
		var m map[string]any
		if err := json.Unmarshal([]byte(meta.Raw), &m); err == nil {
			for k, v := range m {
				req.Metadata[k] = v
			}
		}
	}

	return req, nil
}

func parseClaudeMessage(m gjson.Result) ir.Message {
	role := ir.RoleUser
	if m.Get("role").String() == "assistant" {
		role = ir.RoleAssistant
	}

	msg := ir.Message{Role: role}
	if cc := m.Get("cache_control"); cc.IsObject() {
		msg.CacheControl = &ir.CacheControl{Type: cc.Get("type").String()}
		if v := cc.Get("ttl"); v.Exists() {
			msg.CacheControl.TTL = ir.Ptr(v.Int())
		}
	}

	content := m.Get("content")
	if content.Type == gjson.String {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content.String()})
	} else {
		for _, block := range content.Array() {
			switch t := block.Get("type").String(); t {
			case "text":
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: block.Get("text").String()})
			case "thinking", "reasoning":
				textKey := "thinking"
				if t == "reasoning" {
					textKey = "text"
				}
				msg.Content = append(msg.Content, ir.ContentPart{
					Type:             ir.ContentTypeReasoning,
					Reasoning:        block.Get(textKey).String(),
					ThoughtSignature: []byte(block.Get("signature").String()),
				})
			case "redacted_thinking":
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeRedactedThinking, RedactedData: block.Get("data").String()})
			case "image":
				if src := block.Get("source"); src.Exists() {
					part := ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{}}
					switch src.Get("type").String() {
					case "base64":
						part.Image.MimeType = src.Get("media_type").String()
						part.Image.Data = src.Get("data").String()
					case "url":
						part.Image.URL = src.Get("url").String()
					case "file":
						part.Image.FileID = src.Get("file_id").String()
					}
					msg.Content = append(msg.Content, part)
				}
			case "document":
				if src := block.Get("source"); src.Exists() {
					fp := &ir.FilePart{Filename: block.Get("title").String(), MimeType: src.Get("media_type").String()}
					switch src.Get("type").String() {
					case "base64":
						fp.FileData = src.Get("data").String()
					case "url":
						fp.FileURL = src.Get("url").String()
					case "file":
						fp.FileID = src.Get("file_id").String()
					}
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeFile, File: fp})
				}
			case "tool_use", "mcp_tool_use", "server_tool_use":
				args := block.Get("input").Raw
				if args == "" {
					args = "{}"
				}
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{ID: block.Get("id").String(), Name: block.Get("name").String(), Args: args})
			case "tool_result", "mcp_tool_result", "web_search_tool_result":
				res := &ir.ToolResultPart{ToolCallID: block.Get("tool_use_id").String(), IsError: block.Get("is_error").Bool()}
				bc := block.Get("content")
				if bc.Type == gjson.String {
					res.Result = bc.String()
				} else if bc.IsArray() {
					var texts []string
					for _, p := range bc.Array() {
						switch p.Get("type").String() {
						case "text":
							texts = append(texts, p.Get("text").String())
						case "image":
							if s := p.Get("source"); s.Exists() {
								img := &ir.ImagePart{MimeType: s.Get("media_type").String(), Data: s.Get("data").String(), URL: s.Get("url").String(), FileID: s.Get("file_id").String()}
								res.Images = append(res.Images, img)
							}
						case "document":
							if s := p.Get("source"); s.Exists() {
								res.Files = append(res.Files, &ir.FilePart{Filename: p.Get("title").String(), MimeType: s.Get("media_type").String(), FileData: s.Get("data").String(), FileURL: s.Get("url").String(), FileID: s.Get("file_id").String()})
							}
						}
					}
					res.Result = strings.Join(texts, "\n")
				} else {
					res.Result = bc.Raw
				}
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeToolResult, ToolResult: res})
			}
		}
	}

	if msg.Role == ir.RoleUser && len(msg.Content) > 0 {
		onlyTools := true
		for _, p := range msg.Content {
			if p.Type != ir.ContentTypeToolResult {
				onlyTools = false
				break
			}
		}
		if onlyTools {
			msg.Role = ir.RoleTool
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
	if !content.IsArray() {
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
	return ParseClaudeChunkWithState(rawJSON, nil)
}

func ParseClaudeChunkWithState(rawJSON []byte, state *ir.ClaudeStreamParserState) ([]ir.UnifiedEvent, error) {
	data := ir.ExtractSSEData(rawJSON)
	if len(data) == 0 {
		return nil, nil
	}
	parsed, _ := ir.ParseAndValidateJSON(data)
	switch parsed.Get("type").String() {
	case "content_block_start":
		return ir.ParseClaudeContentBlockStart(parsed, state), nil
	case "content_block_delta":
		return ir.ParseClaudeStreamDeltaWithState(parsed, state), nil
	case "content_block_stop":
		return ir.ParseClaudeContentBlockStop(parsed, state), nil
	case "message_delta":
		return ir.ParseClaudeMessageDelta(parsed), nil
	case "message_stop":
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}}, nil
	case "error":
		return []ir.UnifiedEvent{{Type: ir.EventTypeError, Error: &ClaudeAPIError{Message: parsed.Get("error.message").String()}}}, nil
	}
	return nil, nil
}

type ClaudeAPIError struct{ Message string }

func (e *ClaudeAPIError) Error() string { return e.Message }
