package to_ir

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/misc"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

func ParseOpenAIRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	root, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, err
	}

	req := &ir.UnifiedChatRequest{
		Model:    root.Get("model").String(),
		Metadata: make(map[string]any, 8),
	}

	if v := root.Get("temperature"); v.Exists() {
		req.Temperature = ir.Ptr(v.Float())
	}
	if v := root.Get("top_p"); v.Exists() {
		req.TopP = ir.Ptr(v.Float())
	}
	if v := root.Get("top_k"); v.Exists() {
		req.TopK = ir.Ptr(int(v.Int()))
	}

	if v := root.Get("max_tokens"); v.Exists() {
		req.MaxTokens = ir.Ptr(int(v.Int()))
	} else if v := root.Get("max_output_tokens"); v.Exists() {
		req.MaxTokens = ir.Ptr(int(v.Int()))
	} else if v := root.Get("max_completion_tokens"); v.Exists() {
		req.MaxTokens = ir.Ptr(int(v.Int()))
	}

	if v := root.Get("stop"); v.Exists() {
		if v.IsArray() {
			for _, s := range v.Array() {
				req.StopSequences = append(req.StopSequences, s.String())
			}
		} else {
			req.StopSequences = []string{v.String()}
		}
	}
	if v := root.Get("frequency_penalty"); v.Exists() {
		req.FrequencyPenalty = ir.Ptr(v.Float())
	}
	if v := root.Get("presence_penalty"); v.Exists() {
		req.PresencePenalty = ir.Ptr(v.Float())
	}
	if v := root.Get("logprobs"); v.Exists() {
		req.Logprobs = ir.Ptr(v.Bool())
	}
	if v := root.Get("top_logprobs"); v.Exists() {
		req.TopLogprobs = ir.Ptr(int(v.Int()))
	}
	if v := root.Get("n"); v.Exists() {
		req.CandidateCount = ir.Ptr(int(v.Int()))
	}

	if input := root.Get("input"); input.Exists() && !root.Get("messages").Exists() {
		parseResponsesAPIFields(root, req)
	} else {
		for _, m := range root.Get("messages").Array() {
			req.Messages = append(req.Messages, parseOpenAIMessage(m))
		}
	}

	for _, t := range root.Get("tools").Array() {
		toolType := t.Get("type").String()
		if !t.Get("function").Exists() {
			if strings.HasPrefix(toolType, "web_search") {
				conf := map[string]any{}
				if v := t.Get("search_context_size"); v.Exists() {
					conf["search_context_size"] = v.String()
				}
				if v := t.Get("user_location"); v.IsObject() {
					var val any
					if json.Unmarshal([]byte(v.Raw), &val) == nil {
						conf["user_location"] = val
					}
				}
				req.Metadata[ir.MetaGoogleSearch] = conf
				continue
			}
			if toolType == "code_interpreter" {
				conf := map[string]any{}
				if v := t.Get("container"); v.IsObject() {
					var val any
					if json.Unmarshal([]byte(v.Raw), &val) == nil {
						conf["container"] = val
					}
				}
				req.Metadata[ir.MetaCodeExecution] = conf
				continue
			}
			if toolType == "file_search" {
				conf := map[string]any{}
				if v := t.Get("vector_store"); v.IsObject() {
					var val any
					if json.Unmarshal([]byte(v.Raw), &val) == nil {
						conf["vector_store"] = val
					}
				}
				if v := t.Get("max_num_results"); v.Exists() {
					conf["max_num_results"] = int(v.Int())
				}
				if v := t.Get("ranking_options"); v.IsObject() {
					var val any
					if json.Unmarshal([]byte(v.Raw), &val) == nil {
						conf["ranking_options"] = val
					}
				}
				req.Metadata[ir.MetaFileSearch] = conf
				continue
			}
		}
		if tool := parseOpenAITool(t); tool != nil {
			req.Tools = append(req.Tools, *tool)
		}
	}

	if v := root.Get("parallel_tool_calls"); v.Exists() {
		req.ParallelToolCalls = ir.Ptr(v.Bool())
	}
	for _, m := range root.Get("modalities").Array() {
		req.ResponseModality = append(req.ResponseModality, strings.ToUpper(m.String()))
	}
	if v := root.Get("image_config"); v.IsObject() {
		req.ImageConfig = &ir.ImageConfig{
			AspectRatio: v.Get("aspect_ratio").String(),
			ImageSize:   v.Get("image_size").String(),
		}
	}
	if v := root.Get("audio"); v.IsObject() {
		req.AudioConfig = &ir.AudioConfig{
			Voice:  v.Get("voice").String(),
			Format: v.Get("format").String(),
		}
	}
	if v := root.Get("prediction"); v.IsObject() && v.Get("type").String() == "content" {
		req.Prediction = &ir.PredictionConfig{Type: "content", Content: v.Get("content").String()}
	}
	if v := root.Get("stream_options"); v.IsObject() {
		req.StreamOptions = &ir.StreamOptionsConfig{IncludeUsage: v.Get("include_usage").Bool()}
	}

	req.Thinking = parseThinkingConfig(root)

	if rf := root.Get("response_format"); rf.Exists() {
		if rf.Get("type").String() == "json_schema" {
			req.ResponseSchemaName = rf.Get("json_schema.name").String()
			if v := rf.Get("json_schema.schema"); v.IsObject() {
				var schema map[string]any
				if json.Unmarshal([]byte(v.Raw), &schema) == nil {
					req.ResponseSchema = schema
				}
			}
			req.ResponseSchemaStrict = rf.Get("json_schema.strict").Bool()
		} else if rf.Get("type").String() == "json_object" {
			req.Metadata["ollama_format"] = "json"
		}
	}

	if v := root.Get("logit_bias"); v.IsObject() {
		var lb any
		if json.Unmarshal([]byte(v.Raw), &lb) == nil {
			req.Metadata[ir.MetaOpenAILogitBias] = lb
		}
	}
	if v := root.Get("seed"); v.Exists() {
		req.Metadata[ir.MetaOpenAISeed] = int(v.Int())
	}
	if v := root.Get("user").String(); v != "" {
		req.Metadata[ir.MetaOpenAIUser] = v
	}
	if v := root.Get("service_tier").String(); v != "" {
		req.ServiceTier = ir.ServiceTier(v)
	}

	if v := root.Get("tool_choice"); v.Exists() {
		if v.IsObject() {
			t := v.Get("type").String()
			if t == "function" || (t == "" && v.Get("function.name").Exists()) {
				req.ToolChoice = "function"
				req.ToolChoiceFunction = v.Get("function.name").String()
				for _, a := range v.Get("allowed_tools").Array() {
					req.AllowedTools = append(req.AllowedTools, a.String())
				}
			} else {
				req.ToolChoice = t
			}
		} else {
			req.ToolChoice = v.String()
		}
	}

	return req, nil
}

