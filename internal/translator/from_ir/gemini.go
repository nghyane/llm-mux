// Package from_ir converts unified request format to provider-specific formats.
package from_ir

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
)

var debugToolCalls = os.Getenv("DEBUG_TOOL_CALLS") == "1"

// GeminiProvider handles conversion to Gemini AI Studio API format.
type GeminiProvider struct{}

// ConvertRequest maps UnifiedChatRequest to Gemini AI Studio API JSON format.
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

// applyGenerationConfig sets temperature, topP, topK, maxTokens, thinking, modalities, and image config.
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

	// Check models
	isGemini3 := strings.HasPrefix(req.Model, "gemini-3")
	isClaude := strings.Contains(req.Model, "claude")

	// Determine effective thinking config (handling auto-apply)
	// 3. Thinking Config Handling
	// Default values
	defaultThinkingBudget := 16000
	if isClaude {
		// Ensure default budget for Claude if not set
		if req.Thinking != nil && req.Thinking.ThinkingBudget == nil {
			b := int32(defaultThinkingBudget)
			req.Thinking.ThinkingBudget = &b
		}
	}

	budget, include, auto := util.GetAutoAppliedThinkingConfig(req.Model)
	if isClaude {
		// Only AUTO-apply thinking for Claude if NO tools in request
		// With tools, client must explicitly request thinking via req.Thinking
		if len(req.Tools) == 0 {
			auto = true // Auto-apply for Claude without tools
			include = true
			if budget <= 0 {
				budget = defaultThinkingBudget
			}
		} else {
			// With tools: don't auto-apply, but explicit req.Thinking will still work
			auto = false
		}
	}

	// Logic for Gemini 3 vs Others
	if isGemini3 {
		// Gemini 3 Pro uses thinking_level for thinking control
		// Always enable thinking with includeThoughts=true to get thinking text in response
		// This is required for SDK to preserve thinking blocks in multi-turn flows
		if req.Thinking != nil || auto {
			tc := map[string]any{
				"includeThoughts": true,
			}
			// Map Effort from IR or default
			effort := ir.ReasoningEffortMedium
			if req.Thinking != nil && req.Thinking.Effort != "" {
				effort = req.Thinking.Effort
			}

			switch effort {
			case ir.ReasoningEffortLow:
				tc["thinking_level"] = "LOW"
			case ir.ReasoningEffortMedium:
				tc["thinking_level"] = "MEDIUM"
			case ir.ReasoningEffortHigh:
				tc["thinking_level"] = "HIGH"
			default:
				tc["thinking_level"] = "MEDIUM" // Default to MEDIUM for Gemini 3
			}
			// Note: Gemini 3 uses 'thinking_level' (LOW/MEDIUM/HIGH) instead of budget
			genConfig["thinkingConfig"] = tc
		}
	} else {
		// Logic for Claude/Gemini 2.5 (non-Gemini3)
		// Apply explicit req.Thinking OR auto-apply
		if req.Thinking != nil || auto {
			// Send BOTH camelCase and snake_case to ensure upstream compatibility

			effectiveBudget := budget
			if req.Thinking != nil && req.Thinking.ThinkingBudget != nil {
				effectiveBudget = int(*req.Thinking.ThinkingBudget)
			}

			effectiveInclude := include
			if req.Thinking != nil {
				effectiveInclude = req.Thinking.IncludeThoughts
			}

			// Both Claude and Gemini use CamelCase: thinkingBudget, includeThoughts
			tc := map[string]any{
				"thinkingBudget":  effectiveBudget,
				"includeThoughts": effectiveInclude,
			}
			genConfig["thinkingConfig"] = tc
		}
	}

	if tc, ok := genConfig["thinkingConfig"].(map[string]any); ok {
		// Existing MaxTokens Logic
		b := int32(0)
		if v, ok := tc["thinkingBudget"].(int); ok {
			b = int32(v)
		} else if v32, ok := tc["thinkingBudget"].(int32); ok {
			b = v32
		}

		if b > 0 {
			currentMax := 0
			if v, ok := genConfig["maxOutputTokens"].(int); ok {
				currentMax = v
			} else if v32, ok := genConfig["maxOutputTokens"].(int32); ok {
				currentMax = int(v32)
			} else if req.MaxTokens != nil {
				currentMax = *req.MaxTokens
			}

			safeMax := 32000
			budgetInt := int(b)
			newMax := max(currentMax, budgetInt*2, safeMax)
			if newMax > currentMax {
				genConfig["maxOutputTokens"] = newMax
			}
		}
	}

	// Response Modalities
	if len(req.ResponseModality) > 0 {
		genConfig["responseModalities"] = req.ResponseModality
	}

	// Image Config (standard)
	if req.ImageConfig != nil && req.ImageConfig.AspectRatio != "" && req.Model != "gemini-2.5-flash-image-preview" {
		imgConfig := map[string]any{"aspectRatio": req.ImageConfig.AspectRatio}
		if req.ImageConfig.ImageSize != "" {
			imgConfig["imageSize"] = req.ImageConfig.ImageSize
		}
		genConfig["imageConfig"] = imgConfig
	}

	// Response Schema (Structured Output) - Gemini uses responseJsonSchema
	if req.ResponseSchema != nil {
		genConfig["responseMimeType"] = "application/json"
		genConfig["responseJsonSchema"] = req.ResponseSchema
	}

	// Function Calling Config
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
		genConfig["maxOutputTokens"] = 8192
	}

	if len(genConfig) > 0 {
		root["generationConfig"] = genConfig
	}
	return nil
}

