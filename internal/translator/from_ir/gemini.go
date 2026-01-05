package from_ir

import (
	"fmt"
	"time"

	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/from_ir/parts"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
)

type GeminiProvider struct{}

func (p *GeminiProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	root := map[string]any{"contents": []any{}}
	if err := p.applyMessages(root, req); err != nil {
		return nil, err
	}
	if err := p.applyGenerationConfig(root, req); err != nil {
		return nil, err
	}
	if err := p.applyTools(root, req); err != nil {
		return nil, err
	}
	p.applySafetySettings(root, req)

	if req.Metadata != nil {
		if v, ok := req.Metadata[ir.MetaGeminiCachedContent]; ok {
			root["cachedContent"] = v
		}
		if v, ok := req.Metadata[ir.MetaGeminiLabels]; ok {
			root["labels"] = v
		}
	}

	if req.Model == "gemini-2.5-flash-image-preview" && req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" {
		p.fixImageAspectRatioForPreview(root, req.ImageConfig.AspectRatio)
	}
	return json.Marshal(root)
}

func (p *GeminiProvider) applyGenerationConfig(root map[string]any, req *ir.UnifiedChatRequest) error {
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
	if req.FrequencyPenalty != nil {
		gc["frequencyPenalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		gc["presencePenalty"] = *req.PresencePenalty
	}
	if req.Logprobs != nil && *req.Logprobs {
		gc["responseLogprobs"] = true
		if req.TopLogprobs != nil {
			gc["logprobs"] = *req.TopLogprobs
		}
	}
	if req.CandidateCount != nil && *req.CandidateCount > 1 {
		gc["candidateCount"] = *req.CandidateCount
	}

	p.applyThinkingConfig(gc, req, ir.IsGemini3(req.Model))

	if len(req.ResponseModality) > 0 {
		gc["responseModalities"] = req.ResponseModality
	}
	if req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" && req.Model != "gemini-2.5-flash-image-preview" {
		gc["imageConfig"] = map[string]any{"aspectRatio": req.ImageConfig.AspectRatio, "imageSize": req.ImageConfig.ImageSize}
	}
	if req.ResponseSchema != nil {
		gc["responseMimeType"] = "application/json"
		gc["responseJsonSchema"] = req.ResponseSchema
	}

	if req.FunctionCalling != nil {
		fc := map[string]any{}
		if req.FunctionCalling.Mode != "" {
			fc["mode"] = req.FunctionCalling.Mode
		}
		if len(req.FunctionCalling.AllowedFunctionNames) > 0 {
			fc["allowedFunctionNames"] = req.FunctionCalling.AllowedFunctionNames
		}
		if req.FunctionCalling.StreamFunctionCallArguments {
			fc["streamFunctionCallArguments"] = true
		}
		if len(fc) > 0 {
			root["toolConfig"] = map[string]any{"functionCallingConfig": fc}
		}
	}

	currentMax := 0
	switch v := gc["maxOutputTokens"].(type) {
	case int:
		currentMax = v
	case int32:
		currentMax = int(v)
	case int64:
		currentMax = int(v)
	case float64:
		currentMax = int(v)
	}
	if currentMax < 1 {
		gc["maxOutputTokens"] = ir.DefaultMaxOutputTokens
	}

	root["generationConfig"] = gc
	return nil
}

func (p *GeminiProvider) applyMessages(root map[string]any, req *ir.UnifiedChatRequest) error {
	if len(req.Messages) == 0 {
		return nil
	}
	toolIDToName, toolResults := ir.BuildToolMaps(req.Messages)
	coalescer := ir.GetContentCoalescer(len(req.Messages) * 2)

	for i := range req.Messages {
		msg := &req.Messages[i]
		switch msg.Role {
		case ir.RoleSystem:
			if text := p.extractSystemText(msg); text != "" {
				root["systemInstruction"] = map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}}
			}
		case ir.RoleUser:
			coalescer.Emit("user", parts.BuildUserParts(msg.Content))
		case ir.RoleAssistant:
			modelParts, responseParts := p.buildAssistantAndToolParts(msg, toolIDToName, toolResults, req.Model)
			coalescer.Emit("model", modelParts)
			coalescer.Emit("user", responseParts)
		}
	}
	contents := coalescer.Build()
	ir.PutContentCoalescer(coalescer)
	if contents != nil {
		root["contents"] = contents
	}
	return nil
}

