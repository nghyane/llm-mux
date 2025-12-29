package to_ir

import (
	"strings"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

func ParseOllamaRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	root, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, err
	}

	req := &ir.UnifiedChatRequest{
		Model:    root.Get("model").String(),
		Metadata: make(map[string]any, 4),
	}

	if opts := root.Get("options"); opts.IsObject() {
		if v := opts.Get("temperature"); v.Exists() {
			req.Temperature = ir.Ptr(v.Float())
		}
		if v := opts.Get("top_p"); v.Exists() {
			req.TopP = ir.Ptr(v.Float())
		}
		if v := opts.Get("top_k"); v.Exists() {
			req.TopK = ir.Ptr(int(v.Int()))
		}
		if v := opts.Get("num_predict"); v.Exists() {
			req.MaxTokens = ir.Ptr(int(v.Int()))
		}
		if v := opts.Get("stop"); v.Exists() {
			if v.IsArray() {
				for _, s := range v.Array() {
					req.StopSequences = append(req.StopSequences, s.String())
				}
			} else {
				req.StopSequences = []string{v.String()}
			}
		}
		if v := opts.Get("seed"); v.Exists() {
			req.Metadata["ollama_seed"] = v.Int()
		}
		if v := opts.Get("num_ctx"); v.Exists() {
			req.Metadata["ollama_num_ctx"] = v.Int()
		}
	}

	if msgs := root.Get("messages"); msgs.IsArray() {
		req.Metadata["ollama_endpoint"] = "chat"
		for _, m := range msgs.Array() {
			msg := ir.Message{Role: ir.MapStandardRole(m.Get("role").String())}
			content := m.Get("content").String()
			images := m.Get("images")

			if images.IsArray() && len(images.Array()) > 0 {
				if content != "" {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content})
				}
				for _, img := range images.Array() {
					if part := parseOllamaImage(img.String()); part != nil {
						msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: part})
					}
				}
			} else if content != "" {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content})
			}

			if msg.Role == ir.RoleAssistant {
				msg.ToolCalls = ir.ParseOpenAIStyleToolCalls(m.Get("tool_calls").Array())
			}
			if msg.Role == ir.RoleTool {
				id := m.Get("tool_call_id").String()
				if id == "" {
					id = m.Get("tool_name").String()
				}
				if id != "" {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeToolResult, ToolResult: &ir.ToolResultPart{ToolCallID: id, Result: ir.SanitizeText(content)}})
				}
			}
			if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 {
				req.Messages = append(req.Messages, msg)
			}
		}
	} else if prompt := root.Get("prompt"); prompt.Exists() {
		req.Metadata["ollama_endpoint"] = "generate"
		msg := ir.Message{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: prompt.String()}}}
		for _, img := range root.Get("images").Array() {
			if part := parseOllamaImage(img.String()); part != nil {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: part})
			}
		}
		req.Messages = []ir.Message{msg}
	}

	if sys := root.Get("system").String(); sys != "" {
		req.Messages = append([]ir.Message{{Role: ir.RoleSystem, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: sys}}}}, req.Messages...)
	}

	for _, t := range root.Get("tools").Array() {
		if t.Get("type").String() == "function" {
			fn := t.Get("function")
			if name := fn.Get("name").String(); name != "" {
				var params map[string]any
				if p := fn.Get("parameters"); p.IsObject() {
					json.Unmarshal([]byte(p.Raw), &params)
					params = ir.CleanJsonSchema(params)
				}
				if params == nil {
					params = make(map[string]any)
				}
				req.Tools = append(req.Tools, ir.ToolDefinition{Name: name, Description: fn.Get("description").String(), Parameters: params})
			}
		}
	}

	for _, k := range []string{"format", "keep_alive"} {
		if v := root.Get(k); v.Exists() {
			req.Metadata["ollama_"+k] = v.Value()
		}
	}
	if v := root.Get("stream"); v.Exists() {
		req.Metadata["stream"] = v.Bool()
	}

	return req, nil
}

func parseOllamaImage(data string) *ir.ImagePart {
	if data == "" {
		return nil
	}
	if !strings.HasPrefix(data, "data:") {
		data = "data:image/png;base64," + data
	}
	p := strings.SplitN(data, ",", 2)
	if len(p) != 2 {
		return nil
	}
	mime := "image/png"
	if i := strings.Index(p[0], ";"); i > 5 {
		mime = p[0][5:i]
	}
	return &ir.ImagePart{MimeType: mime, Data: p[1]}
}
