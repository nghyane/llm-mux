package from_ir

import (
	"fmt"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
)

func ToOllamaRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	if req.Metadata != nil {
		if ep, ok := req.Metadata["ollama_endpoint"].(string); ok && ep == "generate" {
			return convertToOllamaGenerateRequest(req)
		}
	}
	return convertToOllamaChatRequest(req)
}

func convertToOllamaChatRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]any{"model": req.Model, "messages": []any{}, "stream": req.Metadata["stream"] == true, "options": buildOllamaOptions(req)}
	for _, msg := range req.Messages {
		if mo := convertMessageToOllama(msg); mo != nil {
			m["messages"] = append(m["messages"].([]any), mo)
		}
	}
	if len(req.Tools) > 0 {
		var tools []any
		for _, t := range req.Tools {
			ps := t.Parameters
			if ps == nil {
				ps = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": t.Name, "description": t.Description, "parameters": ps}})
		}
		m["tools"] = tools
	}
	if req.ResponseSchema != nil {
		m["format"] = req.ResponseSchema
	} else if fmt, ok := req.Metadata["ollama_format"].(string); ok && fmt != "" {
		m["format"] = fmt
	}
	if ka, ok := req.Metadata["ollama_keep_alive"].(string); ok && ka != "" {
		m["keep_alive"] = ka
	}
	return json.Marshal(m)
}

func convertToOllamaGenerateRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	m := map[string]any{"model": req.Model, "prompt": "", "stream": req.Metadata["stream"] == true, "options": buildOllamaOptions(req)}
	var sp, up string
	var imgs []string
	for _, msg := range req.Messages {
		switch msg.Role {
		case ir.RoleSystem:
			sp = ir.CombineTextParts(msg)
		case ir.RoleUser:
			up = ir.CombineTextParts(msg)
			for _, p := range msg.Content {
				if p.Type == ir.ContentTypeImage && p.Image != nil {
					imgs = append(imgs, p.Image.Data)
				}
			}
		}
	}
	if sp != "" {
		m["system"] = sp
	}
	if up != "" {
		m["prompt"] = up
	}
	if len(imgs) > 0 {
		m["images"] = imgs
	}
	if req.ResponseSchema != nil {
		m["format"] = req.ResponseSchema
	} else if fmt, ok := req.Metadata["ollama_format"].(string); ok && fmt != "" {
		m["format"] = fmt
	}
	if ka, ok := req.Metadata["ollama_keep_alive"].(string); ok && ka != "" {
		m["keep_alive"] = ka
	}
	return json.Marshal(m)
}

