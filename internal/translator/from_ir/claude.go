package from_ir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

type ClaudeProvider struct{}

type ClaudeStreamState struct {
	MessageID        string
	Model            string
	MessageStartSent bool
	TextBlockStarted bool
	CurrentBlockType string
	TextBlockIndex   int
	HasToolCalls     bool
	HasTextContent   bool
	FinishSent       bool
	ParserState      *ir.ClaudeStreamParserState
}

func NewClaudeStreamState() *ClaudeStreamState {
	return &ClaudeStreamState{TextBlockIndex: 0, ParserState: ir.NewClaudeStreamParserState()}
}

func (p *ClaudeProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	u1, _ := uuid.NewRandom()
	u2, _ := uuid.NewRandom()
	sum := sha256.Sum256([]byte(u1.String() + u2.String()))
	userID := fmt.Sprintf("user_%s_account_%s_session_%s", hex.EncodeToString(sum[:]), u1.String(), u2.String())

	root := map[string]any{"model": req.Model, "max_tokens": ir.ClaudeDefaultMaxTokens, "metadata": map[string]any{"user_id": userID}, "messages": []any{}}
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

	thinkingEnabled := false
	if req.Thinking != nil {
		b := int32(0)
		if req.Thinking.ThinkingBudget != nil {
			b = *req.Thinking.ThinkingBudget
		}
		if req.Thinking.IncludeThoughts && b != 0 {
			root["thinking"] = map[string]any{"type": "enabled", "budget_tokens": b}
			thinkingEnabled = true
		} else if b == 0 && !req.Thinking.IncludeThoughts {
			root["thinking"] = map[string]any{"type": "disabled"}
		}
	}

	var msgs []any
	for _, m := range req.Messages {
		switch m.Role {
		case ir.RoleSystem:
			if text := ir.CombineTextParts(m); text != "" {
				root["system"] = text
			}
		case ir.RoleUser:
			if ps := ir.BuildClaudeContentParts(m, false, false); len(ps) > 0 {
				obj := map[string]any{"role": ir.ClaudeRoleUser, "content": ps}
				if m.CacheControl != nil {
					cc := map[string]any{"type": m.CacheControl.Type}
					if m.CacheControl.TTL != nil {
						cc["ttl"] = *m.CacheControl.TTL
					}
					obj["cache_control"] = cc
				}
				msgs = append(msgs, obj)
			}
		case ir.RoleAssistant:
			if ps := ir.BuildClaudeContentParts(m, len(m.ToolCalls) > 0, thinkingEnabled); len(ps) > 0 {
				obj := map[string]any{"role": ir.ClaudeRoleAssistant, "content": ps}
				if m.CacheControl != nil {
					cc := map[string]any{"type": m.CacheControl.Type}
					if m.CacheControl.TTL != nil {
						cc["ttl"] = *m.CacheControl.TTL
					}
					obj["cache_control"] = cc
				}
				msgs = append(msgs, obj)
			}
		case ir.RoleTool:
			for _, p := range m.Content {
				if p.Type == ir.ContentTypeToolResult && p.ToolResult != nil {
					tr := map[string]any{"type": ir.ClaudeBlockToolResult, "tool_use_id": p.ToolResult.ToolCallID}
					if p.ToolResult.IsError {
						tr["is_error"] = true
					}
					if len(p.ToolResult.Images) > 0 || len(p.ToolResult.Files) > 0 {
						var c []any
						if p.ToolResult.Result != "" {
							c = append(c, map[string]any{"type": "text", "text": p.ToolResult.Result})
						}
						for _, img := range p.ToolResult.Images {
							s := map[string]any{}
							if img.Data != "" {
								s = map[string]any{"type": "base64", "media_type": img.MimeType, "data": img.Data}
							} else if img.URL != "" {
								s = map[string]any{"type": "url", "url": img.URL}
							} else if img.FileID != "" {
								s = map[string]any{"type": "file", "file_id": img.FileID}
							}
							if len(s) > 0 {
								c = append(c, map[string]any{"type": ir.ClaudeBlockImage, "source": s})
							}
						}
						for _, f := range p.ToolResult.Files {
							s := map[string]any{}
							if f.FileData != "" {
								s = map[string]any{"type": "base64", "data": f.FileData, "media_type": f.MimeType}
							} else if f.FileURL != "" {
								s = map[string]any{"type": "url", "url": f.FileURL}
							} else if f.FileID != "" {
								s = map[string]any{"type": "file", "file_id": f.FileID}
							}
							if len(s) > 0 {
								c = append(c, map[string]any{"type": ir.ClaudeBlockDocument, "title": f.Filename, "source": s})
							}
						}
						tr["content"] = c
					} else {
						tr["content"] = p.ToolResult.Result
					}
					msgs = append(msgs, map[string]any{"role": ir.ClaudeRoleUser, "content": []any{tr}})
				}
			}
		}
	}
	root["messages"] = msgs

	var tools []any
	for _, t := range req.Tools {
		ps := ir.CleanJsonSchemaForClaude(ir.CopyMap(t.Parameters))
		if ps == nil {
			ps = map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false, "$schema": ir.JSONSchemaDraft202012}
		}
		tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "input_schema": ps})
	}

	if req.Metadata != nil {
		for k, mKey := range map[string]string{ir.MetaGoogleSearch: "web_search", ir.MetaClaudeComputer: "computer", ir.MetaClaudeBash: "bash", ir.MetaClaudeTextEditor: "str_replace_editor"} {
			if v, ok := req.Metadata[k]; ok {
				t := map[string]any{"name": mKey}
				if cfg, ok := v.(map[string]any); ok {
					if ot, _ := cfg["_original_type"].(string); ot != "" {
						t["type"] = ot
					} else {
						t["type"] = mKey + "_20241022"
					}
					for mk, mv := range cfg {
						if mk != "_original_type" {
							t[mk] = mv
						}
					}
				} else {
					t["type"] = mKey + "_20241022"
				}
				tools = append(tools, t)
			}
		}
	}

	if req.ToolChoice == "none" {
		tools = nil
	}

	if len(tools) > 0 {
		root["tools"] = tools
		tc := map[string]any{}
		switch req.ToolChoice {
		case "function":
			tc = map[string]any{"type": "tool", "name": req.ToolChoiceFunction}
		case "required", "any":
			tc = map[string]any{"type": "any"}
		case "auto":
			tc = map[string]any{"type": "auto"}
		}
		if len(tc) > 0 {
			if req.ParallelToolCalls != nil && !*req.ParallelToolCalls {
				tc["disable_parallel_tool_use"] = true
			}
			root["tool_choice"] = tc
		}
	}

	if len(req.MCPServers) > 0 {
		var srvs []any
		for _, s := range req.MCPServers {
			srv := map[string]any{"type": s.Type, "url": s.URL, "name": s.Name}
			if s.AuthorizationToken != "" {
				srv["authorization_token"] = s.AuthorizationToken
			}
			if s.ToolConfiguration != nil {
				srv["tool_configuration"] = s.ToolConfiguration
			}
			srvs = append(srvs, srv)
		}
		root["mcp_servers"] = srvs
	}

	if len(req.Metadata) > 0 {
		m := root["metadata"].(map[string]any)
		for k, v := range req.Metadata {
			if k != ir.MetaGoogleSearch && k != ir.MetaClaudeComputer && k != ir.MetaClaudeBash && k != ir.MetaClaudeTextEditor {
				m[k] = v
			}
		}
	}

	return json.Marshal(root)
}

