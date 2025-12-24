// Package from_ir converts unified request format to provider-specific formats.
package from_ir

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
)

// GeminiProvider handles conversion to Gemini AI Studio API format.
type GeminiProvider struct{}

func (p *GeminiProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	root := map[string]any{
		"contents": []any{},
	}

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

	// Restore Gemini-specific fields from Metadata (passthrough)
	if req.Metadata != nil {
		if v, ok := req.Metadata[ir.MetaGeminiCachedContent]; ok {
			root["cachedContent"] = v
		}
		if v, ok := req.Metadata[ir.MetaGeminiLabels]; ok {
			root["labels"] = v
		}
	}

	// Handle gemini-2.5-flash-image-preview aspect ratio
	if req.Model == "gemini-2.5-flash-image-preview" && req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" {
		p.fixImageAspectRatioForPreview(root, req.ImageConfig.AspectRatio)
	}

	return json.Marshal(root)
}

func (p *GeminiProvider) applyGenerationConfig(root map[string]any, req *ir.UnifiedChatRequest) error {
	genConfig := make(map[string]any)

	if req.Temperature != nil {
		genConfig["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		genConfig["topP"] = *req.TopP
	}
	if req.TopK != nil {
		genConfig["topK"] = *req.TopK
	}
	if req.MaxTokens != nil && *req.MaxTokens > 0 {
		genConfig["maxOutputTokens"] = *req.MaxTokens
	}
	if len(req.StopSequences) > 0 {
		genConfig["stopSequences"] = req.StopSequences
	}
	if req.FrequencyPenalty != nil {
		genConfig["frequencyPenalty"] = *req.FrequencyPenalty
	}
	if req.PresencePenalty != nil {
		genConfig["presencePenalty"] = *req.PresencePenalty
	}
	if req.Logprobs != nil && *req.Logprobs {
		genConfig["responseLogprobs"] = true
		if req.TopLogprobs != nil {
			genConfig["logprobs"] = *req.TopLogprobs
		}
	}
	if req.CandidateCount != nil && *req.CandidateCount > 1 {
		genConfig["candidateCount"] = *req.CandidateCount
	}

	isGemini3 := ir.IsGemini3(req.Model)
	isClaude := ir.IsClaude(req.Model)

	p.applyThinkingConfig(genConfig, req, isGemini3, isClaude)

	if len(req.ResponseModality) > 0 {
		genConfig["responseModalities"] = req.ResponseModality
	}

	if req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" && req.Model != "gemini-2.5-flash-image-preview" {
		imgConfig := map[string]any{"aspectRatio": req.ImageConfig.AspectRatio}
		if req.ImageConfig.ImageSize != "" {
			imgConfig["imageSize"] = req.ImageConfig.ImageSize
		}
		genConfig["imageConfig"] = imgConfig
	}

	if req.ResponseSchema != nil {
		genConfig["responseMimeType"] = "application/json"
		genConfig["responseJsonSchema"] = req.ResponseSchema
	}

	if req.FunctionCalling != nil {
		toolConfig := make(map[string]any)
		fcConfig := make(map[string]any)

		if req.FunctionCalling.Mode != "" {
			fcConfig["mode"] = req.FunctionCalling.Mode
		}
		if len(req.FunctionCalling.AllowedFunctionNames) > 0 {
			fcConfig["allowedFunctionNames"] = req.FunctionCalling.AllowedFunctionNames
		}
		if req.FunctionCalling.StreamFunctionCallArguments {
			fcConfig["streamFunctionCallArguments"] = true
		}

		if len(fcConfig) > 0 {
			toolConfig["functionCallingConfig"] = fcConfig
			root["toolConfig"] = toolConfig
		}
	}

	// Validation: Ensure maxOutputTokens is present and valid
	// Upstream Antigravity/Vertex may reject missing maxOutputTokens or treat as 0
	currentMax := 0
	if v, ok := genConfig["maxOutputTokens"].(int); ok {
		currentMax = v
	} else if v32, ok := genConfig["maxOutputTokens"].(int32); ok {
		currentMax = int(v32)
	}

	if currentMax < 1 {
		// Default to 8192 if not provided (Safe for Claude Sonnet/Opus)
		genConfig["maxOutputTokens"] = ir.DefaultMaxOutputTokens
	}

	if len(genConfig) > 0 {
		root["generationConfig"] = genConfig
	}
	return nil
}

