// Package to_ir converts provider-specific API formats into unified format.
package to_ir

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/misc"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// ParseOpenAIRequest parses incoming OpenAI request from client into unified format.
// Automatically detects format: Chat Completions API uses "messages", Responses API uses "input".
// This allows the proxy to accept requests from any OpenAI-compatible client (Cursor, Cline, Codex CLI, etc.)
func ParseOpenAIRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	if err := ir.ValidateJSON(rawJSON); err != nil {
		return nil, err
	}
	root := gjson.ParseBytes(rawJSON)
	req := &ir.UnifiedChatRequest{Model: root.Get("model").String()}

	if v := root.Get("temperature"); v.Exists() {
		f := v.Float()
		req.Temperature = &f
	}
	if v := root.Get("top_p"); v.Exists() {
		f := v.Float()
		req.TopP = &f
	}
	if v := root.Get("top_k"); v.Exists() {
		i := int(v.Int())
		req.TopK = &i
	}
	if v := root.Get("max_tokens"); v.Exists() {
		i := int(v.Int())
		req.MaxTokens = &i
	} else if v := root.Get("max_output_tokens"); v.Exists() {
		i := int(v.Int())
		req.MaxTokens = &i
	}

	if v := root.Get("stop"); v.Exists() {
		if v.IsArray() {
			for _, s := range v.Array() {
				req.StopSequences = append(req.StopSequences, s.String())
			}
		} else {
			req.StopSequences = append(req.StopSequences, v.String())
		}
	}
	if v := root.Get("frequency_penalty"); v.Exists() {
		f := v.Float()
		req.FrequencyPenalty = &f
	}
	if v := root.Get("presence_penalty"); v.Exists() {
		f := v.Float()
		req.PresencePenalty = &f
	}
	if v := root.Get("logprobs"); v.Exists() {
		b := v.Bool()
		req.Logprobs = &b
	}
	if v := root.Get("top_logprobs"); v.Exists() {
		i := int(v.Int())
		req.TopLogprobs = &i
	}
	if v := root.Get("n"); v.Exists() {
		i := int(v.Int())
		req.CandidateCount = &i
	}

	// Auto-detect API format by checking which field exists
	if input := root.Get("input"); input.Exists() && !root.Get("messages").Exists() {
		parseResponsesAPIFields(root, req)
	} else if messages := root.Get("messages"); messages.Exists() && messages.IsArray() {
		for _, m := range messages.Array() {
			req.Messages = append(req.Messages, parseOpenAIMessage(m))
		}
	}

	if tools := root.Get("tools"); tools.Exists() && tools.IsArray() {
		for _, t := range tools.Array() {
			toolType := t.Get("type").String()

			// OpenAI official web search: {"type": "web_search_preview"}
			// Also support variations like "web_search_preview_2025_03_11"
			if strings.HasPrefix(toolType, "web_search") {
				if req.Metadata == nil {
					req.Metadata = make(map[string]any)
				}
				// Map to google_search for Gemini backend
				gsConfig := map[string]any{}
				// Preserve search_context_size if provided
				if scs := t.Get("search_context_size"); scs.Exists() {
					gsConfig["search_context_size"] = scs.String()
				}
				// Preserve user_location if provided
				if ul := t.Get("user_location"); ul.Exists() && ul.IsObject() {
					var ulVal any
					if json.Unmarshal([]byte(ul.Raw), &ulVal) == nil {
						gsConfig["user_location"] = ulVal
					}
				}
				req.Metadata["google_search"] = gsConfig
				continue
			}

			if tool := parseOpenAITool(t); tool != nil {
				req.Tools = append(req.Tools, *tool)
			}
		}
	}

	if v := root.Get("tool_choice"); v.Exists() {
		if v.IsObject() {
			req.ToolChoice = "required"
		} else {
			req.ToolChoice = v.String()
		}
	}
	if v := root.Get("parallel_tool_calls"); v.Exists() {
		b := v.Bool()
		req.ParallelToolCalls = &b
	}
	if mods := root.Get("modalities"); mods.Exists() && mods.IsArray() {
		for _, m := range mods.Array() {
			req.ResponseModality = append(req.ResponseModality, strings.ToUpper(m.String()))
		}
	}
	if imgCfg := root.Get("image_config"); imgCfg.Exists() && imgCfg.IsObject() {
		req.ImageConfig = &ir.ImageConfig{
			AspectRatio: imgCfg.Get("aspect_ratio").String(),
			ImageSize:   imgCfg.Get("image_size").String(),
		}
	}

	req.Thinking = parseThinkingConfig(root)

	if rf := root.Get("response_format"); rf.Exists() {
		if rf.Get("type").String() == "json_schema" {
			if schema := rf.Get("json_schema.schema"); schema.Exists() {
				var schemaMap map[string]any
				if json.Unmarshal([]byte(schema.Raw), &schemaMap) == nil {
					req.ResponseSchema = schemaMap
				}
			}
		} else if rf.Get("type").String() == "json_object" {
			if req.Metadata == nil {
				req.Metadata = make(map[string]any)
			}
			req.Metadata["ollama_format"] = "json"
		}
	}

	if v := root.Get("logit_bias"); v.Exists() && v.IsObject() {
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		var logitBias map[string]any
		if json.Unmarshal([]byte(v.Raw), &logitBias) == nil {
			req.Metadata[ir.MetaOpenAILogitBias] = logitBias
		}
	}
	if v := root.Get("seed"); v.Exists() {
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		req.Metadata[ir.MetaOpenAISeed] = int(v.Int())
	}
	if v := root.Get("user"); v.Exists() && v.String() != "" {
		if req.Metadata == nil {
			req.Metadata = make(map[string]any)
		}
		req.Metadata[ir.MetaOpenAIUser] = v.String()
	}

	// Parse service_tier if present (OpenAI-specific)
	if v := root.Get("service_tier"); v.Exists() && v.String() != "" {
		req.ServiceTier = ir.ServiceTier(v.String())
	}

	return req, nil
}