func (p *GeminiProvider) extractSystemText(msg *ir.Message) string {
	for i := range msg.Content {
		if msg.Content[i].Type == ir.ContentTypeText {
			return msg.Content[i].Text
		}
	}
	return ""
}

func (p *GeminiProvider) buildAssistantAndToolParts(msg *ir.Message, toolIDToName map[string]string, toolResults map[string]*ir.ToolResultPart, model string) (modelParts, responseParts []any) {
	for i := range msg.Content {
		cp := &msg.Content[i]
		part := map[string]any{}
		if cp.Type == ir.ContentTypeReasoning && cp.Reasoning != "" {
			part = map[string]any{"text": cp.Reasoning, "thought": true}
		} else if cp.Type == ir.ContentTypeText && cp.Text != "" {
			part = map[string]any{"text": cp.Text}
		}
		if len(part) > 0 {
			if ir.IsValidThoughtSignature(cp.ThoughtSignature) {
				part["thoughtSignature"] = string(cp.ThoughtSignature)
			}
			modelParts = append(modelParts, part)
		}
	}

	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]
		id := tc.ID
		if id == "" {
			id = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
		}
		part := map[string]any{"functionCall": map[string]any{"name": tc.Name, "args": json.RawMessage(ir.ValidateAndNormalizeJSON(tc.Args)), "id": id}}
		if ir.IsValidThoughtSignature(tc.ThoughtSignature) {
			part["thoughtSignature"] = string(tc.ThoughtSignature)
		} else if ir.IsGemini3(model) {
			part["thoughtSignature"] = ir.DummyThoughtSignature
		}
		modelParts = append(modelParts, part)

		if name, ok := toolIDToName[id]; ok {
			if res, ok := toolResults[id]; ok {
				responseParts = append(responseParts, map[string]any{"functionResponse": map[string]any{"name": name, "id": id, "response": buildFunctionResponseObject(res.Result, res.IsError)}})
				for _, img := range res.Images {
					if img.Data != "" {
						responseParts = append(responseParts, map[string]any{"inlineData": map[string]any{"mimeType": img.MimeType, "data": img.Data}})
					} else if img.URL != "" {
						responseParts = append(responseParts, map[string]any{"fileData": map[string]any{"fileUri": img.URL, "mimeType": img.MimeType}})
					}
				}
			}
		}
	}
	return
}

func (p *GeminiProvider) applyTools(root map[string]any, req *ir.UnifiedChatRequest) error {
	tn := make(map[string]any)
	hasFunctions := len(req.Tools) > 0

	if req.Metadata != nil {
		for k, meta := range map[string]string{ir.MetaGoogleSearch: "googleSearch", ir.MetaGoogleSearchRetrieval: "googleSearchRetrieval", ir.MetaCodeExecution: "codeExecution", ir.MetaURLContext: "urlContext", ir.MetaFileSearch: "fileSearch"} {
			if v, ok := req.Metadata[k]; ok {
				// Gemini API does not support mixing googleSearch/googleSearchRetrieval with functionDeclarations.
				// When both are present, prioritize functionDeclarations (custom tools).
				if hasFunctions && (k == ir.MetaGoogleSearch || k == ir.MetaGoogleSearchRetrieval) {
					continue
				}
				if m, ok := v.(map[string]any); ok {
					cleaned := map[string]any{}
					for mk, mv := range m {
						if mk != "_original_type" && mk != "max_uses" && mk != "container" && mk != "vector_store" && mk != "max_num_results" && mk != "ranking_options" {
							cleaned[mk] = mv
						}
					}
					tn[meta] = cleaned
				} else {
					tn[meta] = v
				}
			}
		}
	}

	if hasFunctions {
		funcs := make([]any, len(req.Tools))
		for i, t := range req.Tools {
			params := ir.CleanJsonSchemaForGemini(ir.CopyMap(t.Parameters))
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			} else {
				if tv, ok := params["type"].(string); !ok || tv == "" || tv == "None" {
					params["type"] = "object"
				}
				if params["properties"] == nil {
					params["properties"] = map[string]any{}
				}
			}
			funcs[i] = map[string]any{"name": t.Name, "description": t.Description, "parameters": params}
		}
		tn["functionDeclarations"] = funcs
	}

	if len(tn) == 0 {
		return nil
	}
	root["tools"] = []any{tn}

	if len(req.Tools) > 0 {
		mode, allowed := "AUTO", []string(nil)
		switch req.ToolChoice {
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		case "validated":
			mode = "VALIDATED"
		case "function":
			if req.ToolChoiceFunction != "" {
				mode, allowed = "ANY", []string{req.ToolChoiceFunction}
			}
		}
		fc := map[string]any{"mode": mode}
		if len(allowed) > 0 {
			fc["allowedFunctionNames"] = allowed
		}
		root["toolConfig"] = map[string]any{"functionCallingConfig": fc}
	}
	return nil
}