func (p *ClaudeProvider) ParseResponse(rj []byte) ([]ir.Message, *ir.Usage, error) {
	root, err := ir.ParseAndValidateJSON(rj)
	if err != nil {
		return nil, nil, err
	}
	msg := ir.Message{Role: ir.RoleAssistant}
	for _, b := range root.Get("content").Array() {
		ir.ParseClaudeContentBlock(b, &msg)
	}
	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil, ir.ParseClaudeUsage(root.Get("usage")), nil
	}
	return []ir.Message{msg}, ir.ParseClaudeUsage(root.Get("usage")), nil
}

func (p *ClaudeProvider) ParseStreamChunk(cj []byte) ([]ir.UnifiedEvent, error) {
	return p.ParseStreamChunkWithState(cj, nil)
}

func (p *ClaudeProvider) ParseStreamChunkWithState(cj []byte, state *ir.ClaudeStreamParserState) ([]ir.UnifiedEvent, error) {
	d := ir.ExtractSSEData(cj)
	if len(d) == 0 {
		return nil, nil
	}
	pjd, _ := ir.ParseAndValidateJSON(d)
	switch pjd.Get("type").String() {
	case ir.ClaudeSSEContentBlockStart:
		return ir.ParseClaudeContentBlockStart(pjd, state), nil
	case ir.ClaudeSSEContentBlockDelta:
		if state != nil {
			return ir.ParseClaudeStreamDeltaWithState(pjd, state), nil
		}
		return ir.ParseClaudeStreamDelta(pjd), nil
	case ir.ClaudeSSEContentBlockStop:
		return ir.ParseClaudeContentBlockStop(pjd, state), nil
	case ir.ClaudeSSEMessageDelta:
		return ir.ParseClaudeMessageDelta(pjd), nil
	case ir.ClaudeSSEMessageStop:
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}}, nil
	case ir.ClaudeSSEError:
		return []ir.UnifiedEvent{{Type: ir.EventTypeError, Error: fmt.Errorf("%s", pjd.Get("error.message").String())}}, nil
	}
	return nil, nil
}