func parseResponsesAPIFields(root gjson.Result, req *ir.UnifiedChatRequest) {
	if v := root.Get("instructions").String(); v != "" {
		req.Instructions = v
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleSystem, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: v}},
		})
	}
	input := root.Get("input")
	if input.Type == gjson.String {
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: input.String()}},
		})
	} else {
		for _, item := range input.Array() {
			if msg := parseResponsesInputItem(item); msg != nil {
				req.Messages = append(req.Messages, *msg)
			}
		}
	}
	req.PreviousResponseID = root.Get("previous_response_id").String()
	if p := root.Get("prompt"); p.IsObject() {
		req.PromptID, req.PromptVersion = p.Get("id").String(), p.Get("version").String()
		if vars := p.Get("variables"); vars.IsObject() {
			req.PromptVariables = vars.Value().(map[string]any)
		}
	}
	req.PromptCacheKey = root.Get("prompt_cache_key").String()
	if v := root.Get("store"); v.Exists() {
		req.Store = ir.Ptr(v.Bool())
	}
}

func parseResponsesInputItem(item gjson.Result) *ir.Message {
	t := item.Get("type").String()
	if t == "" && item.Get("role").Exists() {
		t = "message"
	}
	switch t {
	case "message":
		msg := &ir.Message{Role: ir.MapStandardRole(item.Get("role").String())}
		c := item.Get("content")
		if c.Type == gjson.String {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: c.String()})
		} else {
			for _, p := range c.Array() {
				if cp := parseResponsesContentPart(p); cp != nil {
					msg.Content = append(msg.Content, *cp)
				}
			}
		}
		return msg
	case "function_call":
		return &ir.Message{Role: ir.RoleAssistant, ToolCalls: []ir.ToolCall{{ID: item.Get("call_id").String(), Name: item.Get("name").String(), Args: item.Get("arguments").String()}}}
	case "function_call_output":
		return &ir.Message{Role: ir.RoleTool, Content: []ir.ContentPart{{Type: ir.ContentTypeToolResult, ToolResult: &ir.ToolResultPart{ToolCallID: item.Get("call_id").String(), Result: item.Get("output").String()}}}}
	}
	return nil
}