func (p *GeminiProvider) applyMessages(root map[string]any, req *ir.UnifiedChatRequest) error {
	if len(req.Messages) == 0 {
		return nil
	}

	toolCallIDToName, toolResults := ir.BuildToolMaps(req.Messages)
	coalescer := ir.GetContentCoalescer(len(req.Messages) * 2)

	for i := range req.Messages {
		msg := &req.Messages[i]

		switch msg.Role {
		case ir.RoleSystem:
			if text := p.extractSystemText(msg); text != "" {
				root["systemInstruction"] = map[string]any{
					"role":  "user",
					"parts": []any{map[string]any{"text": text}},
				}
			}

		case ir.RoleUser:
			parts := p.buildUserParts(msg)
			coalescer.Emit("user", parts)

		case ir.RoleAssistant:
			modelParts, responseParts := p.buildAssistantAndToolParts(
				msg, toolCallIDToName, toolResults, req.Model,
			)
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

func (p *GeminiProvider) buildUserParts(msg *ir.Message) []any {
	parts := make([]any, 0, len(msg.Content))

	for i := range msg.Content {
		part := &msg.Content[i]
		switch part.Type {
		case ir.ContentTypeText:
			if part.Text != "" {
				parts = append(parts, map[string]any{"text": part.Text})
			}
		case ir.ContentTypeImage:
			if p := p.buildImagePart(part.Image); p != nil {
				parts = append(parts, p)
			}
		case ir.ContentTypeAudio:
			if p := p.buildAudioPart(part.Audio); p != nil {
				parts = append(parts, p)
			}
		case ir.ContentTypeVideo:
			if p := p.buildVideoPart(part.Video); p != nil {
				parts = append(parts, p)
			}
		}
	}
	return parts
}

func (p *GeminiProvider) buildImagePart(img *ir.ImagePart) any {
	if img == nil {
		return nil
	}
	if img.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": img.MimeType,
				"data":     img.Data,
			},
		}
	}
	if url := img.URL; strings.HasPrefix(url, "files/") || strings.HasPrefix(url, "gs://") {
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": img.MimeType,
				"fileUri":  url,
			},
		}
	}
	return nil
}

func (p *GeminiProvider) buildAudioPart(audio *ir.AudioPart) any {
	if audio == nil || audio.Data == "" {
		return nil
	}
	if strings.HasPrefix(audio.Data, "files/") {
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": audio.MimeType,
				"fileUri":  audio.Data,
			},
		}
	}
	return map[string]any{
		"inlineData": map[string]any{
			"mimeType": audio.MimeType,
			"data":     audio.Data,
		},
	}
}

func (p *GeminiProvider) buildVideoPart(video *ir.VideoPart) any {
	if video == nil {
		return nil
	}
	if video.Data != "" {
		return map[string]any{
			"inlineData": map[string]any{
				"mimeType": video.MimeType,
				"data":     video.Data,
			},
		}
	}
	if video.FileURI != "" {
		return map[string]any{
			"fileData": map[string]any{
				"mimeType": video.MimeType,
				"fileUri":  video.FileURI,
			},
		}
	}
	return nil
}