func ToClaudeSSE(ev ir.UnifiedEvent, state *ClaudeStreamState) ([]byte, error) {
	res := ir.GetStringBuilder()
	defer ir.PutStringBuilder(res)
	switch ev.Type {
	case ir.EventTypeStreamMeta:
		if state != nil && !state.MessageStartSent && ev.StreamMeta != nil {
			state.MessageStartSent = true
			state.Model = ev.StreamMeta.Model
			state.MessageID = ev.StreamMeta.MessageID
			res.WriteString(formatSSE(ir.ClaudeSSEMessageStart, map[string]any{
				"type": ir.ClaudeSSEMessageStart,
				"message": map[string]any{
					"id":      ev.StreamMeta.MessageID,
					"type":    "message",
					"role":    ir.ClaudeRoleAssistant,
					"content": []any{},
					"model":   ev.StreamMeta.Model,
					"usage": map[string]any{
						"input_tokens":                ev.StreamMeta.EstimatedInputTokens,
						"output_tokens":               int64(1),
						"cache_creation_input_tokens": int64(0),
						"cache_read_input_tokens":     int64(0),
					},
				},
			}))
		}
	case ir.EventTypeToken:
		emitTextDeltaTo(res, ev.Content, state)
	case ir.EventTypeReasoning:
		if ev.RedactedData != "" {
			emitRedactedThinkingDeltaTo(res, ev.RedactedData, state)
		} else {
			emitThinkingDeltaTo(res, ev.Reasoning, ev.ThoughtSignature, state)
		}
	case ir.EventTypeToolCall:
		if ev.ToolCall != nil {
			emitToolCallTo(res, ev.ToolCall, state)
		}
	case ir.EventTypeFinish:
		if state != nil && !state.FinishSent {
			state.FinishSent = true
			emitFinishTo(res, ev.Usage, state)
		} else if state == nil {
			emitFinishTo(res, ev.Usage, nil)
		}
	case ir.EventTypeError:
		res.WriteString(formatSSE(ir.ClaudeSSEError, map[string]any{"type": ir.ClaudeSSEError, "error": map[string]any{"type": "api_error", "message": ev.Error.Error()}}))
	}
	if res.Len() == 0 {
		return nil, nil
	}
	return []byte(res.String()), nil
}