// parseResponsesAPIFields extracts Responses API specific fields into unified format.
func parseResponsesAPIFields(root gjson.Result, req *ir.UnifiedChatRequest) {
	if v := root.Get("instructions"); v.Exists() && v.String() != "" {
		req.Instructions = v.String()
		req.Messages = append(req.Messages, ir.Message{
			Role: ir.RoleSystem, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: v.String()}},
		})
	}
	if input := root.Get("input"); input.Exists() {
		if input.Type == gjson.String {
			req.Messages = append(req.Messages, ir.Message{
				Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: input.String()}},
			})
		} else if input.IsArray() {
			for _, item := range input.Array() {
				if msg := parseResponsesInputItem(item); msg != nil {
					req.Messages = append(req.Messages, *msg)
				}
			}
		}
	}
	if v := root.Get("previous_response_id"); v.Exists() {
		req.PreviousResponseID = v.String()
	}
	if prompt := root.Get("prompt"); prompt.Exists() && prompt.IsObject() {
		req.PromptID = prompt.Get("id").String()
		req.PromptVersion = prompt.Get("version").String()
		if vars := prompt.Get("variables"); vars.Exists() && vars.IsObject() {
			req.PromptVariables = make(map[string]any)
			vars.ForEach(func(key, value gjson.Result) bool {
				req.PromptVariables[key.String()] = value.Value()
				return true
			})
		}
	}
	if v := root.Get("prompt_cache_key"); v.Exists() {
		req.PromptCacheKey = v.String()
	}
	if v := root.Get("store"); v.Exists() {
		b := v.Bool()
		req.Store = &b
	}
}