func (p *GeminiProvider) buildAssistantAndToolParts(
	msg *ir.Message,
	toolIDToName map[string]string,
	toolResults map[string]*ir.ToolResultPart,
	model string,
) (modelParts, responseParts []any) {
	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil, nil
	}

	for i := range msg.Content {
		cp := &msg.Content[i]
		switch cp.Type {
		case ir.ContentTypeReasoning:
			if cp.Reasoning == "" {
				continue
			}
			if modelParts == nil {
				modelParts = make([]any, 0, len(msg.Content)+len(msg.ToolCalls))
			}
			part := map[string]any{"text": cp.Reasoning, "thought": true}
			if ir.IsValidThoughtSignature(cp.ThoughtSignature) {
				part["thoughtSignature"] = string(cp.ThoughtSignature)
			}
			modelParts = append(modelParts, part)

		case ir.ContentTypeText:
			if cp.Text == "" {
				continue
			}
			if modelParts == nil {
				modelParts = make([]any, 0, len(msg.Content)+len(msg.ToolCalls))
			}
			part := map[string]any{"text": cp.Text}
			if ir.IsValidThoughtSignature(cp.ThoughtSignature) {
				part["thoughtSignature"] = string(cp.ThoughtSignature)
			}
			modelParts = append(modelParts, part)
		}
	}

	if len(msg.ToolCalls) == 0 {
		return modelParts, nil
	}

	if modelParts == nil {
		modelParts = make([]any, 0, len(msg.ToolCalls))
	}

	for i := range msg.ToolCalls {
		tc := &msg.ToolCalls[i]

		toolID := tc.ID
		if toolID == "" {
			toolID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
		}

		fcMap := map[string]any{
			"name": tc.Name,
			"args": json.RawMessage(ir.ValidateAndNormalizeJSON(tc.Args)),
			"id":   toolID,
		}
		part := map[string]any{"functionCall": fcMap}

		// Only use the tool call's own signature - do not propagate from other parts
		// ThoughtSignature is opaque and context-specific, reusing it can cause corruption
		if ir.IsValidThoughtSignature(tc.ThoughtSignature) {
			part["thoughtSignature"] = string(tc.ThoughtSignature)
		} else if ir.IsGemini3(model) {
			// Gemini 3 requires thoughtSignature for function calls (strict validation)
			// Use dummy signature for migrated conversations without valid signatures
			// See: https://ai.google.dev/gemini-api/docs/gemini-3#thought_signatures
			part["thoughtSignature"] = ir.DummyThoughtSignature
		}
		modelParts = append(modelParts, part)

		name, hasName := toolIDToName[toolID]
		if !hasName {
			continue
		}
		resultPart, hasResult := toolResults[toolID]
		if !hasResult {
			continue
		}

		if responseParts == nil {
			responseParts = make([]any, 0, len(msg.ToolCalls)*2)
		}

		funcResp := map[string]any{
			"name":     name,
			"id":       toolID,
			"response": buildFunctionResponseObject(resultPart.Result, resultPart.IsError),
		}
		responseParts = append(responseParts, map[string]any{"functionResponse": funcResp})

		for _, img := range resultPart.Images {
			if img.Data != "" {
				responseParts = append(responseParts, map[string]any{
					"inlineData": map[string]any{
						"mimeType": img.MimeType,
						"data":     img.Data,
					},
				})
			} else if img.URL != "" {
				responseParts = append(responseParts, map[string]any{
					"fileData": map[string]any{
						"fileUri":  img.URL,
						"mimeType": img.MimeType,
					},
				})
			}
		}
	}

	return modelParts, responseParts
}