func ToClaudeResponse(ms []ir.Message, us *ir.Usage, model, mid string) ([]byte, error) {
	b := ir.NewResponseBuilder(ms, us, model, false)
	res := map[string]any{"id": mid, "type": "message", "role": ir.ClaudeRoleAssistant, "content": b.BuildClaudeContentParts(), "model": model, "stop_reason": ir.ClaudeStopEndTurn}
	if b.HasToolCalls() {
		res["stop_reason"] = ir.ClaudeStopToolUse
	}
	if us != nil {
		um := map[string]any{"input_tokens": us.PromptTokens, "output_tokens": us.CompletionTokens}
		if us.CacheCreationInputTokens > 0 {
			um["cache_creation_input_tokens"] = us.CacheCreationInputTokens
		}
		if us.CacheReadInputTokens > 0 {
			um["cache_read_input_tokens"] = us.CacheReadInputTokens
		} else if us.PromptTokensDetails != nil && us.PromptTokensDetails.CachedTokens > 0 {
			um["cache_read_input_tokens"] = us.PromptTokensDetails.CachedTokens
		}
		res["usage"] = um
	}
	return json.Marshal(res)
}

var ssePool = sync.Pool{New: func() any { return &sseBuffer{data: make([]byte, 0, 512)} }}

func formatSSE(et string, d any) string {
	jb, _ := json.Marshal(d)
	bw := ssePool.Get().(*sseBuffer)
	b := bw.data[:0]
	if cap(b) < 16+len(et)+len(jb) {
		b = make([]byte, 0, 16+len(et)+len(jb))
	}
	b = append(append(append(append(append(b, "event: "...), et...), "\ndata: "...), jb...), "\n\n"...)
	res := string(b)
	bw.data = b[:0]
	ssePool.Put(bw)
	return res
}

func emitTextDeltaTo(res *strings.Builder, t string, s *ClaudeStreamState) {
	idx := 0
	if s != nil {
		s.HasTextContent = true
		if s.TextBlockStarted && s.CurrentBlockType != ir.ClaudeBlockText {
			res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": s.TextBlockIndex}))
			s.TextBlockStarted, s.TextBlockIndex = false, s.TextBlockIndex+1
		}
		idx = s.TextBlockIndex
		if !s.TextBlockStarted {
			s.TextBlockStarted, s.CurrentBlockType = true, ir.ClaudeBlockText
			res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{"type": ir.ClaudeSSEContentBlockStart, "index": idx, "content_block": map[string]any{"type": ir.ClaudeBlockText, "text": ""}}))
		}
	}
	res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": idx, "delta": map[string]any{"type": "text_delta", "text": t}}))
}

func emitThinkingDeltaTo(res *strings.Builder, t string, sig []byte, s *ClaudeStreamState) {
	if t == "" && len(sig) > 0 {
		if s != nil && s.TextBlockStarted && s.CurrentBlockType == ir.ClaudeBlockThinking {
			res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": s.TextBlockIndex, "delta": map[string]any{"type": "signature_delta", "signature": string(sig)}}))
		}
		return
	}
	idx := 0
	if s != nil {
		if s.TextBlockStarted && s.CurrentBlockType != ir.ClaudeBlockThinking {
			res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": s.TextBlockIndex}))
			s.TextBlockStarted, s.TextBlockIndex = false, s.TextBlockIndex+1
		}
		idx = s.TextBlockIndex
		if !s.TextBlockStarted {
			s.TextBlockStarted, s.CurrentBlockType = true, ir.ClaudeBlockThinking
			res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{"type": ir.ClaudeSSEContentBlockStart, "index": idx, "content_block": map[string]any{"type": ir.ClaudeBlockThinking, "thinking": ""}}))
		}
	}
	if t != "" {
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": idx, "delta": map[string]any{"type": "thinking_delta", "thinking": t}}))
	}
	if len(sig) > 0 {
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": idx, "delta": map[string]any{"type": "signature_delta", "signature": string(sig)}}))
	}
}