func (p *GeminiProvider) applySafetySettings(root map[string]any, req *ir.UnifiedChatRequest) {
	if len(req.SafetySettings) > 0 {
		s := make([]any, len(req.SafetySettings))
		for i, v := range req.SafetySettings {
			s[i] = map[string]any{"category": v.Category, "threshold": v.Threshold}
		}
		root["safetySettings"] = s
	} else {
		root["safetySettings"] = ir.DefaultGeminiSafetySettings()
	}
}

func (p *GeminiProvider) fixImageAspectRatioForPreview(root map[string]any, aspectRatio string) {
	contents, ok := root["contents"].([]any)
	if !ok || len(contents) == 0 {
		return
	}
	for _, c := range contents {
		if cm, ok := c.(map[string]any); ok {
			for _, p := range cm["parts"].([]any) {
				if pm, ok := p.(map[string]any); ok {
					if _, ok := pm["inlineData"]; ok {
						return
					}
				}
			}
		}
	}
	img, err := util.CreateWhiteImageBase64(aspectRatio)
	if err != nil {
		return
	}
	fc, ok := contents[0].(map[string]any)
	if !ok {
		return
	}
	parts := append([]any{map[string]any{"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear."}, map[string]any{"inlineData": map[string]any{"mime_type": "image/png", "data": img}}}, fc["parts"].([]any)...)
	fc["parts"] = parts
	if gc, ok := root["generationConfig"].(map[string]any); ok {
		gc["responseModalities"] = []string{"IMAGE", "TEXT"}
		delete(gc, "imageConfig")
	}
}

func ToGeminiResponse(messages []ir.Message, usage *ir.Usage, model string) ([]byte, error) {
	return ToGeminiResponseMeta(messages, usage, model, nil)
}

func ToGeminiResponseMeta(messages []ir.Message, usage *ir.Usage, model string, meta *ir.OpenAIMeta) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model, false)
	candidate := map[string]any{"content": map[string]any{"role": "model", "parts": builder.BuildGeminiContentParts()}, "finishReason": "STOP"}
	if meta != nil && meta.GroundingMetadata != nil {
		candidate["groundingMetadata"] = buildGroundingMetadataMap(meta.GroundingMetadata)
	}
	response := map[string]any{"candidates": []any{}, "modelVersion": model}
	if builder.HasContent() {
		response["candidates"] = []any{candidate}
	}
	if usage != nil {
		um := map[string]any{"promptTokenCount": usage.PromptTokens, "candidatesTokenCount": usage.CompletionTokens, "totalTokenCount": usage.TotalTokens}
		if usage.ThoughtsTokenCount > 0 {
			um["thoughtsTokenCount"] = usage.ThoughtsTokenCount
		}
		if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
			um["cachedContentTokenCount"] = usage.PromptTokensDetails.CachedTokens
		}
		if usage.ToolUsePromptTokens > 0 {
			um["toolUsePromptTokenCount"] = usage.ToolUsePromptTokens
		}
		response["usageMetadata"] = um
	}
	return json.Marshal(response)
}

