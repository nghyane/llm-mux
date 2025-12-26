// Package to_ir converts provider-specific API formats into unified format.
package to_ir

import (
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// ParseOllamaRequest parses incoming Ollama API request into unified format.
// Supports both /api/chat and /api/generate endpoints.
func ParseOllamaRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	root, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, err
	}

	req := &ir.UnifiedChatRequest{
		Model:    root.Get("model").String(),
		Metadata: make(map[string]any, 4), // Pre-allocate for common metadata
	}

	// Parse options (temperature, top_p, etc.)
	parseOllamaOptions(root.Get("options"), req)

	// Determine endpoint type and parse messages
	if msgs := root.Get("messages"); msgs.Exists() && msgs.IsArray() {
		// /api/chat endpoint
		req.Messages = parseOllamaMessages(msgs.Array())
		req.Metadata["ollama_endpoint"] = "chat"
	} else if prompt := root.Get("prompt"); prompt.Exists() {
		// /api/generate endpoint
		req.Messages = []ir.Message{createOllamaUserMessage(prompt.String(), root.Get("images"))}
		req.Metadata["ollama_endpoint"] = "generate"
	}

	// System prompt (override or prepend)
	if sys := root.Get("system"); sys.Exists() && sys.String() != "" {
		req.Messages = append([]ir.Message{{
			Role:    ir.RoleSystem,
			Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: sys.String()}},
		}}, req.Messages...)
	}

	// Tools
	if tools := root.Get("tools"); tools.IsArray() {
		for _, t := range tools.Array() {
			if tool := parseOllamaTool(t); tool != nil {
				req.Tools = append(req.Tools, *tool)
			}
		}
	}

	// Metadata fields
	for _, key := range []string{"format", "keep_alive"} {
		if val := root.Get(key); val.Exists() {
			req.Metadata["ollama_"+key] = val.String()
		}
	}
	if stream := root.Get("stream"); stream.Exists() {
		req.Metadata["stream"] = stream.Bool()
	}

	return req, nil
}

func parseOllamaOptions(opts gjson.Result, req *ir.UnifiedChatRequest) {
	if !opts.Exists() {
		return
	}
	if v := opts.Get("temperature"); v.Exists() {
		f := v.Float()
		req.Temperature = &f
	}
	if v := opts.Get("top_p"); v.Exists() {
		f := v.Float()
		req.TopP = &f
	}
	if v := opts.Get("top_k"); v.Exists() {
		i := int(v.Int())
		req.TopK = &i
	}
	if v := opts.Get("num_predict"); v.Exists() {
		i := int(v.Int())
		req.MaxTokens = &i
	}
	if v := opts.Get("stop"); v.Exists() {
		if v.IsArray() {
			for _, s := range v.Array() {
				req.StopSequences = append(req.StopSequences, s.String())
			}
		} else {
			req.StopSequences = append(req.StopSequences, v.String())
		}
	}
	// Metadata options
	if v := opts.Get("seed"); v.Exists() {
		req.Metadata["ollama_seed"] = v.Int()
	}
	if v := opts.Get("num_ctx"); v.Exists() {
		req.Metadata["ollama_num_ctx"] = v.Int()
	}
}

func parseOllamaMessages(msgs []gjson.Result) []ir.Message {
	var res []ir.Message
	for _, m := range msgs {
		msg := ir.Message{Role: ir.MapStandardRole(m.Get("role").String())}

		// Handle content and images
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

		// Tool calls
		if msg.Role == ir.RoleAssistant {
			msg.ToolCalls = ir.ParseOpenAIStyleToolCalls(m.Get("tool_calls").Array())
		}
		// Tool results
		if msg.Role == ir.RoleTool {
			id := m.Get("tool_call_id").String()
			if id == "" {
				id = m.Get("tool_name").String()
			} // Fallback
			if id != "" {
				msg.Content = append(msg.Content, ir.ContentPart{
					Type:       ir.ContentTypeToolResult,
					ToolResult: &ir.ToolResultPart{ToolCallID: id, Result: ir.SanitizeText(content)},
				})
			}
		}

		if len(msg.Content) > 0 || len(msg.ToolCalls) > 0 {
			res = append(res, msg)
		}
	}
	return res
}

func createOllamaUserMessage(prompt string, images gjson.Result) ir.Message {
	msg := ir.Message{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: prompt}}}
	if images.IsArray() {
		for _, img := range images.Array() {
			if part := parseOllamaImage(img.String()); part != nil {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: part})
			}
		}
	}
	return msg
}

func parseOllamaTool(t gjson.Result) *ir.ToolDefinition {
	if t.Get("type").String() != "function" {
		return nil
	}
	fn := t.Get("function")
	name := fn.Get("name").String()
	if name == "" {
		return nil
	}

	var params map[string]any
	if p := fn.Get("parameters"); p.Exists() {
		json.Unmarshal([]byte(p.Raw), &params)
		params = ir.CleanJsonSchema(params)
	}
	if params == nil {
		params = make(map[string]any)
	}

	return &ir.ToolDefinition{
		Name:        name,
		Description: fn.Get("description").String(),
		Parameters:  params,
	}
}

func parseOllamaImage(data string) *ir.ImagePart {
	if data == "" {
		return nil
	}
	if !strings.HasPrefix(data, "data:") {
		data = "data:image/png;base64," + data
	}
	parts := strings.SplitN(data, ",", 2)
	if len(parts) != 2 {
		return nil
	}

	mime := "image/png"
	if idx := strings.Index(parts[0], ";"); idx > 5 {
		mime = parts[0][5:idx]
	}
	return &ir.ImagePart{MimeType: mime, Data: parts[1]}
}