func parseResponsesContentPart(p gjson.Result) *ir.ContentPart {
	switch p.Get("type").String() {
	case "input_text", "output_text", "text":
		if v := p.Get("text").String(); v != "" {
			return &ir.ContentPart{Type: ir.ContentTypeText, Text: v}
		}
	case "input_image":
		if v := p.Get("image_url.url").String(); v != "" {
			if strings.HasPrefix(v, "data:") {
				return &ir.ContentPart{Type: ir.ContentTypeImage, Image: parseDataURI(v)}
			}
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{URL: v}}
		}
		if v := p.Get("file_id").String(); v != "" {
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{Data: v}}
		}
	case "input_file":
		fp := &ir.FilePart{FileID: p.Get("file_id").String(), FileURL: p.Get("file_url").String(), Filename: p.Get("filename").String(), FileData: p.Get("file_data").String()}
		if fp.FileData != "" && strings.HasPrefix(fp.FileData, "data:") {
			if s := strings.Index(fp.FileData, ";"); s > 5 {
				fp.MimeType = fp.FileData[5:s]
				if c := strings.Index(fp.FileData, ","); c > 0 {
					fp.FileData = fp.FileData[c+1:]
				}
			}
		}
		if fp.FileID != "" || fp.FileURL != "" || fp.FileData != "" {
			return &ir.ContentPart{Type: ir.ContentTypeFile, File: fp}
		}
	}
	return nil
}

func ParseOpenAIResponse(rawJSON []byte) ([]ir.Message, *ir.Usage, error) {
	root, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, nil, err
	}
	usage := ir.ParseOpenAIUsage(root.Get("usage"))
	if v := root.Get("output"); v.IsArray() {
		return parseResponsesAPIOutput(v, usage)
	}

	m := root.Get("choices.0.message")
	if !m.Exists() {
		return nil, usage, nil
	}
	msg := ir.Message{Role: ir.RoleAssistant}
	if rf := ir.ParseReasoningFromJSON(m); rf.Text != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: rf.Text, ThoughtSignature: []byte(rf.Signature)})
	}
	if v := m.Get("content").String(); v != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: v})
	}
	if v := m.Get("audio"); v.IsObject() {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeAudio, Audio: &ir.AudioPart{ID: v.Get("id").String(), Data: v.Get("data").String(), Transcript: v.Get("transcript").String(), ExpiresAt: v.Get("expires_at").Int()}})
	}
	msg.ToolCalls = append(msg.ToolCalls, ir.ParseOpenAIStyleToolCalls(m.Get("tool_calls").Array())...)
	msg.Refusal = m.Get("refusal").String()

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 && msg.Refusal == "" {
		return nil, usage, nil
	}
	return []ir.Message{msg}, usage, nil
}

func parseResponsesAPIOutput(output gjson.Result, usage *ir.Usage) ([]ir.Message, *ir.Usage, error) {
	var res []ir.Message
	for _, item := range output.Array() {
		switch item.Get("type").String() {
		case "message":
			m := ir.Message{Role: ir.RoleAssistant, Refusal: item.Get("refusal").String()}
			for _, c := range item.Get("content").Array() {
				if c.Get("type").String() == "output_text" {
					m.Content = append(m.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: c.Get("text").String()})
				}
			}
			if len(m.Content) > 0 || m.Refusal != "" {
				res = append(res, m)
			}
		case "reasoning":
			m := ir.Message{Role: ir.RoleAssistant}
			for _, s := range item.Get("summary").Array() {
				if s.Get("type").String() == "summary_text" {
					m.Content = append(m.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: s.Get("text").String()})
				}
			}
			if len(m.Content) > 0 {
				res = append(res, m)
			}
		case "function_call":
			res = append(res, ir.Message{Role: ir.RoleAssistant, ToolCalls: []ir.ToolCall{{ID: item.Get("call_id").String(), Name: item.Get("name").String(), Args: item.Get("arguments").String()}}})
		}
	}
	return res, usage, nil
}

func ParseOpenAIChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	raw := bytes.TrimSpace(rawJSON)
	if len(raw) == 0 {
		return nil, nil
	}
	var et string
	data := raw
	if bytes.HasPrefix(raw, []byte("event:")) {
		if i := bytes.IndexByte(raw, '\n'); i > 0 {
			et, data = string(bytes.TrimSpace(raw[6:i])), bytes.TrimSpace(raw[i+1:])
		}
	}
	if bytes.HasPrefix(data, []byte("data:")) {
		data = bytes.TrimSpace(data[5:])
	}
	if len(data) == 0 {
		return nil, nil
	}
	if bytes.Equal(data, []byte("[DONE]")) {
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}}, nil
	}
	root := gjson.ParseBytes(data)
	if et == "" {
		et = root.Get("type").String()
	}
	if et != "" && strings.HasPrefix(et, "response.") {
		return parseResponsesStreamEvent(et, root)
	}

	choice := root.Get("choices.0")
	if !choice.Exists() {
		if u := root.Get("usage"); u.Exists() {
			usage := ir.ParseOpenAIUsage(u)
			return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, Usage: usage, SystemFingerprint: root.Get("system_fingerprint").String()}}, nil
		}
		return nil, nil
	}

	var evs []ir.UnifiedEvent
	d := choice.Get("delta")
	if v := d.Get("content").String(); v != "" {
		evs = append(evs, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: v})
	}
	if v := d.Get("refusal").String(); v != "" {
		evs = append(evs, ir.UnifiedEvent{Type: ir.EventTypeToken, Refusal: v})
	}
	if v := d.Get("audio"); v.IsObject() {
		evs = append(evs, ir.UnifiedEvent{Type: ir.EventTypeAudio, Audio: &ir.AudioPart{ID: v.Get("id").String(), Data: v.Get("data").String(), Transcript: v.Get("transcript").String(), ExpiresAt: v.Get("expires_at").Int()}})
	}
	if rf := ir.ParseReasoningFromJSON(d); rf.Text != "" {
		evs = append(evs, ir.UnifiedEvent{Type: ir.EventTypeReasoning, Reasoning: rf.Text, ThoughtSignature: []byte(rf.Signature)})
	}
	for _, tc := range d.Get("tool_calls").Array() {
		evs = append(evs, ir.UnifiedEvent{Type: ir.EventTypeToolCall, ToolCall: &ir.ToolCall{ID: tc.Get("id").String(), Name: tc.Get("function.name").String(), Args: tc.Get("function.arguments").String()}, ToolCallIndex: int(tc.Get("index").Int())})
	}

	if fr := choice.Get("finish_reason").String(); fr != "" {
		ev := ir.UnifiedEvent{Type: ir.EventTypeFinish, FinishReason: ir.MapOpenAIFinishReason(fr), SystemFingerprint: root.Get("system_fingerprint").String()}
		if v := choice.Get("logprobs"); v.Exists() {
			ev.Logprobs = v.Value()
		}
		if v := choice.Get("content_filter_results"); v.Exists() {
			ev.ContentFilter = v.Value()
		}
		evs = append(evs, ev)
	} else if len(evs) > 0 {
		evs[0].SystemFingerprint = root.Get("system_fingerprint").String()
		if v := choice.Get("logprobs"); v.Exists() {
			evs[0].Logprobs = v.Value()
		}
	}
	return evs, nil
}

func parseResponsesStreamEvent(et string, root gjson.Result) ([]ir.UnifiedEvent, error) {
	switch et {
	case "response.output_text.delta":
		if v := root.Get("delta").String(); v != "" {
			return []ir.UnifiedEvent{{Type: ir.EventTypeToken, Content: v}}, nil
		}
	case "response.reasoning_summary_text.delta":
		if v := root.Get("text").String(); v != "" {
			return []ir.UnifiedEvent{{Type: ir.EventTypeReasoningSummary, ReasoningSummary: v}}, nil
		}
	case "response.function_call_arguments.delta":
		return []ir.UnifiedEvent{{Type: ir.EventTypeToolCallDelta, ToolCall: &ir.ToolCall{ID: root.Get("item_id").String(), Args: root.Get("delta").String()}, ToolCallIndex: int(root.Get("output_index").Int())}}, nil
	case "response.function_call_arguments.done":
		return []ir.UnifiedEvent{{Type: ir.EventTypeToolCall, ToolCall: &ir.ToolCall{ID: root.Get("item_id").String(), Name: root.Get("name").String(), Args: root.Get("arguments").String()}, ToolCallIndex: int(root.Get("output_index").Int())}}, nil
	case "response.web_search_call.in_progress":
		return []ir.UnifiedEvent{{Type: ir.EventTypeToolCall, ToolCall: &ir.ToolCall{ID: root.Get("item_id").String(), Name: "web_search", Args: "{}"}}}, nil
	case "response.refusal.delta":
		if v := root.Get("delta").String(); v != "" {
			return []ir.UnifiedEvent{{Type: ir.EventTypeToken, Refusal: v}}, nil
		}
	case "response.audio_transcript.delta":
		if v := root.Get("delta").String(); v != "" {
			return []ir.UnifiedEvent{{Type: ir.EventTypeAudio, Audio: &ir.AudioPart{Transcript: v}}}, nil
		}
	case "response.completed":
		ev := ir.UnifiedEvent{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}
		if u := root.Get("response.usage"); u.Exists() {
			ev.Usage = ir.ParseOpenAIUsage(u)
		}
		return []ir.UnifiedEvent{ev}, nil
	case "error":
		return []ir.UnifiedEvent{{Type: ir.EventTypeError, FinishReason: ir.FinishReasonError}}, nil
	}
	return nil, nil
}

