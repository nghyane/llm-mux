package from_ir

import (
	"fmt"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

type OpenAIRequestFormat int

const (
	FormatChatCompletions OpenAIRequestFormat = iota
	FormatResponsesAPI
)

func ToOpenAIRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	return ToOpenAIRequestFmt(req, FormatChatCompletions)
}

func ToOpenAIRequestFmt(req *ir.UnifiedChatRequest, format OpenAIRequestFormat) ([]byte, error) {
	if format == FormatResponsesAPI {
		return convertToResponsesAPIRequest(req)
	}
	return convertToChatCompletionsRequest(req)
}

func convertToChatCompletionsRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]any{"model": req.Model, "messages": []any{}}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		m["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		m["max_tokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		m["stop"] = req.StopSequences
	}
	if req.Prediction != nil && req.Prediction.Content != "" {
		m["prediction"] = map[string]any{"type": req.Prediction.Type, "content": req.Prediction.Content}
	}
	if req.Thinking != nil && req.Thinking.IncludeThoughts {
		b := 0
		if req.Thinking.ThinkingBudget != nil {
			b = int(*req.Thinking.ThinkingBudget)
		}
		m["reasoning_effort"] = ir.BudgetToEffort(b, "auto")
	}

	var msgs []any
	for _, msg := range req.Messages {
		if msg.Role == ir.RoleTool {
			for _, p := range msg.Content {
				if p.Type == ir.ContentTypeToolResult && p.ToolResult != nil {
					msgs = append(msgs, map[string]any{"role": "tool", "tool_call_id": p.ToolResult.ToolCallID, "content": p.ToolResult.Result})
				}
			}
			continue
		}
		if obj := convertMessageToOpenAI(msg); obj != nil {
			msgs = append(msgs, obj)
		}
	}
	m["messages"] = msgs

	if req.ResponseSchema != nil {
		rf := map[string]any{"type": "json_schema", "json_schema": map[string]any{"schema": req.ResponseSchema}}
		if req.ResponseSchemaName != "" {
			rf["json_schema"].(map[string]any)["name"] = req.ResponseSchemaName
		}
		if req.ResponseSchemaStrict {
			rf["json_schema"].(map[string]any)["strict"] = true
		}
		m["response_format"] = rf
	}

	var tools []any
	for _, t := range req.Tools {
		ps := t.Parameters
		if ps == nil {
			ps = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": ps}})
	}

	if req.Metadata != nil {
		for k, mk := range map[string]string{ir.MetaGoogleSearch: "web_search_preview", ir.MetaCodeExecution: "code_interpreter", ir.MetaFileSearch: "file_search"} {
			if cfg, ok := req.Metadata[k]; ok {
				t := map[string]any{"type": mk}
				if m, ok := cfg.(map[string]any); ok {
					for ck, cv := range m {
						t[ck] = cv
					}
				}
				tools = append(tools, t)
			}
		}
	}

	if len(tools) > 0 {
		m["tools"] = tools
	}

	if req.ToolChoice == "function" && req.ToolChoiceFunction != "" {
		tc := map[string]any{"type": "function", "function": map[string]any{"name": req.ToolChoiceFunction}}
		if len(req.AllowedTools) > 0 {
			tc["allowed_tools"] = req.AllowedTools
		}
		m["tool_choice"] = tc
	} else if req.ToolChoice != "" {
		m["tool_choice"] = req.ToolChoice
	}
	if req.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if len(req.ResponseModality) > 0 {
		m["modalities"] = req.ResponseModality
	}
	if req.AudioConfig != nil {
		ac := map[string]any{}
		if req.AudioConfig.Voice != "" {
			ac["voice"] = req.AudioConfig.Voice
		}
		if req.AudioConfig.Format != "" {
			ac["format"] = req.AudioConfig.Format
		}
		if len(ac) > 0 {
			m["audio"] = ac
		}
	}

	if req.Metadata != nil {
		for _, k := range []string{ir.MetaOpenAILogprobs, ir.MetaOpenAITopLogprobs, ir.MetaOpenAILogitBias, ir.MetaOpenAISeed, ir.MetaOpenAIUser, ir.MetaOpenAIFrequencyPenalty, ir.MetaOpenAIPresencePenalty} {
			if v, ok := req.Metadata[k]; ok {
				m[strings.TrimPrefix(k, "openai:")] = v
			}
		}
		if v, ok := req.Metadata["service_tier"]; ok {
			m["service_tier"] = v
		}
	}
	if req.ServiceTier != "" {
		m["service_tier"] = string(req.ServiceTier)
	}

	return json.Marshal(m)
}

func convertToResponsesAPIRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]any{"model": req.Model}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		m["top_p"] = *req.TopP
	}
	if req.MaxTokens != nil {
		m["max_output_tokens"] = *req.MaxTokens
	}
	if req.Instructions != "" {
		m["instructions"] = req.Instructions
	}
	if req.AudioConfig != nil {
		ac := map[string]any{}
		if req.AudioConfig.Voice != "" {
			ac["voice"] = req.AudioConfig.Voice
		}
		if req.AudioConfig.Format != "" {
			ac["format"] = req.AudioConfig.Format
		}
		if len(ac) > 0 {
			m["audio"] = ac
		}
	}
	if len(req.ResponseModality) > 0 {
		m["modalities"] = req.ResponseModality
	}

	var input []any
	for _, msg := range req.Messages {
		if msg.Role == ir.RoleSystem && req.Instructions != "" {
			continue
		}
		if item := convertMessageToResponsesInput(msg); item != nil {
			input = append(input, item)
		}
	}
	if len(input) > 0 {
		m["input"] = input
	}

	if req.ResponseSchema != nil {
		rf := map[string]any{"type": "json_schema", "json_schema": map[string]any{"schema": req.ResponseSchema}}
		if req.ResponseSchemaName != "" {
			rf["json_schema"].(map[string]any)["name"] = req.ResponseSchemaName
		}
		if req.ResponseSchemaStrict {
			rf["json_schema"].(map[string]any)["strict"] = true
		}
		m["response_format"] = rf
	}

	if req.Thinking != nil && (req.Thinking.IncludeThoughts || req.Thinking.Effort != "" || req.Thinking.Summary != "") {
		r := map[string]any{}
		if req.Thinking.Effort != "" {
			r["effort"] = req.Thinking.Effort
		} else if req.Thinking.IncludeThoughts {
			b := 0
			if req.Thinking.ThinkingBudget != nil {
				b = int(*req.Thinking.ThinkingBudget)
			}
			r["effort"] = ir.BudgetToEffort(b, "low")
		}
		if req.Thinking.Summary != "" {
			r["summary"] = req.Thinking.Summary
		}
		if len(r) > 0 {
			m["reasoning"] = r
		}
	}

	var tools []any
	for _, t := range req.Tools {
		tools = append(tools, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": t.Parameters})
	}
	if req.Metadata != nil {
		for k, mk := range map[string]string{ir.MetaGoogleSearch: "web_search_preview", ir.MetaCodeExecution: "code_interpreter", ir.MetaFileSearch: "file_search"} {
			if cfg, ok := req.Metadata[k]; ok {
				t := map[string]any{"type": mk}
				if m, ok := cfg.(map[string]any); ok {
					for ck, cv := range m {
						t[ck] = cv
					}
				}
				tools = append(tools, t)
			}
		}
	}
	if len(tools) > 0 {
		m["tools"] = tools
	}

	if req.ToolChoice == "function" && req.ToolChoiceFunction != "" {
		m["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": req.ToolChoiceFunction}}
	} else if req.ToolChoice != "" {
		m["tool_choice"] = req.ToolChoice
	}
	if req.ParallelToolCalls != nil {
		m["parallel_tool_calls"] = *req.ParallelToolCalls
	}
	if req.PreviousResponseID != "" {
		m["previous_response_id"] = req.PreviousResponseID
	}
	if req.PromptID != "" {
		p := map[string]any{"id": req.PromptID}
		if req.PromptVersion != "" {
			p["version"] = req.PromptVersion
		}
		if len(req.PromptVariables) > 0 {
			p["variables"] = req.PromptVariables
		}
		m["prompt"] = p
	}
	if req.PromptCacheKey != "" {
		m["prompt_cache_key"] = req.PromptCacheKey
	}
	if req.Store != nil {
		m["store"] = *req.Store
	}

	return json.Marshal(m)
}

func convertMessageToResponsesInput(msg ir.Message) any {
	switch msg.Role {
	case ir.RoleSystem:
		if t := ir.CombineTextParts(msg); t != "" {
			return map[string]any{"type": "message", "role": "system", "content": []any{map[string]any{"type": "input_text", "text": t}}}
		}
	case ir.RoleUser:
		return buildResponsesUserMessage(msg)
	case ir.RoleAssistant:
		if len(msg.ToolCalls) > 0 {
			tc := msg.ToolCalls[0]
			return map[string]any{"type": "function_call", "call_id": tc.ID, "name": tc.Name, "arguments": tc.Args}
		}
		if t := ir.CombineTextParts(msg); t != "" {
			return map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": t}}}
		}
	case ir.RoleTool:
		for _, p := range msg.Content {
			if p.Type == ir.ContentTypeToolResult && p.ToolResult != nil {
				res := map[string]any{"type": "function_call_output", "call_id": p.ToolResult.ToolCallID, "output": p.ToolResult.Result}
				if p.ToolResult.IsError {
					res["is_error"] = true
				}
				return res
			}
		}
	}
	return nil
}