func ToGeminiChunk(event ir.UnifiedEvent, model string) ([]byte, error) {
	if event.Type == ir.EventTypeStreamMeta {
		return nil, nil
	}
	candidate := map[string]any{"content": map[string]any{"role": "model", "parts": []any{}}}
	chunk := map[string]any{"candidates": []any{}, "modelVersion": model}
	switch event.Type {
	case ir.EventTypeToken:
		t := event.Content
		if t == "" {
			t = "\u200b"
		}
		candidate["content"].(map[string]any)["parts"] = []any{map[string]any{"text": t}}
	case ir.EventTypeReasoning:
		t := event.Reasoning
		if t == "" {
			t = "\u200b"
		}
		p := map[string]any{"text": t, "thought": true}
		if len(event.ThoughtSignature) > 0 {
			p["thoughtSignature"] = string(event.ThoughtSignature)
		}
		candidate["content"].(map[string]any)["parts"] = []any{p}
	case ir.EventTypeToolCall:
		if event.ToolCall != nil {
			var args any = map[string]any{}
			if event.ToolCall.Args != "" && event.ToolCall.Args != "{}" {
				json.Unmarshal([]byte(event.ToolCall.Args), &args)
			}
			p := map[string]any{"functionCall": map[string]any{"name": event.ToolCall.Name, "args": args}}
			if len(event.ThoughtSignature) > 0 {
				p["thoughtSignature"] = string(event.ThoughtSignature)
			} else if len(event.ToolCall.ThoughtSignature) > 0 {
				p["thoughtSignature"] = string(event.ToolCall.ThoughtSignature)
			}
			candidate["content"].(map[string]any)["parts"] = []any{p}
		}
	case ir.EventTypeImage:
		if event.Image != nil {
			candidate["content"].(map[string]any)["parts"] = []any{map[string]any{"inlineData": map[string]any{"mimeType": event.Image.MimeType, "data": event.Image.Data}}}
		}
	case ir.EventTypeCodeExecution:
		if ec := event.CodeExecution; ec != nil {
			var p map[string]any
			if ec.Code != "" {
				p = map[string]any{"executableCode": map[string]any{"language": ec.Language, "code": ec.Code}}
			} else {
				p = map[string]any{"codeExecutionResult": map[string]any{"outcome": ec.Outcome, "output": ec.Output}}
			}
			candidate["content"].(map[string]any)["parts"] = []any{p}
		}
	case ir.EventTypeFinish:
		candidate["finishReason"] = "STOP"
		if event.GroundingMetadata != nil {
			candidate["groundingMetadata"] = buildGroundingMetadataMap(event.GroundingMetadata)
		}
		if usage := event.Usage; usage != nil {
			um := map[string]any{"promptTokenCount": usage.PromptTokens, "candidatesTokenCount": usage.CompletionTokens, "totalTokenCount": usage.TotalTokens}
			if usage.ThoughtsTokenCount > 0 {
				um["thoughtsTokenCount"] = usage.ThoughtsTokenCount
			}
			if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
				um["cachedContentTokenCount"] = usage.PromptTokensDetails.CachedTokens
			}
			if usage.ToolUsePromptTokens > 0 {
				um["toolUsePromptTokenCount"] = usage.ToolUsePromptTokens
			}
			chunk["usageMetadata"] = um
		}
	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)
	}
	chunk["candidates"] = []any{candidate}
	jb, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}
	return append(jb, '\n'), nil
}