func parseResponsesInputItem(item gjson.Result) *ir.Message {
	itemType := item.Get("type").String()
	if itemType == "" && item.Get("role").Exists() {
		itemType = "message"
	}
	switch itemType {
	case "message":
		msg := &ir.Message{Role: ir.MapStandardRole(item.Get("role").String())}
		content := item.Get("content")
		if content.Type == gjson.String {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content.String()})
		} else if content.IsArray() {
			for _, part := range content.Array() {
				if cp := parseResponsesContentPart(part); cp != nil {
					msg.Content = append(msg.Content, *cp)
				}
			}
		}
		return msg
	case "function_call":
		return &ir.Message{
			Role: ir.RoleAssistant,
			ToolCalls: []ir.ToolCall{{
				ID: item.Get("call_id").String(), Name: item.Get("name").String(), Args: item.Get("arguments").String(),
			}},
		}
	case "function_call_output":
		return &ir.Message{
			Role: ir.RoleTool,
			Content: []ir.ContentPart{{
				Type: ir.ContentTypeToolResult,
				ToolResult: &ir.ToolResultPart{
					ToolCallID: item.Get("call_id").String(), Result: item.Get("output").String(),
				},
			}},
		}
	}
	return nil
}

func parseResponsesContentPart(part gjson.Result) *ir.ContentPart {
	switch part.Get("type").String() {
	case "input_text", "output_text", "text":
		if text := part.Get("text").String(); text != "" {
			return &ir.ContentPart{Type: ir.ContentTypeText, Text: text}
		}
	case "input_image":
		if url := part.Get("image_url").String(); url != "" {
			if strings.HasPrefix(url, "data:") {
				return &ir.ContentPart{Type: ir.ContentTypeImage, Image: parseDataURI(url)}
			}
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{URL: url}}
		}
		if fid := part.Get("file_id").String(); fid != "" {
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{Data: fid}}
		}
	case "input_file":
		fp := &ir.FilePart{
			FileID: part.Get("file_id").String(), FileURL: part.Get("file_url").String(),
			Filename: part.Get("filename").String(), FileData: part.Get("file_data").String(),
		}
		if fp.FileID != "" || fp.FileURL != "" || fp.FileData != "" {
			return &ir.ContentPart{Type: ir.ContentTypeFile, File: fp}
		}
	}
	return nil
}

// ParseOpenAIResponse parses non-streaming response FROM OpenAI API into unified format.
// Auto-detects format: Responses API has "output" array, Chat Completions has "choices" array.
func ParseOpenAIResponse(rawJSON []byte) ([]ir.Message, *ir.Usage, error) {
	if err := ir.ValidateJSON(rawJSON); err != nil {
		return nil, nil, err
	}
	root := gjson.ParseBytes(rawJSON)
	usage := ir.ParseOpenAIUsage(root.Get("usage"))

	if output := root.Get("output"); output.Exists() && output.IsArray() {
		return parseResponsesAPIOutput(output, usage)
	}

	message := root.Get("choices.0.message")
	if !message.Exists() {
		return nil, usage, nil
	}
	msg := ir.Message{Role: ir.RoleAssistant}

	rf := ir.ParseReasoningFromJSON(message)
	if rf.Text != "" {
		var sig []byte
		if rf.Signature != "" {
			sig = []byte(rf.Signature)
		}
		msg.Content = append(msg.Content, ir.ContentPart{
			Type:             ir.ContentTypeReasoning,
			Reasoning:        rf.Text,
			ThoughtSignature: sig,
		})
	}
	if content := message.Get("content"); content.Exists() && content.String() != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content.String()})
	}
	msg.ToolCalls = append(msg.ToolCalls, ir.ParseOpenAIStyleToolCalls(message.Get("tool_calls").Array())...)

	// Parse refusal message if model declined to respond
	if refusal := message.Get("refusal").String(); refusal != "" {
		msg.Refusal = refusal
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 && msg.Refusal == "" {
		return nil, usage, nil
	}
	return []ir.Message{msg}, usage, nil
}