func buildResponsesUserMessage(msg ir.Message) any {
	var c []any
	for _, p := range msg.Content {
		switch p.Type {
		case ir.ContentTypeText:
			if p.Text != "" {
				c = append(c, map[string]any{"type": "input_text", "text": p.Text})
			}
		case ir.ContentTypeImage:
			if p.Image != nil {
				if p.Image.URL != "" {
					c = append(c, map[string]any{"type": "input_image", "image_url": p.Image.URL})
				} else if p.Image.Data != "" {
					c = append(c, map[string]any{"type": "input_image", "image_url": fmt.Sprintf("data:%s;base64,%s", p.Image.MimeType, p.Image.Data)})
				}
			}
		case ir.ContentTypeFile:
			if p.File != nil {
				i := map[string]any{"type": "input_file"}
				if p.File.FileID != "" {
					i["file_id"] = p.File.FileID
				}
				if p.File.FileURL != "" {
					i["file_url"] = p.File.FileURL
				}
				if p.File.Filename != "" {
					i["filename"] = p.File.Filename
				}
				if p.File.FileData != "" {
					i["file_data"] = p.File.FileData
				}
				c = append(c, i)
			}
		case ir.ContentTypeAudio:
			if p.Audio != nil && p.Audio.Data != "" {
				ia := map[string]any{"data": p.Audio.Data}
				if p.Audio.Format != "" {
					ia["format"] = p.Audio.Format
				}
				c = append(c, map[string]any{"type": "input_audio", "input_audio": ia})
			}
		}
	}
	if len(c) == 0 {
		return nil
	}
	return map[string]any{"type": "message", "role": "user", "content": c}
}

func ToOpenAIChatCompletion(ms []ir.Message, us *ir.Usage, model, mid string) ([]byte, error) {
	return ToOpenAIChatCompletionMeta(ms, us, model, mid, nil)
}

func ToOpenAIChatCompletionCandidates(cs []ir.CandidateResult, us *ir.Usage, model, mid string, meta *ir.OpenAIMeta) ([]byte, error) {
	rid, cr := mid, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			rid = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			cr = meta.CreateTime
		}
	}
	res := map[string]any{"id": rid, "object": "chat.completion", "created": cr, "model": model, "choices": []any{}}
	if meta != nil && meta.ServiceTier != "" {
		res["service_tier"] = meta.ServiceTier
	}
	var chs []any
	for _, c := range cs {
		if len(c.Messages) == 0 {
			continue
		}
		b := ir.NewResponseBuilder(c.Messages, us, model, false)
		m := b.GetLastMessage()
		if m == nil {
			continue
		}
		mc := map[string]any{"role": string(m.Role)}
		t, tcs := b.GetTextContent(), b.BuildOpenAIToolCalls()
		if t != "" {
			mc["content"] = t
		} else if tcs != nil {
			mc["content"] = nil
		}
		if r := b.GetReasoningContent(); r != "" {
			ir.AddReasoningToMessage(mc, r, "")
		}
		if tcs != nil {
			mc["tool_calls"] = tcs
		}
		co := map[string]any{"index": c.Index, "finish_reason": ir.MapFinishReasonToOpenAI(c.FinishReason), "message": mc}
		if c.Logprobs != nil {
			co["logprobs"] = c.Logprobs
		}
		chs = append(chs, co)
	}
	res["choices"] = chs
	if us != nil {
		res["usage"] = buildUsageMap(us, meta)
	}
	if meta != nil && meta.GroundingMetadata != nil {
		res["grounding_metadata"] = buildOpenAIGroundingMetadata(meta.GroundingMetadata)
	} else {
		for _, c := range cs {
			if c.GroundingMetadata != nil {
				res["grounding_metadata"] = buildOpenAIGroundingMetadata(c.GroundingMetadata)
				break
			}
		}
	}
	return json.Marshal(res)
}

func ToOpenAIChatCompletionMeta(ms []ir.Message, us *ir.Usage, model, mid string, meta *ir.OpenAIMeta) ([]byte, error) {
	b := ir.NewResponseBuilder(ms, us, model, false)
	rid, cr := mid, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			rid = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			cr = meta.CreateTime
		}
	}
	res := map[string]any{"id": rid, "object": "chat.completion", "created": cr, "model": model, "choices": []any{}}
	if meta != nil && meta.ServiceTier != "" {
		res["service_tier"] = meta.ServiceTier
	}
	if m := b.GetLastMessage(); m != nil {
		mc := map[string]any{"role": string(m.Role)}
		t, tcs := b.GetTextContent(), b.BuildOpenAIToolCalls()
		if t != "" {
			mc["content"] = t
		} else if tcs != nil {
			mc["content"] = nil
		}
		if r := b.GetReasoningContent(); r != "" {
			ir.AddReasoningToMessage(mc, r, "")
		}
		if tcs != nil {
			mc["tool_calls"] = tcs
		}
		if ap := findAudioContent(*m); ap != nil {
			ao := map[string]any{}
			if ap.ID != "" {
				ao["id"] = ap.ID
			}
			if ap.Data != "" {
				ao["data"] = ap.Data
			}
			if ap.Transcript != "" {
				ao["transcript"] = ap.Transcript
			}
			if ap.ExpiresAt > 0 {
				ao["expires_at"] = ap.ExpiresAt
			}
			if len(ao) > 0 {
				mc["audio"] = ao
			}
		}
		co := map[string]any{"index": 0, "finish_reason": b.DetermineFinishReason(), "message": mc}
		if meta != nil {
			if meta.NativeFinishReason != "" {
				co["native_finish_reason"] = meta.NativeFinishReason
			}
			if meta.Logprobs != nil {
				co["logprobs"] = meta.Logprobs
			}
		}
		res["choices"] = []any{co}
	}
	if us != nil {
		res["usage"] = buildUsageMap(us, meta)
	}
	if meta != nil && meta.GroundingMetadata != nil {
		res["grounding_metadata"] = buildOpenAIGroundingMetadata(meta.GroundingMetadata)
	}
	return json.Marshal(res)
}