func buildGroundingMetadataMap(gm *ir.GroundingMetadata) map[string]any {
	if gm == nil {
		return nil
	}
	res := map[string]any{}
	if len(gm.WebSearchQueries) > 0 {
		res["webSearchQueries"] = gm.WebSearchQueries
	}
	if gm.SearchEntryPoint != nil && gm.SearchEntryPoint.RenderedContent != "" {
		res["searchEntryPoint"] = map[string]any{"renderedContent": gm.SearchEntryPoint.RenderedContent}
	}
	if len(gm.GroundingChunks) > 0 {
		var chunks []map[string]any
		for _, c := range gm.GroundingChunks {
			if c.Web != nil {
				w := map[string]any{"uri": c.Web.URI, "title": c.Web.Title}
				if c.Web.Domain != "" {
					w["domain"] = c.Web.Domain
				}
				chunks = append(chunks, map[string]any{"web": w})
			}
		}
		res["groundingChunks"] = chunks
	}
	if len(gm.GroundingSupports) > 0 {
		var supports []map[string]any
		for _, s := range gm.GroundingSupports {
			sup := map[string]any{}
			if s.Segment != nil {
				seg := map[string]any{"text": s.Segment.Text}
				if s.Segment.StartIndex > 0 {
					seg["startIndex"] = s.Segment.StartIndex
				}
				if s.Segment.EndIndex > 0 {
					seg["endIndex"] = s.Segment.EndIndex
				}
				sup["segment"] = seg
			}
			if len(s.GroundingChunkIndices) > 0 {
				sup["groundingChunkIndices"] = s.GroundingChunkIndices
			}
			supports = append(supports, sup)
		}
		res["groundingSupports"] = supports
	}
	res["retrievalMetadata"] = map[string]any{}
	return res
}

type VertexEnvelopeProvider struct{}

type coalescedMsg struct {
	role         string
	parts        []any
	cacheControl *ir.CacheControl
}

func (p *VertexEnvelopeProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	innerReq, err := p.buildInnerRequest(req)
	if err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"project": "", "model": req.Model, "request": innerReq})
}

func (p *VertexEnvelopeProvider) buildInnerRequest(req *ir.UnifiedChatRequest) (any, error) {
	if ir.IsClaudeModel(req.Model) {
		return p.buildClaudeInnerRequest(req), nil
	}
	return json.RawMessage(mustConvertGemini(req)), nil
}

func mustConvertGemini(req *ir.UnifiedChatRequest) []byte {
	gj, _ := (&GeminiProvider{}).ConvertRequest(req)
	return gj
}

func (p *VertexEnvelopeProvider) buildClaudeInnerRequest(req *ir.UnifiedChatRequest) map[string]any {
	root := map[string]any{
		"contents": p.buildClaudeContents(req),
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

	gc := p.buildClaudeGenerationConfig(req)
	if len(gc) > 0 {
		root["generationConfig"] = gc
	}

	if len(req.Tools) > 0 {
		root["tools"] = p.buildClaudeTools(req)
	}

	return root
}

func (p *VertexEnvelopeProvider) buildClaudeContents(req *ir.UnifiedChatRequest) []any {
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
			toolResultParts := p.buildClaudeToolResultParts(msg, toolIDToName)
			msgParts = append(userParts, toolResultParts...)
		case ir.RoleAssistant:
			role = "model"
			msgParts = p.buildClaudeAssistantParts(msg)
		case ir.RoleTool:
			role = "user"
			msgParts = p.buildClaudeToolResultParts(msg, toolIDToName)
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

// reorganizeToolPairs ensures each functionCall is immediately followed by its functionResponse.
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

func (p *VertexEnvelopeProvider) buildClaudeAssistantParts(msg *ir.Message) []any {
	var parts []any

	for i := range msg.Content {
		cp := &msg.Content[i]
		switch cp.Type {
		case ir.ContentTypeReasoning:
			if cp.Reasoning != "" {
				part := map[string]any{"text": cp.Reasoning, "thought": true}
				if ir.IsValidThoughtSignature(cp.ThoughtSignature) {
					part["thoughtSignature"] = string(cp.ThoughtSignature)
				}
				parts = append(parts, part)
			}
		case ir.ContentTypeRedactedThinking:
			// Redacted thinking blocks contain encrypted data that must be preserved
			// In Gemini CLI format for Claude, this is represented with a data field
			if cp.RedactedData != "" {
				parts = append(parts, map[string]any{"data": cp.RedactedData})
			}
		case ir.ContentTypeText:
			if cp.Text != "" {
				parts = append(parts, map[string]any{"text": cp.Text})
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
				"args": json.RawMessage(ir.ValidateAndNormalizeJSON(tc.Args)),
				"id":   ir.ToClaudeToolID(id),
			},
		}
		if ir.IsValidThoughtSignature(tc.ThoughtSignature) {
			part["thoughtSignature"] = string(tc.ThoughtSignature)
		}
		parts = append(parts, part)
	}

	return parts
}

func (p *VertexEnvelopeProvider) buildClaudeToolResultParts(msg *ir.Message, toolIDToName map[string]string) []any {
	var parts []any
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
			parts = append(parts, map[string]any{
				"functionResponse": map[string]any{
					"name":     name,
					"id":       ir.ToClaudeToolID(tr.ToolCallID),
					"response": resp,
				},
			})
		}
	}
	return parts
}