func (p *GeminiProvider) applyTools(root map[string]any, req *ir.UnifiedChatRequest) error {
	// Extract built-in tools from Metadata (using ir.Meta* constants)
	var googleSearch, googleSearchRetrieval, codeExecution, urlContext, fileSearch any
	if req.Metadata != nil {
		if gs, ok := req.Metadata[ir.MetaGoogleSearch]; ok {
			// Clean internal fields before passing to Gemini
			if gsMap, ok := gs.(map[string]any); ok {
				cleanedGS := make(map[string]any)
				for k, v := range gsMap {
					if k != "_original_type" && k != "max_uses" { // Claude-specific fields
						cleanedGS[k] = v
					}
				}
				googleSearch = cleanedGS
			} else {
				googleSearch = gs
			}
		}
		if gsr, ok := req.Metadata[ir.MetaGoogleSearchRetrieval]; ok {
			googleSearchRetrieval = gsr
		}
		if ce, ok := req.Metadata[ir.MetaCodeExecution]; ok {
			// Clean internal fields
			if ceMap, ok := ce.(map[string]any); ok {
				cleanedCE := make(map[string]any)
				for k, v := range ceMap {
					if k != "container" { // OpenAI-specific field
						cleanedCE[k] = v
					}
				}
				codeExecution = cleanedCE
			} else {
				codeExecution = ce
			}
		}
		if uc, ok := req.Metadata[ir.MetaURLContext]; ok {
			urlContext = uc
		}
		if fs, ok := req.Metadata[ir.MetaFileSearch]; ok {
			// Note: Gemini fileSearch requires fileSearchStoreNames, OpenAI uses vector_store
			// Clean OpenAI-specific fields
			if fsMap, ok := fs.(map[string]any); ok {
				cleanedFS := make(map[string]any)
				for k, v := range fsMap {
					if k != "vector_store" && k != "max_num_results" && k != "ranking_options" {
						cleanedFS[k] = v
					}
				}
				fileSearch = cleanedFS
			} else {
				fileSearch = fs
			}
		}

		// Note: Claude-specific tools (computer, bash, text_editor) have no Gemini equivalent
		// MetaClaudeComputer, MetaClaudeBash, MetaClaudeTextEditor are silently dropped
		// These tools require Claude-specific backend support
	}

	hasBuiltInTools := googleSearch != nil || googleSearchRetrieval != nil || codeExecution != nil || urlContext != nil || fileSearch != nil
	if len(req.Tools) == 0 && !hasBuiltInTools {
		return nil
	}

	toolNode := make(map[string]any)

	if len(req.Tools) > 0 {
		funcs := make([]any, len(req.Tools))
		for i, t := range req.Tools {
			funcDecl := map[string]any{
				"name":        t.Name,
				"description": t.Description,
			}
			if len(t.Parameters) == 0 {
				funcDecl["parameters"] = map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				}
			} else {
				// Use "parameters" instead of "parametersJsonSchema" for broad compatibility
				// (including Antigravity/Vertex Claude models).
				// Use CleanJsonSchemaForGemini to recursively remove unsupported fields like
				// exclusiveMinimum, exclusiveMaximum, etc.
				params := ir.CleanJsonSchemaForGemini(copyMap(t.Parameters))
				// Gemini requires parameters to have type "object"
				// Handle nil, empty string, or invalid type values (e.g., "None" from some SDKs)
				typeVal, hasType := params["type"].(string)
				if !hasType || typeVal == "" || typeVal == "None" {
					params["type"] = "object"
				}
				if params["properties"] == nil {
					params["properties"] = map[string]any{}
				}
				funcDecl["parameters"] = params
			}
			funcs[i] = funcDecl
		}
		toolNode["functionDeclarations"] = funcs
	}

	// Add built-in tools
	if googleSearch != nil {
		toolNode["googleSearch"] = googleSearch
	}
	if googleSearchRetrieval != nil {
		toolNode["googleSearchRetrieval"] = googleSearchRetrieval
	}
	if codeExecution != nil {
		toolNode["codeExecution"] = codeExecution
	}
	if urlContext != nil {
		toolNode["urlContext"] = urlContext
	}
	if fileSearch != nil {
		toolNode["fileSearch"] = fileSearch
	}

	root["tools"] = []any{toolNode}

	// Set toolConfig.functionCallingConfig.mode based on ToolChoice from request.
	// - "none" -> NONE (don't call functions)
	// - "required" or "any" -> ANY (must call a function)
	// - "auto" or empty -> AUTO (model decides)
	// Note: We default to AUTO, not ANY, because ANY forces the model to always
	// call a function even when inappropriate (e.g., user says "hello").
	if len(req.Tools) > 0 {
		mode := "AUTO" // Default: let model decide
		switch req.ToolChoice {
		case "none":
			mode = "NONE"
		case "required", "any":
			mode = "ANY"
		case "auto", "":
			mode = "AUTO"
		}
		root["toolConfig"] = map[string]any{
			"functionCallingConfig": map[string]any{
				"mode": mode,
			},
		}
	}

	return nil
}