func buildUsageMap(us *ir.Usage, meta *ir.OpenAIMeta) map[string]any {
	um := map[string]any{"prompt_tokens": us.PromptTokens, "completion_tokens": us.CompletionTokens, "total_tokens": us.TotalTokens}
	pd := map[string]any{}
	if us.PromptTokensDetails != nil {
		if us.PromptTokensDetails.CachedTokens > 0 {
			pd["cached_tokens"] = us.PromptTokensDetails.CachedTokens
		}
		if us.PromptTokensDetails.AudioTokens > 0 {
			pd["audio_tokens"] = us.PromptTokensDetails.AudioTokens
		}
	} else if us.CachedTokens > 0 {
		pd["cached_tokens"] = us.CachedTokens
	}
	if len(pd) > 0 {
		um["prompt_tokens_details"] = pd
	}
	cd := map[string]any{}
	var tt int32
	if meta != nil && meta.ThoughtsTokenCount > 0 {
		tt = meta.ThoughtsTokenCount
	} else if us.ThoughtsTokenCount > 0 {
		tt = us.ThoughtsTokenCount
	}
	if tt > 0 {
		cd["reasoning_tokens"] = tt
	}
	if us.CompletionTokensDetails != nil {
		if us.CompletionTokensDetails.AudioTokens > 0 {
			cd["audio_tokens"] = us.CompletionTokensDetails.AudioTokens
		}
		if us.CompletionTokensDetails.AcceptedPredictionTokens > 0 {
			cd["accepted_prediction_tokens"] = us.CompletionTokensDetails.AcceptedPredictionTokens
		}
		if us.CompletionTokensDetails.RejectedPredictionTokens > 0 {
			cd["rejected_prediction_tokens"] = us.CompletionTokensDetails.RejectedPredictionTokens
		}
	} else {
		if us.AcceptedPredictionTokens > 0 {
			cd["accepted_prediction_tokens"] = us.AcceptedPredictionTokens
		}
		if us.RejectedPredictionTokens > 0 {
			cd["rejected_prediction_tokens"] = us.RejectedPredictionTokens
		}
	}
	if len(cd) > 0 {
		um["completion_tokens_details"] = cd
	}
	return um
}

func ToOpenAIChunk(ev ir.UnifiedEvent, model, mid string, ci int) ([]byte, error) {
	return ToOpenAIChunkMeta(ev, model, mid, ci, nil)
}

type openaiTextChunk struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index int `json:"index"`
		Delta struct {
			Role    string `json:"role,omitempty"`
			Content string `json:"content,omitempty"`
		} `json:"delta"`
	} `json:"choices"`
}

