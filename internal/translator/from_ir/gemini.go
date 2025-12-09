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
	if req.MaxTokens != nil {
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

	// Check if model is Gemini 3 family (gemini-3-pro-preview, gemini-3-pro-high, etc.)
	isGemini3 := strings.HasPrefix(req.Model, "gemini-3")
	if util.ModelSupportsThinking(req.Model) || isGemini3 {
		if req.Thinking != nil {
			if isGemini3 {
				// Gemini 3 Pro uses thinking_level (always include thoughts for readable text)
				tc := map[string]any{
					"includeThoughts": true,
				}
				switch req.Thinking.Effort {
				case "low":
					tc["thinking_level"] = "LOW"
				case "high":
					tc["thinking_level"] = "HIGH"
				}
				// If budget is set but not effort, ignore budget for Gemini 3 as per docs
				genConfig["thinkingConfig"] = tc
			} else {
				// Gemini 2.5 and others use thinking_budget (always include thoughts for readable text)
				budget := req.Thinking.Budget
				if budget > 0 {
					budget = util.NormalizeThinkingBudget(req.Model, budget)
				}
				genConfig["thinkingConfig"] = map[string]any{
					"thinkingBudget":  budget,
					"includeThoughts": true,
				}
			}
		} else if isGemini3 {
			// Gemini 3 default: include thoughts for readable text (no thinkingBudget needed)
			genConfig["thinkingConfig"] = map[string]any{
				"includeThoughts": true,
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
			if len(msg.ToolCalls) > 0 {
				// Pre-allocate slices
				parts := make([]any, 0, len(msg.ToolCalls))
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
						// Generate a unique ID for this tool call
						toolID = fmt.Sprintf("call_%d_%d", time.Now().UnixNano(), i)
					}
					fcMap["id"] = toolID

					part := map[string]any{
						"functionCall": fcMap,
					}
					if tc.ThoughtSignature != "" {
						part["thoughtSignature"] = tc.ThoughtSignature
					} else if i == 0 {
						// Fallback for missing signature (only for first tool call)
						part["thoughtSignature"] = "skip_thought_signature_validator"
					}
					parts = append(parts, part)
					toolCallIDs = append(toolCallIDs, toolID)
				}

				contents = append(contents, map[string]any{
					"role":  "model",
					"parts": parts,
				})

				var responseParts []any

				if debugToolCalls {
					log.Debugf("gemini: TOOL CALL IDs in this message: %v", toolCallIDs)
					log.Debugf("gemini: toolCallIDToName map: %v", toolCallIDToName)
					toolResultIDs := make([]string, 0)
					for id := range toolResults {
						toolResultIDs = append(toolResultIDs, id)
					}
					log.Debugf("gemini: toolResults IDs: %v", toolResultIDs)
				}

				for _, tcID := range toolCallIDs {
					name, ok := toolCallIDToName[tcID]
					if !ok {
						if debugToolCalls {
							log.Debugf("gemini: SKIP - tool call ID %s not found in toolCallIDToName", tcID)
						}
						continue
					}
					resultPart, hasResult := toolResults[tcID]
					if !hasResult {
						if debugToolCalls {
							log.Debugf("gemini: SKIP - tool call ID %s not found in toolResults", tcID)
						}
						continue
					}

					if debugToolCalls {
						resultPreview := resultPart.Result
						if len(resultPreview) > 100 {
							resultPreview = resultPreview[:100]
						}
						log.Debugf("gemini: MATCH - tool call ID %s -> name=%s, result=%s", tcID, name, resultPreview)
					}

					// Construct functionResponse (include 'id' field for Claude models on Antigravity/Vertex)
					funcResp := map[string]any{
						"name": name,
						"id":   tcID,
					}

					if len(resultPart.Images) > 0 || len(resultPart.Files) > 0 {
						// Multimodal function response
						var responseObj any
						if parsed := gjson.Parse(resultPart.Result); parsed.Type == gjson.JSON {
							var jsonObj any
							if err := json.Unmarshal([]byte(resultPart.Result), &jsonObj); err == nil {
								if _, isArray := jsonObj.([]any); isArray {
									responseObj = map[string]any{"result": jsonObj}
								} else {
									responseObj = jsonObj
								}
							} else {
								responseObj = map[string]any{"content": resultPart.Result}
							}
						} else {
							responseObj = map[string]any{"content": resultPart.Result}
						}
						funcResp["response"] = responseObj

						var nestedParts []any
						for _, img := range resultPart.Images {
							nestedParts = append(nestedParts, map[string]any{
								"inlineData": map[string]any{
									"mimeType": img.MimeType,
									"data":     img.Data,
								},
							})
						}
						for _, f := range resultPart.Files {
							nestedParts = append(nestedParts, map[string]any{
								"inlineData": map[string]any{ // Use inlineData for small files or fileData for GCS?
									// The doc says "Each multimodal part must contain inlineData or fileData."
									// If we have base64 data, use inlineData.
									"mimeType": "application/pdf", // Default or detect? FilePart doesn't have MimeType?
									"data":     f.FileData,
								},
							})
						}

						if len(nestedParts) > 0 {
						}
					} else {
						var responseObj any
						if parsed := gjson.Parse(resultPart.Result); parsed.Type == gjson.JSON {
							var jsonObj any
							if err := json.Unmarshal([]byte(resultPart.Result), &jsonObj); err == nil {
								if _, isArray := jsonObj.([]any); isArray {
									responseObj = map[string]any{"result": jsonObj}
								} else {
									responseObj = jsonObj
								}
							} else {
								responseObj = map[string]any{"content": resultPart.Result}
							}
						} else {
							responseObj = map[string]any{"content": resultPart.Result}
						}
						funcResp["response"] = responseObj
					}

					responseParts = append(responseParts, map[string]any{
						"functionResponse": funcResp,
					})
				}

				if len(responseParts) > 0 {
					contents = append(contents, map[string]any{
						"role":  "user",
						"parts": responseParts,
					})
				}
			} else {
				// Pre-allocate parts slice
				parts := make([]any, 0, len(msg.Content))
				for i := range msg.Content {
					part := &msg.Content[i]
					switch part.Type {
					case ir.ContentTypeReasoning:
						p := map[string]any{
							"text":    part.Reasoning,
							"thought": true,
						}
						if isValidThoughtSignature(part.ThoughtSignature) {
							p["thoughtSignature"] = part.ThoughtSignature
						}
						parts = append(parts, p)
					case ir.ContentTypeText:
						p := map[string]any{"text": part.Text}
						if isValidThoughtSignature(part.ThoughtSignature) {
							p["thoughtSignature"] = part.ThoughtSignature
						}
						parts = append(parts, p)
					}
				}

				if len(parts) > 0 {
					contents = append(contents, map[string]any{
						"role":  "model",
						"parts": parts,
					})
				}
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
				funcDecl["parameters"] = ir.CleanJsonSchema(copyMap(t.Parameters))
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
	firstContent := contents[0].(map[string]any)
	existingParts := firstContent["parts"].([]any)

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
	builder := ir.NewResponseBuilder(messages, usage, model)

	response := map[string]any{
		"candidates":   []any{},
		"modelVersion": model,
	}

	if builder.HasContent() {
		response["candidates"] = []any{
			map[string]any{
				"content": map[string]any{
					"role":  "model",
					"parts": builder.BuildGeminiContentParts(),
				},
				"finishReason": "STOP",
			},
		}
	}

	if usage != nil {
		response["usageMetadata"] = map[string]any{
			"promptTokenCount":     usage.PromptTokens,
			"candidatesTokenCount": usage.CompletionTokens,
			"totalTokenCount":      usage.TotalTokens,
		}
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
			candidate["content"].(map[string]any)["parts"] = []any{
				map[string]any{
					"functionCall": map[string]any{
						"name": event.ToolCall.Name,
						"args": argsObj,
					},
				},
			}
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
		if event.Usage != nil {
			chunk["usageMetadata"] = map[string]any{
				"promptTokenCount":     event.Usage.PromptTokens,
				"candidatesTokenCount": event.Usage.CompletionTokens,
				"totalTokenCount":      event.Usage.TotalTokens,
			}
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
func isValidThoughtSignature(ts string) bool {
	if ts == "" {
		return false
	}
	// Filter out invalid placeholder values from clients (e.g., Cherry Studio)
	switch ts {
	case "[undefined]", "undefined", "null", "[null]":
		return false
	}
	return true
}