// applyMessages converts messages to Gemini contents format.
func (p *GeminiProvider) applyMessages(root map[string]any, req *ir.UnifiedChatRequest) error {
	var contents []any
	toolCallIDToName, toolResults := ir.BuildToolMaps(req.Messages)

	for _, msg := range req.Messages {
		switch msg.Role {
		case ir.RoleSystem:
			var textContent string
			for _, part := range msg.Content {
				if part.Type == ir.ContentTypeText {
					textContent = part.Text
					break
				}
			}
			if textContent != "" {
				root["systemInstruction"] = map[string]any{
					"role": "user",
					"parts": []any{
						map[string]any{"text": textContent},
					},
				}
			}

		case ir.RoleUser:
			// Pre-allocate parts slice
			parts := make([]any, 0, len(msg.Content))
			for i := range msg.Content {
				part := &msg.Content[i]
				switch part.Type {
				case ir.ContentTypeText:
					parts = append(parts, map[string]any{"text": part.Text})
				case ir.ContentTypeImage:
					if part.Image != nil {
						parts = append(parts, map[string]any{
							"inlineData": map[string]any{
								"mimeType": part.Image.MimeType,
								"data":     part.Image.Data,
							},
						})
					}
				}
			}
			if len(parts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "user",
					"parts": parts,
				})
			}

		case ir.RoleAssistant:
			// Consolidate content parts (Text/Reasoning) and ToolCalls into one "model" message.
			// Claude Thinking requires strict ordering: Thinking -> Text? -> ToolCalls.

			parts := make([]any, 0, len(msg.Content)+len(msg.ToolCalls))

			// Claude Thinking Protocol: Placeholder injection removed - signatures are cryptographically validated.
			// SDK must send thinking blocks from previous turns back exactly as received.
			// See: https://docs.anthropic.com/claude/docs/extended-thinking

			// 1. Process Content (Thinking/Text) and collect signature for propagation
			var lastThoughtSignature []byte // Signature from thinking block to propagate to tool calls
			for _, contentPart := range msg.Content {
				switch contentPart.Type {
				case ir.ContentTypeReasoning:
					p := map[string]any{
						"text":    contentPart.Reasoning,
						"thought": true,
					}
					if isValidThoughtSignature(contentPart.ThoughtSignature) {
						p["thoughtSignature"] = string(contentPart.ThoughtSignature)
						// Store for propagation to tool calls (Gemini API requires this)
						lastThoughtSignature = contentPart.ThoughtSignature
						if debugToolCalls {
							log.Debugf("gemini: captured thinking signature (len=%d) for propagation", len(lastThoughtSignature))
						}
					}
					parts = append(parts, p)
				case ir.ContentTypeText:
					p := map[string]any{"text": contentPart.Text}
					if isValidThoughtSignature(contentPart.ThoughtSignature) {
						p["thoughtSignature"] = string(contentPart.ThoughtSignature)
					}
					parts = append(parts, p)
				}
			}

			// 2. Process Tool Calls
			var responseParts []any

			if len(msg.ToolCalls) > 0 {
				toolCallIDs := make([]string, 0, len(msg.ToolCalls))

				for i := range msg.ToolCalls {
					tc := &msg.ToolCalls[i]
					argsJSON := ir.ValidateAndNormalizeJSON(tc.Args)
					fcMap := map[string]any{
						"name": tc.Name,
						"args": json.RawMessage(argsJSON),
					}
					toolID := tc.ID
					if toolID == "" {
						toolID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
					}
					fcMap["id"] = toolID

					part := map[string]any{
						"functionCall": fcMap,
					}
					// Use tool call's own signature, or propagate from thinking block
					// Gemini API requires thoughtSignature on functionCall when thinking is enabled
					if len(tc.ThoughtSignature) > 0 {
						part["thoughtSignature"] = string(tc.ThoughtSignature)
					} else if len(lastThoughtSignature) > 0 {
						// Propagate signature from thinking block to tool call
						// This is required for multi-turn flows where SDK doesn't preserve signature on tool_use
						part["thoughtSignature"] = string(lastThoughtSignature)
					}
					parts = append(parts, part)
					toolCallIDs = append(toolCallIDs, toolID)
				}

				// Build Tool Results (User Response) based on IDs
				if debugToolCalls {
					log.Debugf("gemini: TOOL CALL IDs: %v", toolCallIDs)
				}

				for _, tcID := range toolCallIDs {
					name, ok := toolCallIDToName[tcID]
					if !ok {
						continue
					}
					resultPart, hasResult := toolResults[tcID]
					if !hasResult {
						continue
					}

					funcResp := map[string]any{
						"name": name,
						"id":   tcID,
					}
					funcResp["response"] = buildFunctionResponseObject(resultPart.Result)
					responseParts = append(responseParts, map[string]any{
						"functionResponse": funcResp,
					})
				}
			}

			if len(parts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": parts,
				})
			}

			if len(responseParts) > 0 {
				contents = append(contents, map[string]any{
					"role":  "user",
					"parts": responseParts,
				})
			}
		}
	}

	if len(contents) > 0 {
		root["contents"] = contents
	}
	return nil
}