func ToOpenAIChunkMeta(ev ir.UnifiedEvent, model, mid string, ci int, meta *ir.OpenAIMeta) ([]byte, error) {
	if ev.Type == ir.EventTypeStreamMeta {
		return nil, nil
	}
	rid, cr := mid, time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			rid = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			cr = meta.CreateTime
		}
	}
	if ev.Type == ir.EventTypeToken && ev.Content != "" && ev.Refusal == "" && ev.Logprobs == nil && ev.SystemFingerprint == "" {
		ch := openaiTextChunk{ID: rid, Object: "chat.completion.chunk", Created: cr, Model: model, Choices: make([]struct {
			Index int `json:"index"`
			Delta struct {
				Role    string `json:"role,omitempty"`
				Content string `json:"content,omitempty"`
			} `json:"delta"`
		}, 1)}
		ch.Choices[0].Delta.Role, ch.Choices[0].Delta.Content = "assistant", ev.Content
		jb, _ := json.Marshal(ch)
		return ir.BuildSSEChunk(jb), nil
	}
	ch := map[string]any{"id": rid, "object": "chat.completion.chunk", "created": cr, "model": model, "choices": []any{}}
	if ev.SystemFingerprint != "" {
		ch["system_fingerprint"] = ev.SystemFingerprint
	}
	c := map[string]any{"index": 0, "delta": map[string]any{}}
	switch ev.Type {
	case ir.EventTypeToken:
		d := map[string]any{"role": "assistant"}
		if ev.Content != "" {
			d["content"] = ev.Content
		}
		if ev.Refusal != "" {
			d["refusal"] = ev.Refusal
		}
		c["delta"] = d
	case ir.EventTypeReasoning:
		c["delta"] = ir.BuildReasoningDelta(ev.Reasoning, string(ev.ThoughtSignature))
	case ir.EventTypeToolCall:
		if ev.ToolCall != nil {
			tm := map[string]any{"index": ci, "id": ev.ToolCall.ID, "type": "function", "function": map[string]any{"name": ev.ToolCall.Name, "arguments": ev.ToolCall.Args}}
			ts := ev.ThoughtSignature
			if len(ts) == 0 {
				ts = ev.ToolCall.ThoughtSignature
			}
			if len(ts) > 0 {
				tm["extra_content"] = map[string]any{"google": map[string]any{"thought_signature": string(ts)}}
			}
			c["delta"] = map[string]any{"role": "assistant", "tool_calls": []any{tm}}
		}
	case ir.EventTypeToolCallDelta:
		if ev.ToolCall != nil {
			tm := map[string]any{"index": ci, "function": map[string]any{"arguments": ev.ToolCall.Args}}
			c["delta"] = map[string]any{"tool_calls": []any{tm}}
		}
	case ir.EventTypeImage:
		if ev.Image != nil {
			c["delta"] = map[string]any{"role": "assistant", "images": []any{map[string]any{"type": "image_url", "image_url": map[string]string{"url": fmt.Sprintf("data:%s;base64,%s", ev.Image.MimeType, ev.Image.Data)}}}}
		}
	case ir.EventTypeAudio:
		if ev.Audio != nil {
			ao := map[string]any{}
			if ev.Audio.ID != "" {
				ao["id"] = ev.Audio.ID
			}
			if ev.Audio.Data != "" {
				ao["data"] = ev.Audio.Data
			}
			if ev.Audio.Transcript != "" {
				ao["transcript"] = ev.Audio.Transcript
			}
			if ev.Audio.ExpiresAt > 0 {
				ao["expires_at"] = ev.Audio.ExpiresAt
			}
			c["delta"] = map[string]any{"role": "assistant", "audio": ao}
		}
	case ir.EventTypeFinish:
		c["finish_reason"] = ir.MapFinishReasonToOpenAI(ev.FinishReason)
		if meta != nil && meta.NativeFinishReason != "" {
			c["native_finish_reason"] = meta.NativeFinishReason
		}
		if ev.Logprobs != nil {
			c["logprobs"] = ev.Logprobs
		}
		if ev.ContentFilter != nil {
			c["content_filter_results"] = ev.ContentFilter
		}
		if ev.Usage != nil {
			ch["usage"] = buildUsageMap(ev.Usage, meta)
		}
		if ev.GroundingMetadata != nil {
			ch["grounding_metadata"] = buildOpenAIGroundingMetadata(ev.GroundingMetadata)
		}
	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", ev.Error)
	}
	if ev.Logprobs != nil && ev.Type != ir.EventTypeFinish {
		c["logprobs"] = ev.Logprobs
	}
	ch["choices"] = []any{c}
	jb, _ := json.Marshal(ch)
	return ir.BuildSSEChunk(jb), nil
}

func convertMessageToOpenAI(msg ir.Message) map[string]any {
	var res map[string]any
	switch msg.Role {
	case ir.RoleSystem:
		if t := ir.CombineTextParts(msg); t != "" {
			res = map[string]any{"role": "system", "content": t}
		}
	case ir.RoleUser:
		res = buildOpenAIUserMessage(msg)
	case ir.RoleAssistant:
		res = buildOpenAIAssistantMessage(msg)
	case ir.RoleTool:
		res = buildOpenAIToolMessage(msg)
	}
	if res != nil && msg.CacheControl != nil {
		cc := map[string]any{"type": msg.CacheControl.Type}
		if msg.CacheControl.TTL != nil {
			cc["ttl"] = *msg.CacheControl.TTL
		}
		res["cache_control"] = cc
	}
	return res
}

func buildOpenAIUserMessage(msg ir.Message) map[string]any {
	ps := make([]any, 0, len(msg.Content))
	for i := range msg.Content {
		p := &msg.Content[i]
		switch p.Type {
		case ir.ContentTypeText:
			if p.Text != "" {
				ps = append(ps, map[string]any{"type": "text", "text": p.Text})
			}
		case ir.ContentTypeImage:
			if p.Image != nil {
				ps = append(ps, map[string]any{"type": "image_url", "image_url": map[string]string{"url": fmt.Sprintf("data:%s;base64,%s", p.Image.MimeType, p.Image.Data)}})
			}
		case ir.ContentTypeAudio:
			if p.Audio != nil && p.Audio.Data != "" {
				ia := map[string]any{"data": p.Audio.Data}
				if p.Audio.Format != "" {
					ia["format"] = p.Audio.Format
				}
				ps = append(ps, map[string]any{"type": "input_audio", "input_audio": ia})
			}
		}
	}
	if len(ps) == 0 {
		return nil
	}
	if len(ps) == 1 {
		if tp, ok := ps[0].(map[string]any); ok && tp["type"] == "text" {
			return map[string]any{"role": "user", "content": tp["text"]}
		}
	}
	return map[string]any{"role": "user", "content": ps}
}