func parseResponsesAPIOutput(output gjson.Result, usage *ir.Usage) ([]ir.Message, *ir.Usage, error) {
	var messages []ir.Message
	for _, item := range output.Array() {
		switch item.Get("type").String() {
		case "message":
			msg := ir.Message{Role: ir.RoleAssistant}
			for _, c := range item.Get("content").Array() {
				if c.Get("type").String() == "output_text" {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: c.Get("text").String()})
				}
			}
			// Parse refusal if present in message output
			if refusal := item.Get("refusal").String(); refusal != "" {
				msg.Refusal = refusal
			}
			if len(msg.Content) > 0 || msg.Refusal != "" {
				messages = append(messages, msg)
			}
		case "reasoning":
			msg := ir.Message{Role: ir.RoleAssistant}
			for _, s := range item.Get("summary").Array() {
				if s.Get("type").String() == "summary_text" {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: s.Get("text").String()})
				}
			}
			if len(msg.Content) > 0 {
				messages = append(messages, msg)
			}
		case "function_call":
			messages = append(messages, ir.Message{
				Role: ir.RoleAssistant,
				ToolCalls: []ir.ToolCall{{
					ID: item.Get("call_id").String(), Name: item.Get("name").String(), Args: item.Get("arguments").String(),
				}},
			})
		}
	}
	return messages, usage, nil
}

// SSE prefix constants for zero-allocation comparison
var (
	sseEventPrefix = []byte("event:")
	sseDataPrefix  = []byte("data:")
	sseDone        = []byte("[DONE]")
)

// ParseOpenAIChunk parses streaming SSE chunk FROM OpenAI API into events.
// Handles both formats:
// - Chat Completions: "data: {...}" with choices[].delta
// - Responses API: "event: response.xxx\ndata: {...}" with semantic event types
// Optimized: uses bytes operations to avoid string allocations in hot path.
func ParseOpenAIChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	raw := bytes.TrimSpace(rawJSON)
	if len(raw) == 0 {
		return nil, nil
	}

	var eventType string
	data := raw

	// Parse "event: xxx\ndata: yyy" format (Responses API)
	if bytes.HasPrefix(raw, sseEventPrefix) {
		if idx := bytes.IndexByte(raw, '\n'); idx > 0 {
			eventType = string(bytes.TrimSpace(raw[6:idx])) // Skip "event:"
			data = bytes.TrimSpace(raw[idx+1:])
		}
	}

	// Strip "data:" or "data: " prefix
	if bytes.HasPrefix(data, sseDataPrefix) {
		data = bytes.TrimSpace(data[5:]) // Skip "data:"
	}

	if len(data) == 0 {
		return nil, nil
	}
	if bytes.Equal(data, sseDone) {
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}}, nil
	}
	if !gjson.ValidBytes(data) {
		return nil, nil
	}
	root := gjson.ParseBytes(data)

	if eventType == "" {
		eventType = root.Get("type").String()
	}
	if eventType != "" && strings.HasPrefix(eventType, "response.") {
		return parseResponsesStreamEvent(eventType, root)
	}

	var events []ir.UnifiedEvent
	choice := root.Get("choices.0")
	if !choice.Exists() {
		if u := root.Get("usage"); u.Exists() {
			usage := &ir.Usage{
				PromptTokens: u.Get("prompt_tokens").Int(), CompletionTokens: u.Get("completion_tokens").Int(), TotalTokens: u.Get("total_tokens").Int(),
			}
			if v := u.Get("prompt_tokens_details.cached_tokens"); v.Exists() {
				usage.CachedTokens = v.Int()
			}
			if v := u.Get("prompt_tokens_details.audio_tokens"); v.Exists() {
				usage.AudioTokens = v.Int()
			}
			if v := u.Get("completion_tokens_details.reasoning_tokens"); v.Exists() {
				usage.ThoughtsTokenCount = int32(v.Int())
			}
			if v := u.Get("completion_tokens_details.audio_tokens"); v.Exists() {
				if usage.CompletionTokensDetails == nil {
					usage.CompletionTokensDetails = &ir.CompletionTokensDetails{}
				}
				usage.CompletionTokensDetails.AudioTokens = v.Int()
			}
			if v := u.Get("completion_tokens_details.accepted_prediction_tokens"); v.Exists() {
				usage.AcceptedPredictionTokens = v.Int()
			}
			if v := u.Get("completion_tokens_details.rejected_prediction_tokens"); v.Exists() {
				usage.RejectedPredictionTokens = v.Int()
			}

			events = append(events, ir.UnifiedEvent{
				Type:              ir.EventTypeFinish,
				Usage:             usage,
				SystemFingerprint: root.Get("system_fingerprint").String(),
			})
		}
		return events, nil
	}

	delta := choice.Get("delta")
	if content := delta.Get("content"); content.Exists() && content.String() != "" {
		events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: content.String()})
	}
	if refusal := delta.Get("refusal"); refusal.Exists() && refusal.String() != "" {
		events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Refusal: refusal.String()})
	}
	if rf := ir.ParseReasoningFromJSON(delta); rf.Text != "" {
		var sig []byte
		if rf.Signature != "" {
			sig = []byte(rf.Signature)
		}
		events = append(events, ir.UnifiedEvent{
			Type:             ir.EventTypeReasoning,
			Reasoning:        rf.Text,
			ThoughtSignature: sig,
		})
	}
	for _, tc := range delta.Get("tool_calls").Array() {
		tcIndex := int(tc.Get("index").Int())
		events = append(events, ir.UnifiedEvent{
			Type: ir.EventTypeToolCall,
			ToolCall: &ir.ToolCall{
				ID: tc.Get("id").String(), Name: tc.Get("function.name").String(), Args: tc.Get("function.arguments").String(),
			},
			ToolCallIndex: tcIndex,
		})
	}

	finishReason := choice.Get("finish_reason")
	if finishReason.Exists() && finishReason.String() != "" {
		event := ir.UnifiedEvent{Type: ir.EventTypeFinish, FinishReason: ir.MapOpenAIFinishReason(finishReason.String())}
		if logprobs := choice.Get("logprobs"); logprobs.Exists() {
			event.Logprobs = logprobs.Value()
		}
		if cfr := choice.Get("content_filter_results"); cfr.Exists() {
			event.ContentFilter = cfr.Value()
		}
		event.SystemFingerprint = root.Get("system_fingerprint").String()
		events = append(events, event)
	} else {
		if len(events) > 0 {
			events[0].SystemFingerprint = root.Get("system_fingerprint").String()
			if logprobs := choice.Get("logprobs"); logprobs.Exists() {
				events[0].Logprobs = logprobs.Value()
			}
		}
	}

	return events, nil
}