func (p *GeminiProvider) applySafetySettings(root map[string]any, req *ir.UnifiedChatRequest) {
	if len(req.SafetySettings) > 0 {
		settings := make([]any, len(req.SafetySettings))
		for i, s := range req.SafetySettings {
			settings[i] = map[string]any{
				"category":  s.Category,
				"threshold": s.Threshold,
			}
		}
		root["safetySettings"] = settings
	} else {
		// Default settings
		root["safetySettings"] = ir.DefaultGeminiSafetySettings()
	}
}

// fixImageAspectRatioForPreview handles gemini-2.5-flash-image-preview requirements.
func (p *GeminiProvider) fixImageAspectRatioForPreview(root map[string]any, aspectRatio string) {
	contents, ok := root["contents"].([]any)
	if !ok || len(contents) == 0 {
		return
	}

	// Check if there's already an image
	hasInlineData := false
	for _, content := range contents {
		if cMap, ok := content.(map[string]any); ok {
			if parts, ok := cMap["parts"].([]any); ok {
				for _, part := range parts {
					if pMap, ok := part.(map[string]any); ok {
						if _, exists := pMap["inlineData"]; exists {
							hasInlineData = true
							break
						}
					}
				}
			}
		}
		if hasInlineData {
			break
		}
	}

	if hasInlineData {
		return
	}

	// Inject white image placeholder
	emptyImageBase64, err := util.CreateWhiteImageBase64(aspectRatio)
	if err != nil {
		return
	}

	// Create new parts for the first content message
	firstContent, ok := contents[0].(map[string]any)
	if !ok {
		return
	}
	existingParts, ok := firstContent["parts"].([]any)
	if !ok {
		return
	}

	newParts := []any{
		map[string]any{
			"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear.",
		},
		map[string]any{
			"inlineData": map[string]any{
				"mime_type": "image/png",
				"data":      emptyImageBase64,
			},
		},
	}
	newParts = append(newParts, existingParts...)
	firstContent["parts"] = newParts

	// Update generation config
	if genConfig, ok := root["generationConfig"].(map[string]any); ok {
		genConfig["responseModalities"] = []string{"IMAGE", "TEXT"}
		delete(genConfig, "imageConfig")
	} else {
		root["generationConfig"] = map[string]any{
			"responseModalities": []string{"IMAGE", "TEXT"},
		}
	}
}

// --- Response Conversion ---

// ToGeminiResponse converts messages to a complete Gemini API response.
func ToGeminiResponse(messages []ir.Message, usage *ir.Usage, model string) ([]byte, error) {
	return ToGeminiResponseMeta(messages, usage, model, nil)
}

// ToGeminiResponseMeta converts messages to a complete Gemini API response with metadata.
func ToGeminiResponseMeta(messages []ir.Message, usage *ir.Usage, model string, meta *ir.OpenAIMeta) ([]byte, error) {
	builder := ir.NewResponseBuilder(messages, usage, model)

	response := map[string]any{
		"candidates":   []any{},
		"modelVersion": model,
	}

	candidate := map[string]any{
		"content": map[string]any{
			"role":  "model",
			"parts": builder.BuildGeminiContentParts(),
		},
		"finishReason": "STOP",
	}

	// Add grounding metadata to candidate if present
	if meta != nil && meta.GroundingMetadata != nil {
		candidate["groundingMetadata"] = buildGroundingMetadataMap(meta.GroundingMetadata)
	}

	if builder.HasContent() {
		response["candidates"] = []any{candidate}
	}

	if usage != nil {
		usageMetadata := map[string]any{
			"promptTokenCount":     usage.PromptTokens,
			"candidatesTokenCount": usage.CompletionTokens,
			"totalTokenCount":      usage.TotalTokens,
		}
		if usage.ThoughtsTokenCount > 0 {
			usageMetadata["thoughtsTokenCount"] = usage.ThoughtsTokenCount
		}
		if usage.PromptTokensDetails != nil && usage.PromptTokensDetails.CachedTokens > 0 {
			usageMetadata["cachedContentTokenCount"] = usage.PromptTokensDetails.CachedTokens
		}
		if usage.ToolUsePromptTokens > 0 {
			usageMetadata["toolUsePromptTokenCount"] = usage.ToolUsePromptTokens
		}
		response["usageMetadata"] = usageMetadata
	}

	return json.Marshal(response)
}