func buildOpenAIAssistantMessage(msg ir.Message) map[string]any {
	res := map[string]any{"role": "assistant"}
	t, r := ir.CombineTextAndReasoning(msg)
	if t != "" {
		res["content"] = t
	}
	if r != "" {
		ir.AddReasoningToMessage(res, r, ir.GetFirstReasoningSignature(msg))
	}
	if len(msg.ToolCalls) > 0 {
		tcs := make([]any, len(msg.ToolCalls))
		for i := range msg.ToolCalls {
			tc := &msg.ToolCalls[i]
			tm := map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": tc.Args}}
			if len(tc.ThoughtSignature) > 0 {
				tm["extra_content"] = map[string]any{"google": map[string]any{"thought_signature": string(tc.ThoughtSignature)}}
			}
			tcs[i] = tm
		}
		res["tool_calls"] = tcs
	}
	if msg.Refusal != "" {
		res["refusal"] = msg.Refusal
	}
	return res
}

func buildOpenAIToolMessage(msg ir.Message) map[string]any {
	for _, p := range msg.Content {
		if p.Type == ir.ContentTypeToolResult && p.ToolResult != nil {
			return map[string]any{"role": "tool", "tool_call_id": p.ToolResult.ToolCallID, "content": p.ToolResult.Result}
		}
	}
	return nil
}

func ToResponsesAPIResponse(ms []ir.Message, us *ir.Usage, model string, meta *ir.OpenAIMeta) ([]byte, error) {
	rid, cr := fmt.Sprintf("resp_%d", time.Now().UnixNano()), time.Now().Unix()
	if meta != nil {
		if meta.ResponseID != "" {
			rid = meta.ResponseID
		}
		if meta.CreateTime > 0 {
			cr = meta.CreateTime
		}
	}
	res := map[string]any{"id": rid, "object": "response", "created_at": cr, "status": "completed", "model": model}
	var out []any
	var ot string
	b := ir.NewResponseBuilder(ms, us, model, false)
	for _, m := range ms {
		if m.Role != ir.RoleAssistant {
			continue
		}
		t, r := ir.CombineTextAndReasoning(m)
		if r != "" {
			out = append(out, map[string]any{"id": fmt.Sprintf("rs_%s", rid), "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": r}}})
		}
		if t != "" {
			ot = t
			out = append(out, map[string]any{"id": fmt.Sprintf("msg_%s", rid), "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": t, "annotations": []any{}}}})
		}
		for _, tc := range m.ToolCalls {
			out = append(out, map[string]any{"id": fmt.Sprintf("fc_%s", tc.ID), "type": "function_call", "status": "completed", "call_id": tc.ID, "name": tc.Name, "arguments": tc.Args})
		}
	}
	if len(out) > 0 {
		res["output"] = out
	}
	if ot != "" {
		res["output_text"] = ot
	}
	if usMap := b.BuildUsageMap(); usMap != nil {
		rum := map[string]any{"input_tokens": usMap["prompt_tokens"], "output_tokens": usMap["completion_tokens"], "total_tokens": usMap["total_tokens"]}
		var ct int64
		if us != nil && us.PromptTokensDetails != nil && us.PromptTokensDetails.CachedTokens > 0 {
			ct = us.PromptTokensDetails.CachedTokens
		} else if us != nil && us.CachedTokens > 0 {
			ct = us.CachedTokens
		}
		if ct > 0 {
			rum["input_tokens_details"] = map[string]any{"cached_tokens": ct}
		}
		od := map[string]any{}
		var tt int64
		if us != nil && us.CompletionTokensDetails != nil && us.CompletionTokensDetails.ReasoningTokens > 0 {
			tt = us.CompletionTokensDetails.ReasoningTokens
		} else if meta != nil && meta.ThoughtsTokenCount > 0 {
			tt = int64(meta.ThoughtsTokenCount)
		} else if us != nil && us.ThoughtsTokenCount > 0 {
			tt = int64(us.ThoughtsTokenCount)
		}
		if tt > 0 {
			od["reasoning_tokens"] = tt
		}
		if us != nil && us.CompletionTokensDetails != nil && us.CompletionTokensDetails.AudioTokens > 0 {
			od["audio_tokens"] = us.CompletionTokensDetails.AudioTokens
		}
		if len(od) > 0 {
			rum["output_tokens_details"] = od
		}
		res["usage"] = rum
	}
	if meta != nil && meta.GroundingMetadata != nil {
		res["grounding_metadata"] = buildOpenAIGroundingMetadata(meta.GroundingMetadata)
	}
	return json.Marshal(res)
}

type ResponsesStreamState struct {
	Seq             int
	ResponseID      string
	Created         int64
	Started         bool
	ReasoningID     string
	MsgID           string
	TextBuffer      strings.Builder
	ReasoningBuffer strings.Builder
	FuncCallIDs     map[int]string
	FuncNames       map[int]string
	FuncArgsBuffer  map[int]*strings.Builder
}

func NewResponsesStreamState() *ResponsesStreamState {
	return &ResponsesStreamState{FuncCallIDs: make(map[int]string), FuncNames: make(map[int]string), FuncArgsBuffer: make(map[int]*strings.Builder)}
}

func formatResponsesSSE(et string, jb []byte) string {
	b := ir.GetStringBuilder()
	defer ir.PutStringBuilder(b)
	b.Grow(16 + len(et) + len(jb))
	b.WriteString("event: ")
	b.WriteString(et)
	b.WriteString("\ndata: ")
	b.Write(jb)
	b.WriteString("\n\n")
	return b.String()
}