func (p *VertexEnvelopeProvider) buildClaudeGenerationConfig(req *ir.UnifiedChatRequest) map[string]any {
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

func (p *VertexEnvelopeProvider) buildClaudeTools(req *ir.UnifiedChatRequest) []any {
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

func (p *VertexEnvelopeProvider) ParseResponse(rj []byte) ([]ir.Message, *ir.Usage, error) {
	_, ms, us, err := to_ir.ParseGeminiResponse(rj)
	return ms, us, err
}

func (p *VertexEnvelopeProvider) ParseStreamChunk(cj []byte) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunk(cj)
}

func (p *VertexEnvelopeProvider) ParseStreamChunkWithContext(cj []byte, sc *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunkWithContext(cj, sc)
}

func (p *VertexEnvelopeProvider) ParseStreamChunkWithState(cj []byte, state *ir.GeminiStreamParserState) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunkWithState(cj, state)
}

func (p *VertexEnvelopeProvider) ParseStreamChunkWithStateContext(cj []byte, state *ir.GeminiStreamParserState, sc *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunkWithStateContext(cj, state, sc)
}

func buildFunctionResponseObject(res string, isError bool) any {
	if res == "" {
		if isError {
			return map[string]any{"error": "Tool execution failed"}
		}
		return map[string]any{"content": ""}
	}
	if isError {
		return map[string]any{"error": res}
	}
	if parsed := gjson.Parse(res); parsed.Type == gjson.JSON {
		var jo any
		if err := json.Unmarshal([]byte(res), &jo); err == nil {
			if _, isArray := jo.([]any); isArray {
				return map[string]any{"result": jo}
			}
			return jo
		}
	}
	return map[string]any{"content": res}
}

func (p *GeminiProvider) applyThinkingConfig(gc map[string]any, req *ir.UnifiedChatRequest, isG3 bool) {
	if force, _ := req.Metadata[ir.MetaForceDisableThinking].(bool); force {
		return
	}
	budget, include, auto := util.GetAutoAppliedThinkingConfig(req.Model)
	if req.Thinking == nil && !auto {
		return
	}
	eb, ei := budget, include
	if req.Thinking != nil {
		if req.Thinking.ThinkingBudget != nil {
			eb = int(*req.Thinking.ThinkingBudget)
		}
		ei = req.Thinking.IncludeThoughts
	}
	if eb <= 0 {
		eb = ir.DefaultThinkingBudgetTokens
	}
	if isG3 {
		p.applyGemini3ThinkingConfig(gc, req, ei)
	} else {
		gc["thinkingConfig"] = map[string]any{"thinkingBudget": eb, "includeThoughts": ei}
	}
}

func (p *GeminiProvider) applyGemini3ThinkingConfig(gc map[string]any, req *ir.UnifiedChatRequest, ei bool) {
	tl := ""
	switch {
	case req.Thinking != nil && req.Thinking.Effort != "":
		tl = string(ir.EffortToThinkingLevel(req.Model, string(req.Thinking.Effort)))
	case req.Thinking != nil && req.Thinking.ThinkingBudget != nil:
		tl = string(ir.BudgetToThinkingLevel(req.Model, int(*req.Thinking.ThinkingBudget)))
	default:
		tl = string(ir.DefaultThinkingLevel(req.Model))
	}
	gc["thinkingConfig"] = map[string]any{"includeThoughts": ei, "thinkingLevel": tl}
}