// applyTools converts tool definitions to Gemini functionDeclarations format.
func (p *GeminiProvider) applyTools(root map[string]any, req *ir.UnifiedChatRequest) error {
	// Extract built-in tools from Metadata (using ir.Meta* constants)
	var googleSearch, googleSearchRetrieval, codeExecution, urlContext any
	if req.Metadata != nil {
		if gs, ok := req.Metadata[ir.MetaGoogleSearch]; ok {
			googleSearch = gs
		}
		if gsr, ok := req.Metadata[ir.MetaGoogleSearchRetrieval]; ok {
			googleSearchRetrieval = gsr
		}
		if ce, ok := req.Metadata[ir.MetaCodeExecution]; ok {
			codeExecution = ce
		}
		if uc, ok := req.Metadata[ir.MetaURLContext]; ok {
			urlContext = uc
		}
	}

	hasBuiltInTools := googleSearch != nil || googleSearchRetrieval != nil || codeExecution != nil || urlContext != nil
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
				params := ir.CleanJsonSchema(copyMap(t.Parameters))
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

// applySafetySettings sets safety settings or applies defaults.
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

// buildGroundingMetadataMap converts GroundingMetadata to a map for JSON output.
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

// ConvertRequest converts UnifiedChatRequest to Gemini CLI JSON format.
func (p *GeminiCLIProvider) ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error) {
	// Build core Gemini AI Studio request
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

// ParseStreamChunk parses a streaming Gemini CLI chunk into events.
// Delegates to to_ir package as the logic is identical to Gemini AI Studio chunk parsing.
func (p *GeminiCLIProvider) ParseStreamChunk(chunkJSON []byte) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunk(chunkJSON)
}

// ParseStreamChunkWithContext parses a streaming Gemini CLI chunk with schema context.
// The schemaCtx parameter allows normalizing tool call parameters based on the original request schema.
func (p *GeminiCLIProvider) ParseStreamChunkWithContext(chunkJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
	return to_ir.ParseGeminiChunkWithContext(chunkJSON, schemaCtx)
}

// isValidThoughtSignature checks if a thought signature is valid for output.
// Filters out invalid values like "[undefined]", "undefined", "null", empty strings.
func isValidThoughtSignature(ts []byte) bool {
	if len(ts) == 0 {
		return false
	}
	// Filter out invalid placeholder values from clients (e.g., Cherry Studio)
	tsStr := string(ts)
	switch tsStr {
	case "[undefined]", "undefined", "null", "[null]":
		return false
	}
	return true
}

// buildFunctionResponseObject parses tool result into Gemini function response format.
func buildFunctionResponseObject(result string) any {
	if result == "" {
		return map[string]any{"content": ""}
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