// ToGeminiChunk converts a single event to Gemini streaming chunk.
func ToGeminiChunk(event ir.UnifiedEvent, model string) ([]byte, error) {
	chunk := map[string]any{
		"candidates":   []any{},
		"modelVersion": model,
	}

	candidate := map[string]any{
		"content": map[string]any{
			"role":  "model",
			"parts": []any{},
		},
	}

	switch event.Type {
	case ir.EventTypeToken:
		candidate["content"].(map[string]any)["parts"] = []any{
			map[string]any{"text": event.Content},
		}

	case ir.EventTypeReasoning:
		candidate["content"].(map[string]any)["parts"] = []any{
			map[string]any{"text": event.Reasoning, "thought": true},
		}

	case ir.EventTypeToolCall:
		if event.ToolCall != nil {
			var argsObj any = map[string]any{}
			if event.ToolCall.Args != "" && event.ToolCall.Args != "{}" {
				if err := json.Unmarshal([]byte(event.ToolCall.Args), &argsObj); err != nil {
					argsObj = map[string]any{}
				}
			}
			part := map[string]any{
				"functionCall": map[string]any{
					"name": event.ToolCall.Name,
					"args": argsObj,
				},
			}
			// Include thoughtSignature on functionCall part (required by Gemini API for multi-turn)
			if len(event.ThoughtSignature) > 0 {
				part["thoughtSignature"] = string(event.ThoughtSignature)
			} else if len(event.ToolCall.ThoughtSignature) > 0 {
				part["thoughtSignature"] = string(event.ToolCall.ThoughtSignature)
			}
			candidate["content"].(map[string]any)["parts"] = []any{part}
		}

	case ir.EventTypeImage:
		if event.Image != nil {
			candidate["content"].(map[string]any)["parts"] = []any{
				map[string]any{
					"inlineData": map[string]any{
						"mimeType": event.Image.MimeType,
						"data":     event.Image.Data,
					},
				},
			}
		}

	case ir.EventTypeCodeExecution:
		if event.CodeExecution != nil {
			var part map[string]any
			if event.CodeExecution.Code != "" {
				// Executable code
				part = map[string]any{
					"executableCode": map[string]any{
						"language": event.CodeExecution.Language,
						"code":     event.CodeExecution.Code,
					},
				}
			} else {
				// Code execution result
				part = map[string]any{
					"codeExecutionResult": map[string]any{
						"outcome": event.CodeExecution.Outcome,
						"output":  event.CodeExecution.Output,
					},
				}
			}
			candidate["content"].(map[string]any)["parts"] = []any{part}
		}

	case ir.EventTypeFinish:
		candidate["finishReason"] = "STOP"
		if event.GroundingMetadata != nil {
			candidate["groundingMetadata"] = buildGroundingMetadataMap(event.GroundingMetadata)
		}
		if event.Usage != nil {
			usageMetadata := map[string]any{
				"promptTokenCount":     event.Usage.PromptTokens,
				"candidatesTokenCount": event.Usage.CompletionTokens,
				"totalTokenCount":      event.Usage.TotalTokens,
			}
			if event.Usage.ThoughtsTokenCount > 0 {
				usageMetadata["thoughtsTokenCount"] = event.Usage.ThoughtsTokenCount
			}
			if event.Usage.PromptTokensDetails != nil && event.Usage.PromptTokensDetails.CachedTokens > 0 {
				usageMetadata["cachedContentTokenCount"] = event.Usage.PromptTokensDetails.CachedTokens
			}
			if event.Usage.ToolUsePromptTokens > 0 {
				usageMetadata["toolUsePromptTokenCount"] = event.Usage.ToolUsePromptTokens
			}
			chunk["usageMetadata"] = usageMetadata
		}

	case ir.EventTypeError:
		return nil, fmt.Errorf("stream error: %v", event.Error)

	default:
		return nil, nil
	}

	chunk["candidates"] = []any{candidate}

	jsonBytes, err := json.Marshal(chunk)
	if err != nil {
		return nil, err
	}

	// Gemini uses newline-delimited JSON (not SSE format)
	return append(jsonBytes, '\n'), nil
}