func ToResponsesAPIChunk(ev ir.UnifiedEvent, model string, s *ResponsesStreamState) ([]string, error) {
	if ev.Type == ir.EventTypeStreamMeta {
		return nil, nil
	}
	if s.ResponseID == "" {
		s.ResponseID, s.Created = fmt.Sprintf("resp_%d", time.Now().UnixNano()), time.Now().Unix()
	}
	ns := func() int { s.Seq++; return s.Seq }
	out := make([]string, 0, 4)
	if !s.Started {
		for _, t := range []string{"response.created", "response.in_progress"} {
			b, _ := json.Marshal(map[string]any{"type": t, "sequence_number": ns(), "response": map[string]any{"id": s.ResponseID, "object": "response", "created_at": s.Created, "status": "in_progress"}})
			out = append(out, formatResponsesSSE(t, b))
		}
		s.Started = true
	}
	switch ev.Type {
	case ir.EventTypeToken:
		if s.MsgID == "" {
			s.MsgID = fmt.Sprintf("msg_%s", s.ResponseID)
			b1, _ := json.Marshal(map[string]any{"type": "response.output_item.added", "sequence_number": ns(), "output_index": 0, "item": map[string]any{"id": s.MsgID, "type": "message", "status": "in_progress", "role": "assistant", "content": []any{}}})
			out = append(out, formatResponsesSSE("response.output_item.added", b1))
			b2, _ := json.Marshal(map[string]any{"type": "response.content_part.added", "sequence_number": ns(), "item_id": s.MsgID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": ""}})
			out = append(out, formatResponsesSSE("response.content_part.added", b2))
		}
		s.TextBuffer.WriteString(ev.Content)
		b, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "sequence_number": ns(), "item_id": s.MsgID, "output_index": 0, "content_index": 0, "delta": ev.Content})
		out = append(out, formatResponsesSSE("response.output_text.delta", b))
	case ir.EventTypeReasoning, ir.EventTypeReasoningSummary:
		t := ev.Reasoning
		if ev.Type == ir.EventTypeReasoningSummary {
			t = ev.ReasoningSummary
		}
		if s.ReasoningID == "" {
			s.ReasoningID = fmt.Sprintf("rs_%s", s.ResponseID)
			b, _ := json.Marshal(map[string]any{"type": "response.output_item.added", "sequence_number": ns(), "output_index": 0, "item": map[string]any{"id": s.ReasoningID, "type": "reasoning", "status": "in_progress", "summary": []any{}}})
			out = append(out, formatResponsesSSE("response.output_item.added", b))
		}
		s.ReasoningBuffer.WriteString(t)
		b, _ := json.Marshal(map[string]any{"type": "response.reasoning_summary_text.delta", "sequence_number": ns(), "item_id": s.ReasoningID, "output_index": 0, "content_index": 0, "delta": t})
		out = append(out, formatResponsesSSE("response.reasoning_summary_text.delta", b))
	case ir.EventTypeToolCall:
		idx := ev.ToolCallIndex
		if _, ok := s.FuncCallIDs[idx]; !ok {
			s.FuncCallIDs[idx], s.FuncNames[idx] = fmt.Sprintf("fc_%s", ev.ToolCall.ID), ev.ToolCall.Name
			b, _ := json.Marshal(map[string]any{"type": "response.output_item.added", "sequence_number": ns(), "output_index": idx, "item": map[string]any{"id": s.FuncCallIDs[idx], "type": "function_call", "status": "in_progress", "call_id": ev.ToolCall.ID, "name": ev.ToolCall.Name, "arguments": ""}})
			out = append(out, formatResponsesSSE("response.output_item.added", b))
		}
		if ev.ToolCall.Args != "" {
			b, _ := json.Marshal(map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": ns(), "item_id": s.FuncCallIDs[idx], "output_index": idx, "delta": ev.ToolCall.Args})
			out = append(out, formatResponsesSSE("response.function_call_arguments.delta", b))
		}
		b, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "sequence_number": ns(), "item_id": s.FuncCallIDs[idx], "output_index": idx, "item": map[string]any{"id": s.FuncCallIDs[idx], "type": "function_call", "status": "completed", "call_id": ev.ToolCall.ID, "name": ev.ToolCall.Name, "arguments": ev.ToolCall.Args}})
		out = append(out, formatResponsesSSE("response.output_item.done", b))
	case ir.EventTypeToolCallDelta:
		idx := ev.ToolCallIndex
		if _, ok := s.FuncCallIDs[idx]; !ok {
			s.FuncCallIDs[idx] = fmt.Sprintf("fc_%s", ev.ToolCall.ID)
			b, _ := json.Marshal(map[string]any{"type": "response.output_item.added", "sequence_number": ns(), "output_index": idx, "item": map[string]any{"id": s.FuncCallIDs[idx], "type": "function_call", "status": "in_progress", "call_id": ev.ToolCall.ID, "name": "", "arguments": ""}})
			out = append(out, formatResponsesSSE("response.output_item.added", b))
		}
		if s.FuncArgsBuffer[idx] == nil {
			s.FuncArgsBuffer[idx] = &strings.Builder{}
		}
		s.FuncArgsBuffer[idx].WriteString(ev.ToolCall.Args)
		b, _ := json.Marshal(map[string]any{"type": "response.function_call_arguments.delta", "sequence_number": ns(), "item_id": s.FuncCallIDs[idx], "output_index": idx, "delta": ev.ToolCall.Args})
		out = append(out, formatResponsesSSE("response.function_call_arguments.delta", b))
	case ir.EventTypeFinish:
		t, r := s.TextBuffer.String(), s.ReasoningBuffer.String()
		if s.MsgID != "" {
			b1, _ := json.Marshal(map[string]any{"type": "response.content_part.done", "sequence_number": ns(), "item_id": s.MsgID, "output_index": 0, "content_index": 0, "part": map[string]any{"type": "output_text", "text": t}})
			out = append(out, formatResponsesSSE("response.content_part.done", b1))
			b2, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "sequence_number": ns(), "output_index": 0, "item": map[string]any{"id": s.MsgID, "type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": t}}}})
			out = append(out, formatResponsesSSE("response.output_item.done", b2))
		}
		if s.ReasoningID != "" {
			b, _ := json.Marshal(map[string]any{"type": "response.output_item.done", "sequence_number": ns(), "output_index": 0, "item": map[string]any{"id": s.ReasoningID, "type": "reasoning", "status": "completed", "summary": []any{map[string]any{"type": "summary_text", "text": r}}}})
			out = append(out, formatResponsesSSE("response.output_item.done", b))
		}
		um := map[string]any{}
		if ev.Usage != nil {
			um = map[string]any{"input_tokens": ev.Usage.PromptTokens, "output_tokens": ev.Usage.CompletionTokens, "total_tokens": ev.Usage.TotalTokens}
			var ct int64
			if ev.Usage.PromptTokensDetails != nil && ev.Usage.PromptTokensDetails.CachedTokens > 0 {
				ct = ev.Usage.PromptTokensDetails.CachedTokens
			} else if ev.Usage.CachedTokens > 0 {
				ct = ev.Usage.CachedTokens
			}
			if ct > 0 {
				um["input_tokens_details"] = map[string]any{"cached_tokens": ct}
			}
			var rt int64
			if ev.Usage.CompletionTokensDetails != nil && ev.Usage.CompletionTokensDetails.ReasoningTokens > 0 {
				rt = ev.Usage.CompletionTokensDetails.ReasoningTokens
			} else if ev.Usage.ThoughtsTokenCount > 0 {
				rt = int64(ev.Usage.ThoughtsTokenCount)
			}
			if rt > 0 {
				um["output_tokens_details"] = map[string]any{"reasoning_tokens": rt}
			}
		}
		b, _ := json.Marshal(map[string]any{"type": "response.done", "sequence_number": ns(), "response": map[string]any{"id": s.ResponseID, "object": "response", "created_at": s.Created, "status": "completed", "usage": um}})
		out = append(out, formatResponsesSSE("response.done", b))
	}
	return out, nil
}