func buildOllamaOptions(req *ir.UnifiedChatRequest) map[string]any {
	o := make(map[string]any)
	if req.Temperature != nil {
		o["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		o["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		o["top_k"] = *req.TopK
	}
	if req.MaxTokens != nil {
		o["num_predict"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		o["stop"] = req.StopSequences
	}
	if req.Metadata != nil {
		if v, ok := req.Metadata["ollama_seed"].(int64); ok {
			o["seed"] = v
		}
		if v, ok := req.Metadata["ollama_num_ctx"].(int64); ok {
			o["num_ctx"] = v
		}
	}
	return o
}

func convertMessageToOllama(m ir.Message) map[string]any {
	switch m.Role {
	case ir.RoleSystem:
		if t := ir.CombineTextParts(m); t != "" {
			return map[string]any{"role": "system", "content": t}
		}
	case ir.RoleUser:
		return buildOllamaUserMessage(m)
	case ir.RoleAssistant:
		return buildOllamaAssistantMessage(m)
	case ir.RoleTool:
		return buildOllamaToolMessage(m)
	}
	return nil
}

func buildOllamaUserMessage(m ir.Message) map[string]any {
	var t string
	var imgs []string
	for _, p := range m.Content {
		switch p.Type {
		case ir.ContentTypeText:
			t += p.Text
		case ir.ContentTypeImage:
			if p.Image != nil {
				imgs = append(imgs, p.Image.Data)
			}
		}
	}
	res := map[string]any{"role": "user"}
	if t != "" {
		res["content"] = t
	}
	if len(imgs) > 0 {
		res["images"] = imgs
	}
	if t == "" && len(imgs) == 0 {
		return nil
	}
	return res
}

func buildOllamaAssistantMessage(m ir.Message) map[string]any {
	res := map[string]any{"role": "assistant"}
	t, r := ir.CombineTextAndReasoning(m)
	if t != "" {
		res["content"] = t
	}
	if r != "" {
		res["thinking"] = r
	}
	if len(m.ToolCalls) > 0 {
		tcs := make([]any, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			tcs[i] = map[string]any{"id": tc.ID, "type": "function", "function": map[string]any{"name": tc.Name, "arguments": tc.Args}}
		}
		res["tool_calls"] = tcs
	}
	return res
}

func buildOllamaToolMessage(m ir.Message) map[string]any {
	for _, p := range m.Content {
		if p.Type == ir.ContentTypeToolResult && p.ToolResult != nil {
			return map[string]any{"role": "tool", "tool_call_id": p.ToolResult.ToolCallID, "content": p.ToolResult.Result}
		}
	}
	return nil
}

func ToOllamaChatResponse(ms []ir.Message, us *ir.Usage, model string) ([]byte, error) {
	b := ir.NewResponseBuilder(ms, us, model, false)
	res := map[string]any{"model": model, "created_at": time.Now().UTC().Format(time.RFC3339), "done": true, "message": map[string]any{"role": "assistant", "content": ""}}
	if m := b.GetLastMessage(); m != nil {
		mc := res["message"].(map[string]any)
		mc["role"] = string(m.Role)
		if t := b.GetTextContent(); t != "" {
			mc["content"] = t
		}
		if r := b.GetReasoningContent(); r != "" {
			mc["thinking"] = r
		}
		if tcs := b.BuildOpenAIToolCalls(); tcs != nil {
			mc["tool_calls"], res["done_reason"] = tcs, "tool_calls"
		} else {
			res["done_reason"] = "stop"
		}
	}
	if us != nil {
		res["prompt_eval_count"], res["eval_count"] = us.PromptTokens, us.CompletionTokens
		res["total_duration"], res["load_duration"], res["prompt_eval_duration"], res["eval_duration"] = 0, 0, 0, 0
	}
	return json.Marshal(res)
}

func ToOllamaGenerateResponse(ms []ir.Message, us *ir.Usage, model string) ([]byte, error) {
	b := ir.NewResponseBuilder(ms, us, model, false)
	res := map[string]any{"model": model, "created_at": time.Now().UTC().Format(time.RFC3339), "done": true, "response": "", "done_reason": "stop"}
	if t := b.GetTextContent(); t != "" {
		res["response"] = t
	}
	if r := b.GetReasoningContent(); r != "" {
		res["thinking"] = r
	}
	if us != nil {
		res["prompt_eval_count"], res["eval_count"] = us.PromptTokens, us.CompletionTokens
		res["total_duration"], res["load_duration"], res["prompt_eval_duration"], res["eval_duration"] = 0, 0, 0, 0
	}
	return json.Marshal(res)
}

func ToOllamaChatChunk(ev ir.UnifiedEvent, model string) ([]byte, error) {
	if ev.Type == ir.EventTypeStreamMeta {
		return nil, nil
	}
	res := map[string]any{"model": model, "created_at": time.Now().UTC().Format(time.RFC3339), "done": false, "message": map[string]any{"role": "assistant", "content": ""}}
	switch ev.Type {
	case ir.EventTypeToken:
		res["message"].(map[string]any)["content"] = ev.Content
	case ir.EventTypeReasoning:
		res["message"].(map[string]any)["thinking"] = ev.Reasoning
	case ir.EventTypeToolCall:
		if ev.ToolCall != nil {
			res["message"].(map[string]any)["tool_calls"] = []any{map[string]any{"id": ev.ToolCall.ID, "type": "function", "function": map[string]any{"name": ev.ToolCall.Name, "arguments": ev.ToolCall.Args}}}
		}
	case ir.EventTypeFinish:
		res["done"], res["done_reason"] = true, mapFinishReasonToOllama(ev.FinishReason)
		if ev.Usage != nil {
			res["prompt_eval_count"], res["eval_count"] = ev.Usage.PromptTokens, ev.Usage.CompletionTokens
			res["total_duration"], res["load_duration"], res["prompt_eval_duration"], res["eval_duration"] = 0, 0, 0, 0
		}
	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", ev.Error)
	}
	jb, _ := json.Marshal(res)
	return append(jb, '\n'), nil
}

func ToOllamaGenerateChunk(ev ir.UnifiedEvent, model string) ([]byte, error) {
	if ev.Type == ir.EventTypeStreamMeta {
		return nil, nil
	}
	res := map[string]any{"model": model, "created_at": time.Now().UTC().Format(time.RFC3339), "done": false, "response": ""}
	switch ev.Type {
	case ir.EventTypeToken:
		res["response"] = ev.Content
	case ir.EventTypeReasoning:
		res["thinking"] = ev.Reasoning
	case ir.EventTypeFinish:
		res["done"], res["done_reason"] = true, mapFinishReasonToOllama(ev.FinishReason)
		if ev.Usage != nil {
			res["prompt_eval_count"], res["eval_count"] = ev.Usage.PromptTokens, ev.Usage.CompletionTokens
			res["total_duration"], res["load_duration"], res["prompt_eval_duration"], res["eval_duration"] = 0, 0, 0, 0
		}
	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", ev.Error)
	}
	jb, _ := json.Marshal(res)
	return append(jb, '\n'), nil
}

func OpenAIToOllamaChat(rj []byte, m string) ([]byte, error) {
	ms, us, err := to_ir.ParseOpenAIResponse(rj)
	if err != nil {
		return nil, err
	}
	return ToOllamaChatResponse(ms, us, m)
}

func OpenAIToOllamaGenerate(rj []byte, m string) ([]byte, error) {
	ms, us, err := to_ir.ParseOpenAIResponse(rj)
	if err != nil {
		return nil, err
	}
	return ToOllamaGenerateResponse(ms, us, m)
}

func OpenAIChunkToOllamaChat(rj []byte, m string) ([]byte, error) {
	evs, err := to_ir.ParseOpenAIChunk(rj)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	return ToOllamaChatChunk(evs[0], m)
}

func OpenAIChunkToOllamaGenerate(rj []byte, m string) ([]byte, error) {
	evs, err := to_ir.ParseOpenAIChunk(rj)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	return ToOllamaGenerateChunk(evs[0], m)
}

func mapFinishReasonToOllama(r ir.FinishReason) string {
	switch r {
	case ir.FinishReasonMaxTokens:
		return "length"
	case ir.FinishReasonToolCalls:
		return "tool_calls"
	default:
		return "stop"
	}
}

func ToOllamaShowResponse(mn string) []byte {
	cl, mt, ar := 128000, 16384, "transformer"
	if info := findModelInfoByName(mn); info != nil {
		if info.Type != "" {
			ar = info.Type
		}
		if info.ContextLength > 0 {
			cl = info.ContextLength
		} else if info.InputTokenLimit > 0 {
			cl = info.InputTokenLimit
		}
		if info.MaxCompletionTokens > 0 {
			mt = info.MaxCompletionTokens
		} else if info.OutputTokenLimit > 0 {
			mt = info.OutputTokenLimit
		}
	}
	res := map[string]any{"license": "", "modelfile": "# Modelfile for " + mn + "\nFROM " + mn, "parameters": fmt.Sprintf("num_ctx %d\nnum_predict %d\ntemperature 0.7\ntop_p 0.9", cl, mt), "template": "{{ if .System }}{{ .System }}\n{{ end }}{{ .Prompt }}", "details": map[string]any{"parent_model": "", "format": "gguf", "family": "Ollama", "families": []string{"Ollama"}, "parameter_size": "0B", "quantization_level": "Q4_K_M"}, "model_info": map[string]any{"general.architecture": ar, "general.basename": mn, "general.file_type": 2, "general.parameter_count": 0, "general.quantization_version": 2, "general.context_length": cl, "llama.context_length": cl, "llama.rope.freq_base": 10000.0, ar + ".context_length": cl}, "capabilities": []string{"tools", "vision", "completion"}}
	jb, _ := json.Marshal(res)
	return jb
}

func findModelInfoByName(mn string) *registry.ModelInfo {
	reg := registry.GetGlobalRegistry()
	if info := reg.GetModelInfo(mn); info != nil {
		return info
	}
	for _, m := range reg.GetAvailableModels("openai") {
		id, _ := m["id"].(string)
		cid := id
		if idx := strings.Index(id, "] "); idx != -1 {
			cid = id[idx+2:]
		}
		if strings.EqualFold(cid, mn) {
			info := &registry.ModelInfo{ID: cid}
			if v, ok := m["type"].(string); ok {
				info.Type = v
			}
			if v, ok := m["context_length"].(int); ok {
				info.ContextLength = v
			}
			if v, ok := m["max_completion_tokens"].(int); ok {
				info.MaxCompletionTokens = v
			}
			return info
		}
	}
	return nil
}