func buildGroundingMetadataMap(gm *ir.GroundingMetadata) map[string]any {
	if gm == nil {
		return nil
	}

	result := map[string]any{}

	if len(gm.WebSearchQueries) > 0 {
		result["webSearchQueries"] = gm.WebSearchQueries
	}

	if gm.SearchEntryPoint != nil && gm.SearchEntryPoint.RenderedContent != "" {
		result["searchEntryPoint"] = map[string]any{
			"renderedContent": gm.SearchEntryPoint.RenderedContent,
		}
	}

	if len(gm.GroundingChunks) > 0 {
		chunks := make([]map[string]any, 0, len(gm.GroundingChunks))
		for _, chunk := range gm.GroundingChunks {
			if chunk.Web != nil {
				webMap := map[string]any{
					"uri":   chunk.Web.URI,
					"title": chunk.Web.Title,
				}
				if chunk.Web.Domain != "" {
					webMap["domain"] = chunk.Web.Domain
				}
				chunks = append(chunks, map[string]any{"web": webMap})
			}
		}
		if len(chunks) > 0 {
			result["groundingChunks"] = chunks
		}
	}

	if len(gm.GroundingSupports) > 0 {
		supports := make([]map[string]any, 0, len(gm.GroundingSupports))
		for _, s := range gm.GroundingSupports {
			support := map[string]any{}
			if s.Segment != nil {
				segment := map[string]any{
					"text": s.Segment.Text,
				}
				if s.Segment.StartIndex > 0 {
					segment["startIndex"] = s.Segment.StartIndex
				}
				if s.Segment.EndIndex > 0 {
					segment["endIndex"] = s.Segment.EndIndex
				}
				support["segment"] = segment
			}
			if len(s.GroundingChunkIndices) > 0 {
				support["groundingChunkIndices"] = s.GroundingChunkIndices
			}
			supports = append(supports, support)
		}
		if len(supports) > 0 {
			result["groundingSupports"] = supports
		}
	}

	result["retrievalMetadata"] = map[string]any{}

	return result
}

// --- Gemini CLI Provider ---

// GeminiCLIProvider handles conversion to Gemini CLI format.
// CLI format wraps AI Studio format: {"project":"", "model":"", "request":{...}}
type GeminiCLIProvider struct{}

func (p *GeminiCLIProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	geminiJSON, err := (&GeminiProvider{}).ConvertRequest(req)
	if err != nil {
		return nil, err
	}

	// Wrap in CLI envelope: {"project":"", "model":"...", "request":{...}}
	envelope := map[string]any{
		"project": "",
		"model":   "",
		"request": json.RawMessage(geminiJSON),
	}
	if req.Model != "" {
		envelope["model"] = req.Model
	}

	return json.Marshal(envelope)
}

// ParseResponse parses a non-streaming Gemini CLI response into unified format.
// Delegates to to_ir package as the logic is identical to Gemini AI Studio response parsing.
func (p *GeminiCLIProvider) ParseResponse(responseJSON []byte) ([]ir.Message, *ir.Usage, error) {
	_, messages, usage, err := to_ir.ParseGeminiResponse(responseJSON)
	return messages, usage, err
}

// Delegates to to_ir package as the logic is identical to Gemini AI Studio chunk parsing.
func (p *GeminiCLIProvider) ParseStreamChunk(chunkJSON []byte) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunk(chunkJSON)
}

// Delegates to to_ir package as the logic is identical to Gemini AI Studio chunk parsing.
func (p *GeminiCLIProvider) ParseStreamChunkWithContext(chunkJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunkWithContext(chunkJSON, schemaCtx)
}