func buildOpenAIGroundingMetadata(gm *ir.GroundingMetadata) map[string]any {
	if gm == nil {
		return nil
	}
	res := map[string]any{}
	if len(gm.WebSearchQueries) > 0 {
		res["web_search_queries"] = gm.WebSearchQueries
	}
	if gm.SearchEntryPoint != nil && gm.SearchEntryPoint.RenderedContent != "" {
		res["search_entry_point"] = map[string]any{"rendered_content": gm.SearchEntryPoint.RenderedContent}
	}
	if len(gm.GroundingChunks) > 0 {
		var s []map[string]any
		for _, c := range gm.GroundingChunks {
			if c.Web != nil {
				sm := map[string]any{"type": "web", "uri": c.Web.URI, "title": c.Web.Title}
				if c.Web.Domain != "" {
					sm["domain"] = c.Web.Domain
				}
				s = append(s, sm)
			}
		}
		if len(s) > 0 {
			res["sources"] = s
		}
	}
	if len(gm.GroundingSupports) > 0 {
		var cs []map[string]any
		for _, sup := range gm.GroundingSupports {
			ci := map[string]any{}
			if sup.Segment != nil {
				ci["text"] = sup.Segment.Text
				if sup.Segment.StartIndex > 0 {
					ci["start_index"] = sup.Segment.StartIndex
				}
				if sup.Segment.EndIndex > 0 {
					ci["end_index"] = sup.Segment.EndIndex
				}
			}
			if len(sup.GroundingChunkIndices) > 0 {
				ci["source_indices"] = sup.GroundingChunkIndices
			}
			cs = append(cs, ci)
		}
		if len(cs) > 0 {
			res["citations"] = cs
		}
	}
	return res
}

func findAudioContent(m ir.Message) *ir.AudioPart {
	for _, p := range m.Content {
		if p.Type == ir.ContentTypeAudio && p.Audio != nil {
			return p.Audio
		}
	}
	return nil
}