func parseResponsesStreamEvent(eventType string, root gjson.Result) ([]ir.UnifiedEvent, error) {
	var events []ir.UnifiedEvent
	switch eventType {
	case "response.output_text.delta":
		if delta := root.Get("delta"); delta.Exists() && delta.String() != "" {
			events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: delta.String()})
		}
	case "response.reasoning_summary_text.delta":
		if text := root.Get("text"); text.Exists() && text.String() != "" {
			events = append(events, ir.UnifiedEvent{Type: ir.EventTypeReasoningSummary, ReasoningSummary: text.String()})
		}
	case "response.function_call_arguments.delta":
		if delta := root.Get("delta"); delta.Exists() {
			events = append(events, ir.UnifiedEvent{
				Type:          ir.EventTypeToolCallDelta,
				ToolCall:      &ir.ToolCall{ID: root.Get("item_id").String(), Args: delta.String()},
				ToolCallIndex: int(root.Get("output_index").Int()),
			})
		}
	case "response.function_call_arguments.done":
		events = append(events, ir.UnifiedEvent{
			Type: ir.EventTypeToolCall,
			ToolCall: &ir.ToolCall{
				ID: root.Get("item_id").String(), Name: root.Get("name").String(), Args: root.Get("arguments").String(),
			},
			ToolCallIndex: int(root.Get("output_index").Int()),
		})
	case "response.completed":
		event := ir.UnifiedEvent{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop}
		if u := root.Get("response.usage"); u.Exists() {
			event.Usage = &ir.Usage{
				PromptTokens: u.Get("input_tokens").Int(), CompletionTokens: u.Get("output_tokens").Int(), TotalTokens: u.Get("total_tokens").Int(),
			}
			if v := u.Get("input_tokens_details.cached_tokens"); v.Exists() {
				event.Usage.CachedTokens = v.Int()
			}
			if v := u.Get("output_tokens_details.reasoning_tokens"); v.Exists() {
				event.Usage.ThoughtsTokenCount = int32(v.Int())
			}
		}
		events = append(events, event)
	case "error":
		events = append(events, ir.UnifiedEvent{Type: ir.EventTypeError, FinishReason: ir.FinishReasonError})
	}
	return events, nil
}