func buildFunctionResponseObject(result string, isError bool) any {
	if result == "" {
		if isError {
			return map[string]any{"error": "Tool execution failed"}
		}
		return map[string]any{"content": ""}
	}
	if isError {
		return map[string]any{"error": result}
	}
	if parsed := gjson.Parse(result); parsed.Type == gjson.JSON {
		var jsonObj any
		if err := json.Unmarshal([]byte(result), &jsonObj); err == nil {
			if _, isArray := jsonObj.([]any); isArray {
				return map[string]any{"result": jsonObj}
			}
			return jsonObj
		}
	}
	return map[string]any{"content": result}
}

func (p *GeminiProvider) applyThinkingConfig(genConfig map[string]any, req *ir.UnifiedChatRequest, isGemini3, isClaude bool) {
	budget, include, auto := util.GetAutoAppliedThinkingConfig(req.Model)

	if isClaude {
		if req.Thinking != nil && req.Thinking.ThinkingBudget == nil {
			b := int32(ir.DefaultThinkingBudgetTokens)
			req.Thinking.ThinkingBudget = &b
		}
		if len(req.Tools) == 0 {
			auto = true
			include = true
			if budget <= 0 {
				budget = ir.DefaultThinkingBudgetTokens
			}
		} else {
			auto = false
		}
	}

	if req.Thinking == nil && !auto {
		return
	}

	if isGemini3 {
		p.applyGemini3ThinkingConfig(genConfig, req)
	} else {
		p.applyLegacyThinkingConfig(genConfig, req, budget, include)
	}

	p.adjustMaxTokensForThinking(genConfig, req)
}

func (p *GeminiProvider) applyGemini3ThinkingConfig(genConfig map[string]any, req *ir.UnifiedChatRequest) {
	tc := map[string]any{"includeThoughts": true}

	var thinkingLevel string
	switch {
	case req.Thinking != nil && req.Thinking.Effort != "":
		thinkingLevel = string(ir.EffortToThinkingLevel(req.Model, string(req.Thinking.Effort)))
	case req.Thinking != nil && req.Thinking.ThinkingBudget != nil:
		thinkingLevel = string(ir.BudgetToThinkingLevel(req.Model, int(*req.Thinking.ThinkingBudget)))
	default:
		thinkingLevel = string(ir.DefaultThinkingLevel(req.Model))
	}

	tc["thinkingLevel"] = thinkingLevel
	genConfig["thinkingConfig"] = tc
}

func (p *GeminiProvider) applyLegacyThinkingConfig(genConfig map[string]any, req *ir.UnifiedChatRequest, budget int, include bool) {
	effectiveBudget := budget
	if req.Thinking != nil && req.Thinking.ThinkingBudget != nil {
		effectiveBudget = int(*req.Thinking.ThinkingBudget)
	}

	effectiveInclude := include
	if req.Thinking != nil {
		effectiveInclude = req.Thinking.IncludeThoughts
	}

	genConfig["thinkingConfig"] = map[string]any{
		"thinkingBudget":  effectiveBudget,
		"includeThoughts": effectiveInclude,
	}
}

func (p *GeminiProvider) adjustMaxTokensForThinking(genConfig map[string]any, req *ir.UnifiedChatRequest) {
	tc, ok := genConfig["thinkingConfig"].(map[string]any)
	if !ok {
		return
	}

	var b int32
	switch v := tc["thinkingBudget"].(type) {
	case int:
		b = int32(v)
	case int32:
		b = v
	}

	if b <= 0 {
		return
	}

	currentMax := 0
	switch v := genConfig["maxOutputTokens"].(type) {
	case int:
		currentMax = v
	case int32:
		currentMax = int(v)
	default:
		if req.MaxTokens != nil {
			currentMax = *req.MaxTokens
		}
	}

	newMax := max(currentMax, int(b)*2, ir.GeminiSafeMaxTokens)
	if newMax > currentMax {
		genConfig["maxOutputTokens"] = newMax
	}
}