func parseOpenAIMessage(m gjson.Result) ir.Message {
	role := m.Get("role").String()
	msg := ir.Message{Role: ir.MapStandardRole(role)}
	if cc := m.Get("cache_control"); cc.IsObject() {
		msg.CacheControl = &ir.CacheControl{Type: cc.Get("type").String()}
		if v := cc.Get("ttl"); v.Exists() {
			msg.CacheControl.TTL = ir.Ptr(v.Int())
		}
	}
	if role == "assistant" {
		if rf := ir.ParseReasoningFromJSON(m); rf.Text != "" {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: rf.Text, ThoughtSignature: []byte(rf.Signature)})
		}
	}
	c := m.Get("content")
	if c.Type == gjson.String && role != "tool" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: c.String()})
	} else {
		for _, item := range c.Array() {
			if p := parseOpenAIContentPart(item, &msg); p != nil {
				msg.Content = append(msg.Content, *p)
			}
		}
	}
	if role == "assistant" {
		for _, tc := range m.Get("tool_calls").Array() {
			if tc.Get("type").String() == "function" {
				t := ir.ToolCall{ID: tc.Get("id").String(), Name: tc.Get("function.name").String(), Args: tc.Get("function.arguments").String()}
				if sig := tc.Get("extra_content.google.thought_signature").String(); sig != "" {
					t.ThoughtSignature = []byte(sig)
				}
				msg.ToolCalls = append(msg.ToolCalls, t)
			}
		}
	}
	if role == "tool" {
		id := m.Get("tool_call_id").String()
		if id == "" {
			id = m.Get("tool_use_id").String()
		}
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeToolResult, ToolResult: &ir.ToolResultPart{ToolCallID: id, Result: ir.SanitizeText(extractContentString(c))}})
	}
	return msg
}

func parseOpenAIContentPart(item gjson.Result, msg *ir.Message) *ir.ContentPart {
	switch t := item.Get("type").String(); t {
	case "text":
		if v := item.Get("text").String(); v != "" {
			return &ir.ContentPart{Type: ir.ContentTypeText, Text: v}
		}
	case "thinking", "reasoning":
		textKey := "thinking"
		if t == "reasoning" {
			textKey = "text"
		}
		if v := item.Get(textKey).String(); v != "" {
			return &ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: v, ThoughtSignature: []byte(item.Get("signature").String())}
		}
	case "redacted_thinking":
		return &ir.ContentPart{Type: ir.ContentTypeRedactedThinking, RedactedData: item.Get("data").String()}
	case "image_url":
		u := item.Get("image_url.url").String()
		if img := parseDataURI(u); img != nil {
			img.Detail = item.Get("image_url.detail").String()
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: img}
		}
		if u != "" {
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{URL: u, Detail: item.Get("image_url.detail").String()}}
		}
	case "image":
		mt := item.Get("source.media_type").String()
		if mt == "" {
			mt = "image/png"
		}
		if d := item.Get("source.data").String(); d != "" {
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{MimeType: mt, Data: d}}
		}
	case "input_audio":
		if v := item.Get("input_audio"); v.Exists() {
			return &ir.ContentPart{Type: ir.ContentTypeAudio, Audio: &ir.AudioPart{Data: v.Get("data").String(), Format: v.Get("format").String()}}
		}
	case "file":
		fn, fd, fid, fu := item.Get("file.filename").String(), item.Get("file.file_data").String(), item.Get("file.file_id").String(), item.Get("file.url").String()
		if fn != "" || fd != "" || fid != "" || fu != "" {
			ext := ""
			if i := strings.LastIndex(fn, "."); i >= 0 && i < len(fn)-1 {
				ext = fn[i+1:]
			}
			mt := misc.MimeTypes[ext]
			if mt != "" && strings.HasPrefix(mt, "image/") && fd != "" {
				return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{MimeType: mt, Data: fd}}
			}
			return &ir.ContentPart{Type: ir.ContentTypeFile, File: &ir.FilePart{FileID: fid, FileURL: fu, Filename: fn, FileData: fd, MimeType: mt}}
		}
	case "tool_use":
		args := item.Get("input").Raw
		if args == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{ID: item.Get("id").String(), Name: item.Get("name").String(), Args: args})
	case "tool_result":
		msg.Role = ir.RoleTool
		return &ir.ContentPart{Type: ir.ContentTypeToolResult, ToolResult: &ir.ToolResultPart{ToolCallID: item.Get("tool_use_id").String(), Result: ir.SanitizeText(extractContentString(item.Get("content")))}}
	}
	return nil
}

