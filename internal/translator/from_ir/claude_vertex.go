package from_ir

import (
	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/from_ir/parts"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// coalescedMsg represents a message with coalesced parts for Claude Vertex format.
type coalescedMsg struct {
	role         string
	parts        []any
	cacheControl *ir.CacheControl
}

// ToVertexClaudeRequest converts an IR request to Vertex Claude format with envelope.
// Output format: {"project": "", "model": X, "request": <claude_format_json>}
func ToVertexClaudeRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	innerReq := buildClaudeVertexRequest(req)
	return json.Marshal(map[string]any{"project": "", "model": req.Model, "request": innerReq})
}

// BuildClaudeVertexRequest builds the inner Claude request structure for Vertex.
func BuildClaudeVertexRequest(req *ir.UnifiedChatRequest) map[string]any {
	return buildClaudeVertexRequest(req)
}

func buildClaudeVertexRequest(req *ir.UnifiedChatRequest) map[string]any {
	root := map[string]any{
		"contents": buildClaudeContents(req),
	}

	for _, m := range req.Messages {
		if m.Role == ir.RoleSystem {
			if text := ir.CombineTextParts(m); text != "" {
				root["systemInstruction"] = map[string]any{
					"role":  "user",
					"parts": []any{map[string]any{"text": text}},
				}
				break
			}
		}
	}

	gc := buildClaudeGenerationConfig(req)
	if len(gc) > 0 {
		root["generationConfig"] = gc
	}

	if len(req.Tools) > 0 {
		root["tools"] = buildClaudeTools(req)
	}

	return root
}

func buildClaudeContents(req *ir.UnifiedChatRequest) []any {
	if len(req.Messages) == 0 {
		return nil
	}

	toolIDToName, _ := ir.BuildToolMaps(req.Messages)
	var messages []coalescedMsg

	for i := range req.Messages {
		msg := &req.Messages[i]
		if msg.Role == ir.RoleSystem {
			continue
		}

		var role string
		var msgParts []any

		switch msg.Role {
		case ir.RoleUser:
			role = "user"
			userParts := parts.BuildUserParts(msg.Content)
			toolResultParts := buildClaudeToolResultParts(msg, toolIDToName)
			msgParts = append(userParts, toolResultParts...)
		case ir.RoleAssistant:
			role = "model"
			msgParts = buildClaudeAssistantParts(msg)
		case ir.RoleTool:
			role = "user"
			msgParts = buildClaudeToolResultParts(msg, toolIDToName)
		}

		if len(msgParts) == 0 {
			continue
		}

		if len(messages) > 0 && messages[len(messages)-1].role == role {
			last := &messages[len(messages)-1]
			last.parts = append(last.parts, msgParts...)
			if msg.CacheControl != nil {
				last.cacheControl = msg.CacheControl
			}
		} else {
			messages = append(messages, coalescedMsg{
				role:         role,
				parts:        msgParts,
				cacheControl: msg.CacheControl,
			})
		}
	}

	if len(messages) == 0 {
		return nil
	}

	messages = validateCoalescedToolPairs(messages)

	contents := make([]any, len(messages))
	for i, m := range messages {
		content := map[string]any{"role": m.role, "parts": m.parts}
		// Only add cacheControl if message doesn't have thinking parts
		// Vertex Claude API does not allow cacheControl on contents with thinking
		if m.cacheControl != nil && !hasThinkingParts(m.parts) {
			content["cacheControl"] = buildCacheControlMap(m.cacheControl)
		}
		contents[i] = content
	}

	return contents
}

// validateCoalescedToolPairs ensures each functionCall is immediately followed by its functionResponse.
// This reorders messages to satisfy Claude API's requirement that tool_use must have
// corresponding tool_result in the immediately following message.
func validateCoalescedToolPairs(messages []coalescedMsg) []coalescedMsg {
	toolResults := make(map[string]any)
	for _, msg := range messages {
		for _, part := range msg.parts {
			if m, ok := part.(map[string]any); ok {
				if fr, hasFR := m["functionResponse"].(map[string]any); hasFR {
					if id, ok := fr["id"].(string); ok && id != "" {
						toolResults[id] = part
					}
				}
			}
		}
	}

	if len(toolResults) == 0 {
		return messages
	}

	type flatPart struct {
		role         string
		part         any
		cacheControl *ir.CacheControl
	}
	var flattened []flatPart

	for _, msg := range messages {
		for _, part := range msg.parts {
			flattened = append(flattened, flatPart{role: msg.role, part: part, cacheControl: msg.cacheControl})
		}
	}

	var result []coalescedMsg
	usedResponses := make(map[string]bool)

	for _, fp := range flattened {
		partMap, ok := fp.part.(map[string]any)
		if !ok {
			result = appendOrCoalesce(result, fp.role, fp.part, fp.cacheControl)
			continue
		}

		if _, hasFR := partMap["functionResponse"]; hasFR {
			continue
		}

		if fc, hasFC := partMap["functionCall"].(map[string]any); hasFC {
			result = appendOrCoalesce(result, "model", fp.part, fp.cacheControl)

			if id, ok := fc["id"].(string); ok && id != "" {
				if toolResult, exists := toolResults[id]; exists && !usedResponses[id] {
					result = appendOrCoalesce(result, "user", toolResult, nil)
					usedResponses[id] = true
				}
			}
			continue
		}

		result = appendOrCoalesce(result, fp.role, fp.part, fp.cacheControl)
	}

	filtered := make([]coalescedMsg, 0, len(result))
	for _, m := range result {
		if len(m.parts) > 0 {
			filtered = append(filtered, m)
		}
	}
	return filtered
}