func emitRedactedThinkingDeltaTo(res *strings.Builder, d string, s *ClaudeStreamState) {
	idx := 0
	if s != nil {
		if s.TextBlockStarted && s.CurrentBlockType != ir.ClaudeBlockRedactedThinking {
			res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": s.TextBlockIndex}))
			s.TextBlockStarted, s.TextBlockIndex = false, s.TextBlockIndex+1
		}
		idx = s.TextBlockIndex
		if !s.TextBlockStarted {
			s.TextBlockStarted, s.CurrentBlockType = true, ir.ClaudeBlockRedactedThinking
			res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{"type": ir.ClaudeSSEContentBlockStart, "index": idx, "content_block": map[string]any{"type": ir.ClaudeBlockRedactedThinking, "data": ""}}))
		}
	}
	res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": idx, "delta": map[string]any{"type": ir.ClaudeDeltaRedactedThinking, "data": d}}))
}

func emitToolCallTo(res *strings.Builder, tc *ir.ToolCall, s *ClaudeStreamState) {
	if s != nil && s.TextBlockStarted && s.CurrentBlockType == ir.ClaudeBlockThinking && len(tc.ThoughtSignature) > 0 {
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": s.TextBlockIndex, "delta": map[string]any{"type": "signature_delta", "signature": string(tc.ThoughtSignature)}}))
	}
	if s != nil && s.TextBlockStarted {
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": s.TextBlockIndex}))
		s.TextBlockStarted, s.TextBlockIndex, s.CurrentBlockType = false, s.TextBlockIndex+1, ""
	}
	idx := 0
	if s != nil {
		s.HasToolCalls, idx = true, s.TextBlockIndex
		s.TextBlockIndex++
	}
	res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{"type": ir.ClaudeSSEContentBlockStart, "index": idx, "content_block": map[string]any{"type": ir.ClaudeBlockToolUse, "id": ir.ToClaudeToolID(tc.ID), "name": tc.Name, "input": map[string]any{}}}))
	args := tc.Args
	if args == "" {
		args = "{}"
	}
	res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": idx, "delta": map[string]any{"type": "input_json_delta", "partial_json": args}}))
	res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": idx}))
}

func emitFinishTo(res *strings.Builder, us *ir.Usage, s *ClaudeStreamState) {
	if s != nil && s.TextBlockStarted {
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": s.TextBlockIndex}))
		s.TextBlockStarted, s.TextBlockIndex, s.CurrentBlockType = false, s.TextBlockIndex+1, ""
	}
	if s != nil && !s.HasTextContent && !s.HasToolCalls {
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStart, map[string]any{"type": ir.ClaudeSSEContentBlockStart, "index": s.TextBlockIndex, "content_block": map[string]any{"type": ir.ClaudeBlockText, "text": ""}}))
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockDelta, map[string]any{"type": ir.ClaudeSSEContentBlockDelta, "index": s.TextBlockIndex, "delta": map[string]any{"type": "text_delta", "text": " "}}))
		res.WriteString(formatSSE(ir.ClaudeSSEContentBlockStop, map[string]any{"type": ir.ClaudeSSEContentBlockStop, "index": s.TextBlockIndex}))
	}
	sr := ir.ClaudeStopEndTurn
	if s != nil && s.HasToolCalls {
		sr = ir.ClaudeStopToolUse
	}
	um := map[string]any{"output_tokens": int64(0)}
	if us != nil {
		um["output_tokens"], um["input_tokens"] = us.CompletionTokens+int64(us.ThoughtsTokenCount), us.PromptTokens
		if us.CacheCreationInputTokens > 0 {
			um["cache_creation_input_tokens"] = us.CacheCreationInputTokens
		}
		if us.CacheReadInputTokens > 0 {
			um["cache_read_input_tokens"] = us.CacheReadInputTokens
		} else if us.PromptTokensDetails != nil && us.PromptTokensDetails.CachedTokens > 0 {
			um["cache_read_input_tokens"] = us.PromptTokensDetails.CachedTokens
		}
	}
	res.WriteString(formatSSE(ir.ClaudeSSEMessageDelta, map[string]any{"type": ir.ClaudeSSEMessageDelta, "delta": map[string]any{"stop_reason": sr}, "usage": um}))
	res.WriteString(formatSSE(ir.ClaudeSSEMessageStop, map[string]any{"type": ir.ClaudeSSEMessageStop}))
}

type sseBuffer struct{ data []byte }