func parseOpenAIMessage(m gjson.Result) ir.Message {
	roleStr := m.Get("role").String()
	msg := ir.Message{Role: ir.MapStandardRole(roleStr)}

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

	if roleStr == "assistant" {
		if rf := ir.ParseReasoningFromJSON(m); rf.Text != "" {
			var sig []byte
			if rf.Signature != "" {
				sig = []byte(rf.Signature)
			}
			msg.Content = append(msg.Content, ir.ContentPart{
				Type:             ir.ContentTypeReasoning,
				Reasoning:        rf.Text,
				ThoughtSignature: sig,
			})
		}
	}

	content := m.Get("content")
	if content.Type == gjson.String && roleStr != "tool" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content.String()})
	} else if content.IsArray() {
		for _, item := range content.Array() {
			if part := parseOpenAIContentPart(item, &msg); part != nil {
				msg.Content = append(msg.Content, *part)
			}
		}
	}

	if roleStr == "assistant" {
		for _, tc := range m.Get("tool_calls").Array() {
			if tc.Get("type").String() == "function" {
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
					ID: tc.Get("id").String(), Name: tc.Get("function.name").String(), Args: tc.Get("function.arguments").String(),
				})
			}
		}
	}

	if roleStr == "tool" {
		toolCallID := m.Get("tool_call_id").String()
		if toolCallID == "" {
			toolCallID = m.Get("tool_use_id").String()
		}
		msg.Content = append(msg.Content, ir.ContentPart{
			Type: ir.ContentTypeToolResult,
			ToolResult: &ir.ToolResultPart{
				ToolCallID: toolCallID, Result: ir.SanitizeText(extractContentString(content)),
			},
		})
	}
	return msg
}

func parseOpenAIContentPart(item gjson.Result, msg *ir.Message) *ir.ContentPart {
	switch item.Get("type").String() {
	case "text":
		if text := item.Get("text").String(); text != "" {
			return &ir.ContentPart{Type: ir.ContentTypeText, Text: text}
		}
	case "thinking":
		// Claude Extended Thinking: Parse thinking blocks from history
		if text := item.Get("thinking").String(); text != "" {
			var sig []byte
			if s := item.Get("signature").String(); s != "" {
				sig = []byte(s)
			}
			return &ir.ContentPart{
				Type:             ir.ContentTypeReasoning,
				Reasoning:        text,
				ThoughtSignature: sig,
			}
		}
	case "redacted_thinking":
		// Claude Extended Thinking: Redacted thinking blocks (content hidden but preserved for protocol)
		return &ir.ContentPart{
			Type:             ir.ContentTypeReasoning,
			Reasoning:        "[Redacted]",
			ThoughtSignature: []byte(item.Get("data").String()), // Preserve opaque data if present
		}
	case "image_url":
		if img := parseDataURI(item.Get("image_url.url").String()); img != nil {
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: img}
		}
	case "image":
		mediaType := item.Get("source.media_type").String()
		if mediaType == "" {
			mediaType = "image/png"
		}
		if data := item.Get("source.data").String(); data != "" {
			return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{MimeType: mediaType, Data: data}}
		}
	case "file":
		filename := item.Get("file.filename").String()
		fileData := item.Get("file.file_data").String()
		if filename != "" && fileData != "" {
			ext := ""
			if idx := strings.LastIndex(filename, "."); idx >= 0 && idx < len(filename)-1 {
				ext = filename[idx+1:]
			}
			if mimeType, ok := misc.MimeTypes[ext]; ok {
				return &ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{MimeType: mimeType, Data: fileData}}
			}
		}
	case "tool_use":
		argsRaw := item.Get("input").Raw
		if argsRaw == "" {
			argsRaw = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
			ID: item.Get("id").String(), Name: item.Get("name").String(), Args: argsRaw,
		})
	case "tool_result":
		msg.Role = ir.RoleTool
		return &ir.ContentPart{
			Type: ir.ContentTypeToolResult,
			ToolResult: &ir.ToolResultPart{
				ToolCallID: item.Get("tool_use_id").String(), Result: ir.SanitizeText(extractContentString(item.Get("content"))),
			},
		}
	}
	return nil
}