func parseOpenAITool(t gjson.Result) *ir.ToolDefinition {
	var n, d string
	var pr gjson.Result
	if t.Get("type").String() == "function" {
		fn := t.Get("function")
		n, d, pr = fn.Get("name").String(), fn.Get("description").String(), fn.Get("parameters")
	} else if t.Get("name").Exists() {
		n, d, pr = t.Get("name").String(), t.Get("description").String(), t.Get("input_schema")
	}
	if n == "" {
		return nil
	}
	var params map[string]any
	if pr.IsObject() {
		if json.Unmarshal([]byte(pr.Raw), &params) == nil {
			params = ir.CleanJsonSchema(params)
			if tv, ok := params["type"].(string); !ok || tv == "" || tv == "None" {
				params["type"] = "object"
			}
		}
	}
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return &ir.ToolDefinition{Name: n, Description: d, Parameters: params}
}

func parseThinkingConfig(root gjson.Result) *ir.ThinkingConfig {
	var tc *ir.ThinkingConfig
	if re := root.Get("reasoning_effort"); re.Exists() {
		budget, include := ir.EffortToBudget(re.String())
		tc = &ir.ThinkingConfig{Effort: ir.ReasoningEffort(re.String()), ThinkingBudget: ir.Ptr(int32(budget)), IncludeThoughts: include}
	}
	if r := root.Get("reasoning"); r.IsObject() {
		if tc == nil {
			tc = &ir.ThinkingConfig{}
		}
		if e := r.Get("effort"); e.Exists() {
			budget, include := ir.EffortToBudget(e.String())
			tc.Effort, tc.ThinkingBudget, tc.IncludeThoughts = ir.ReasoningEffort(e.String()), ir.Ptr(int32(budget)), include
		}
		if s := r.Get("summary"); s.Exists() {
			tc.Summary = s.String()
		}
	}
	if v := root.Get("extra_body.google.thinking_config"); v.IsObject() {
		if tc == nil {
			tc = &ir.ThinkingConfig{}
		}
		if b := v.Get("thinkingBudget"); b.Exists() {
			tc.ThinkingBudget = ir.Ptr(int32(b.Int()))
		} else if b := v.Get("thinking_budget"); b.Exists() {
			tc.ThinkingBudget = ir.Ptr(int32(b.Int()))
		}
		if i := v.Get("includeThoughts"); i.Exists() {
			tc.IncludeThoughts = i.Bool()
		} else if i := v.Get("include_thoughts"); i.Exists() {
			tc.IncludeThoughts = i.Bool()
		}
	}
	return tc
}

func parseDataURI(url string) *ir.ImagePart {
	if !strings.HasPrefix(url, "data:") {
		return nil
	}
	p := strings.SplitN(url, ",", 2)
	if len(p) != 2 {
		return nil
	}
	m := "image/jpeg"
	if i := strings.Index(p[0], ";"); i > 5 {
		m = p[0][5:i]
	}
	return &ir.ImagePart{MimeType: m, Data: p[1]}
}

func extractContentString(c gjson.Result) string {
	if c.Type == gjson.String {
		return c.String()
	}
	for _, i := range c.Array() {
		if i.Get("type").String() == "text" {
			return i.Get("text").String()
		}
	}
	return c.Raw
}