func appendOrCoalesce(msgs []coalescedMsg, role string, part any, cc *ir.CacheControl) []coalescedMsg {
	if len(msgs) > 0 && msgs[len(msgs)-1].role == role {
		last := &msgs[len(msgs)-1]
		last.parts = append(last.parts, part)
		if cc != nil {
			last.cacheControl = cc
		}
		return msgs
	}
	return append(msgs, coalescedMsg{role: role, parts: []any{part}, cacheControl: cc})
}

func buildCacheControlMap(cc *ir.CacheControl) map[string]any {
	result := map[string]any{"type": cc.Type}
	if cc.TTL != nil {
		result["ttl"] = *cc.TTL
	}
	return result
}

// hasThinkingParts checks if any part in the slice is a thinking block.
// Vertex Claude API does not allow cacheControl on contents containing thinking parts.
// We check for:
// - "thought: true" flag
// - "thoughtSignature" field (indicates thinking content)
// - Redacted thinking (parts with "data" field but no "text" field)
func hasThinkingParts(parts []any) bool {
	for _, p := range parts {
		if m, ok := p.(map[string]any); ok {
			// Check for thought=true flag
			if thought, exists := m["thought"]; exists {
				if b, ok := thought.(bool); ok && b {
					return true
				}
			}
			// Also check for thoughtSignature as an indicator
			if _, exists := m["thoughtSignature"]; exists {
				return true
			}
			// Check for redacted thinking (parts with "data" field and no "text" field)
			if _, hasData := m["data"]; hasData {
				if _, hasText := m["text"]; !hasText {
					return true
				}
			}
		}
	}
	return false
}

func buildClaudeAssistantParts(msg *ir.Message) []any {
	var result []any

	for i := range msg.Content {
		cp := &msg.Content[i]
		switch cp.Type {
		case ir.ContentTypeReasoning:
			if cp.Reasoning != "" {
				part := map[string]any{"text": cp.Reasoning, "thought": true}
				if ir.IsValidThoughtSignature(cp.ThoughtSignature) {
					part["thoughtSignature"] = string(cp.ThoughtSignature)
				}
				result = append(result, part)
			}
		case ir.ContentTypeRedactedThinking:
			// Redacted thinking blocks contain encrypted data that must be preserved
			// In Gemini CLI format for Claude, this is represented with a data field
			if cp.RedactedData != "" {
				result = append(result, map[string]any{"data": cp.RedactedData})
			}
		case ir.ContentTypeText:
			if cp.Text != "" {
				result = append(result, map[string]any{"text": cp.Text})
			}
		}
	}

	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]
		id := tc.ID
		if id == "" {
			id = ir.GenToolCallID()
		}
		part := map[string]any{
			"functionCall": map[string]any{
				"name": tc.Name,
				"args": ir.ArgsAsRaw(tc.Args),
				"id":   ir.ToClaudeToolID(id),
			},
		}
		if ir.IsValidThoughtSignature(tc.ThoughtSignature) {
			part["thoughtSignature"] = string(tc.ThoughtSignature)
		}
		result = append(result, part)
	}

	return result
}

func buildClaudeToolResultParts(msg *ir.Message, toolIDToName map[string]string) []any {
	var result []any
	for i := range msg.Content {
		part := &msg.Content[i]
		if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
			tr := part.ToolResult
			name := toolIDToName[tr.ToolCallID]
			if name == "" {
				name = tr.ToolCallID
			}
			resp := map[string]any{"content": tr.Result}
			if tr.IsError {
				resp = map[string]any{"error": tr.Result}
			}
			result = append(result, map[string]any{
				"functionResponse": map[string]any{
					"name":     name,
					"id":       ir.ToClaudeToolID(tr.ToolCallID),
					"response": resp,
				},
			})
		}
	}
	return result
}

func buildClaudeGenerationConfig(req *ir.UnifiedChatRequest) map[string]any {
	gc := make(map[string]any)

	if req.Temperature != nil {
		gc["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		gc["topP"] = *req.TopP
	}
	if req.TopK != nil {
		gc["topK"] = *req.TopK
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		gc["maxOutputTokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		gc["stopSequences"] = req.StopSequences
	}

	if req.Thinking != nil && req.Thinking.IncludeThoughts {
		tc := map[string]any{"includeThoughts": true}
		if req.Thinking.ThinkingBudget != nil && *req.Thinking.ThinkingBudget > 0 {
			tc["thinkingBudget"] = *req.Thinking.ThinkingBudget
		}
		gc["thinkingConfig"] = tc
	}

	return gc
}

func buildClaudeTools(req *ir.UnifiedChatRequest) []any {
	var funcs []any
	for _, t := range req.Tools {
		params := ir.CleanJsonSchemaForGemini(ir.CopyMap(t.Parameters))
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		funcs = append(funcs, map[string]any{
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		})
	}
	return []any{map[string]any{"functionDeclarations": funcs}}
}

// VertexEnvelopeProvider wraps requests for Vertex AI with the envelope format.
// For Claude models: uses Gemini CLI format (contents/functionCall/functionResponse)
// For Gemini models: uses native Gemini format
type VertexEnvelopeProvider struct{}

// ConvertRequest converts an IR request to the Vertex envelope format.
func (p *VertexEnvelopeProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	if ir.IsClaudeModel(req.Model) {
		return ToVertexClaudeRequest(req)
	}
	// For Gemini models, use native Gemini format wrapped in envelope
	geminiJSON, err := (&GeminiProvider{}).ConvertRequest(req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"project": "", "model": req.Model, "request": json.RawMessage(geminiJSON)})
}