func parseOpenAITool(t gjson.Result) *ir.ToolDefinition {
	var name, description string
	var paramsResult gjson.Result

	if t.Get("type").String() == "function" {
		fn := t.Get("function")
		name, description, paramsResult = fn.Get("name").String(), fn.Get("description").String(), fn.Get("parameters")
	} else if t.Get("name").Exists() {
		name, description, paramsResult = t.Get("name").String(), t.Get("description").String(), t.Get("input_schema")
	}

	if name == "" {
		return nil
	}

	var params map[string]any
	if paramsResult.Exists() && paramsResult.IsObject() {
		if json.Unmarshal([]byte(paramsResult.Raw), &params) == nil {
			params = ir.CleanJsonSchema(params)
			// Ensure type is "object" - some SDKs send "None" or omit type
			if typeVal, ok := params["type"].(string); !ok || typeVal == "" || typeVal == "None" {
				params["type"] = "object"
			}
		}
	}
	if params == nil {
		params = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return &ir.ToolDefinition{Name: name, Description: description, Parameters: params}
}

func parseThinkingConfig(root gjson.Result) *ir.ThinkingConfig {
	var thinking *ir.ThinkingConfig
	if re := root.Get("reasoning_effort"); re.Exists() {
		thinking = &ir.ThinkingConfig{Effort: ir.ReasoningEffort(re.String())}
		budget, include := ir.MapEffortToBudget(re.String())
		b := int32(budget)
		thinking.ThinkingBudget = &b
		thinking.IncludeThoughts = include
	}
	if reasoning := root.Get("reasoning"); reasoning.Exists() && reasoning.IsObject() {
		if thinking == nil {
			thinking = &ir.ThinkingConfig{}
		}
		if effort := reasoning.Get("effort"); effort.Exists() {
			thinking.Effort = ir.ReasoningEffort(effort.String())
			budget, include := ir.MapEffortToBudget(effort.String())
			b := int32(budget)
			thinking.ThinkingBudget = &b
			thinking.IncludeThoughts = include
		}
		if summary := reasoning.Get("summary"); summary.Exists() {
			thinking.Summary = summary.String()
		}
	}

	// Cherry Studio extension: extra_body.google.thinking_config
	if tc := root.Get("extra_body.google.thinking_config"); tc.Exists() && tc.IsObject() {
		if thinking == nil {
			thinking = &ir.ThinkingConfig{}
		}
		if v := tc.Get("thinkingBudget"); v.Exists() {
			b := int32(v.Int())
			thinking.ThinkingBudget = &b
		} else if v := tc.Get("thinking_budget"); v.Exists() {
			b := int32(v.Int())
			thinking.ThinkingBudget = &b
		}
		if v := tc.Get("includeThoughts"); v.Exists() {
			thinking.IncludeThoughts = v.Bool()
		} else if v := tc.Get("include_thoughts"); v.Exists() {
			thinking.IncludeThoughts = v.Bool()
		}
	}
	return thinking
}

// parseDataURI extracts mime type and base64 data from data URI (format: data:image/png;base64,<data>).
func parseDataURI(url string) *ir.ImagePart {
	if !strings.HasPrefix(url, "data:") {
		return nil
	}
	parts := strings.SplitN(url, ",", 2)
	if len(parts) != 2 {
		return nil
	}
	mime := "image/jpeg"
	if idx := strings.Index(parts[0], ";"); idx > 5 {
		mime = parts[0][5:idx]
	}
	return &ir.ImagePart{MimeType: mime, Data: parts[1]}
}

// extractContentString extracts text from content (string or array of text blocks).
func extractContentString(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	for _, item := range content.Array() {
		if item.Get("type").String() == "text" {
			return item.Get("text").String()
		}
	}
	return content.Raw
}
