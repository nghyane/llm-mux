// Package to_ir converts provider-specific API formats into unified format.
package to_ir

import (
	"os"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// debugToolCalls enables verbose logging of tool call arguments for debugging
var debugToolCalls = os.Getenv("DEBUG_TOOL_CALLS") == "1"

// ParseGeminiRequest converts a raw Gemini API request JSON into unified format.
// Handles both native Gemini format and Gemini CLI format (with "request" wrapper).
func ParseGeminiRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	parsed, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, err
	}

	// Handle Gemini CLI format: {"request": {...}}
	if requestWrapper := parsed.Get("request"); requestWrapper.Exists() {
		parsed = requestWrapper
	}

	req := &ir.UnifiedChatRequest{}

	req.Model = parsed.Get("model").String()

	// Generation Config
	if gc := parsed.Get("generationConfig"); gc.Exists() {
		if v := gc.Get("maxOutputTokens"); v.Exists() {
			i := int(v.Int())
			req.MaxTokens = &i
		}
		if v := gc.Get("temperature"); v.Exists() {
			f := v.Float()
			req.Temperature = &f
		}
		if v := gc.Get("topP"); v.Exists() {
			f := v.Float()
			req.TopP = &f
		}
		if v := gc.Get("topK"); v.Exists() {
			i := int(v.Int())
			req.TopK = &i
		}
		if v := gc.Get("stopSequences"); v.Exists() && v.IsArray() {
			for _, s := range v.Array() {
				req.StopSequences = append(req.StopSequences, s.String())
			}
		}

		if tc := gc.Get("thinkingConfig"); tc.Exists() {
			req.Thinking = &ir.ThinkingConfig{}
			if v := tc.Get("thinkingBudget"); v.Exists() {
				b := int32(v.Int())
				req.Thinking.ThinkingBudget = &b
			}
			if v := tc.Get("includeThoughts"); v.Exists() {
				req.Thinking.IncludeThoughts = v.Bool()
			}
			if v := tc.Get("thinkingLevel"); v.Exists() {
				req.Thinking.ThinkingLevel = ir.ThinkingLevel(v.String())
			}
		}

		if v := gc.Get("responseModalities"); v.Exists() && v.IsArray() {
			for _, m := range v.Array() {
				req.ResponseModality = append(req.ResponseModality, m.String())
			}
		}

		var schemaResult gjson.Result
		if rs := gc.Get("responseJsonSchema"); rs.Exists() {
			schemaResult = rs
		} else if rs := gc.Get("responseSchema"); rs.Exists() {
			schemaResult = rs
		}
		if schemaResult.Exists() && schemaResult.IsObject() {
			var schema map[string]any
			if err := json.Unmarshal([]byte(schemaResult.Raw), &schema); err == nil {
				req.ResponseSchema = schema
			}
		}
	}

	// System Instruction
	if si := parsed.Get("systemInstruction"); si.Exists() {
		systemText := parseGeminiSystemInstruction(si)
		if systemText != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role:    ir.RoleSystem,
				Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: systemText}},
			})
		}
	}

	if contents := parsed.Get("contents"); contents.Exists() && contents.IsArray() {
		for _, c := range contents.Array() {
			msg := parseGeminiContent(c)
			if msg.Role != "" {
				req.Messages = append(req.Messages, msg)
			}
		}
	}

	if tools := parsed.Get("tools"); tools.Exists() && tools.IsArray() {
		for _, t := range tools.Array() {
			// Support both camelCase and snake_case for functionDeclarations
			fds := t.Get("functionDeclarations")
			if !fds.Exists() || !fds.IsArray() {
				fds = t.Get("function_declarations")
			}
			if fds.Exists() && fds.IsArray() {
				for _, fd := range fds.Array() {
					var params map[string]any
					if p := fd.Get("parameters"); p.Exists() && p.IsObject() {
						if err := json.Unmarshal([]byte(p.Raw), &params); err == nil {
							params = ir.CleanJsonSchema(params)
						}
					}
					if params == nil {
						params = make(map[string]any)
					}
					req.Tools = append(req.Tools, ir.ToolDefinition{
						Name:        fd.Get("name").String(),
						Description: fd.Get("description").String(),
						Parameters:  params,
					})
				}
			}

			// Built-in tools stored in Metadata for passthrough
			if req.Metadata == nil {
				req.Metadata = make(map[string]any)
			}
			if gs := t.Get("googleSearch"); gs.Exists() {
				var gsVal any
				if gs.IsObject() {
					if err := json.Unmarshal([]byte(gs.Raw), &gsVal); err != nil {
						gsVal = map[string]any{}
					}
				} else {
					gsVal = map[string]any{}
				}
				req.Metadata[ir.MetaGoogleSearch] = gsVal
			}
			if gsr := t.Get("googleSearchRetrieval"); gsr.Exists() {
				var gsrVal any
				if gsr.IsObject() {
					if err := json.Unmarshal([]byte(gsr.Raw), &gsrVal); err != nil {
						gsrVal = map[string]any{}
					}
				} else {
					gsrVal = map[string]any{}
				}
				req.Metadata[ir.MetaGoogleSearchRetrieval] = gsrVal
			}
			if ce := t.Get("codeExecution"); ce.Exists() {
				var ceVal any
				if ce.IsObject() {
					if err := json.Unmarshal([]byte(ce.Raw), &ceVal); err != nil {
						ceVal = map[string]any{}
					}
				} else {
					ceVal = map[string]any{}
				}
				req.Metadata[ir.MetaCodeExecution] = ceVal
			}
			if uc := t.Get("urlContext"); uc.Exists() {
				var ucVal any
				if uc.IsObject() {
					if err := json.Unmarshal([]byte(uc.Raw), &ucVal); err != nil {
						ucVal = map[string]any{}
					}
				} else {
					ucVal = map[string]any{}
				}
				req.Metadata[ir.MetaURLContext] = ucVal
			}
			if fs := t.Get("fileSearch"); fs.Exists() {
				var fsVal any
				if fs.IsObject() {
					if err := json.Unmarshal([]byte(fs.Raw), &fsVal); err != nil {
						fsVal = map[string]any{}
					}
				} else {
					fsVal = map[string]any{}
				}
				req.Metadata[ir.MetaFileSearch] = fsVal
			}
		}
	}

	if req.Metadata == nil {
		req.Metadata = make(map[string]any)
	}
	if v := parsed.Get("cachedContent"); v.Exists() && v.String() != "" {
		req.Metadata[ir.MetaGeminiCachedContent] = v.String()
	}
	if v := parsed.Get("labels"); v.Exists() && v.IsObject() {
		var labels map[string]any
		if json.Unmarshal([]byte(v.Raw), &labels) == nil {
			req.Metadata[ir.MetaGeminiLabels] = labels
		}
	}

	// Parse toolConfig for function calling configuration
	if tc := parsed.Get("toolConfig.functionCallingConfig"); tc.Exists() {
		mode := tc.Get("mode").String()
		if mode != "" {
			// Map Gemini mode to IR ToolChoice
			switch strings.ToUpper(mode) {
			case "AUTO":
				req.ToolChoice = "auto"
			case "ANY":
				req.ToolChoice = "required" // Gemini "ANY" = always call a function
			case "NONE":
				req.ToolChoice = "none"
			case "VALIDATED":
				req.ToolChoice = "validated" // Preserve for outbound
			default:
				req.ToolChoice = strings.ToLower(mode) // Forward-compatible
			}
		}
		// Parse allowedFunctionNames
		if afn := tc.Get("allowedFunctionNames"); afn.Exists() && afn.IsArray() {
			for _, name := range afn.Array() {
				req.AllowedTools = append(req.AllowedTools, name.String())
			}
		}
		// Also check snake_case variant
		if afn := tc.Get("allowed_function_names"); afn.Exists() && afn.IsArray() {
			for _, name := range afn.Array() {
				req.AllowedTools = append(req.AllowedTools, name.String())
			}
		}
	}

	return req, nil
}

// parseGeminiSystemInstruction extracts text from systemInstruction field.
func parseGeminiSystemInstruction(si gjson.Result) string {
	if parts := si.Get("parts"); parts.Exists() && parts.IsArray() {
		var texts []string
		for _, p := range parts.Array() {
			if text := p.Get("text").String(); text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n")
	}
	if si.Type == gjson.String {
		return si.String()
	}
	return ""
}

// parseGeminiContent converts a Gemini content object to IR Message.
func parseGeminiContent(c gjson.Result) ir.Message {
	roleStr := c.Get("role").String()
	role := ir.RoleUser
	if roleStr == "model" {
		role = ir.RoleAssistant
	}

	msg := ir.Message{Role: role}
	parts := c.Get("parts")
	if !parts.Exists() || !parts.IsArray() {
		return msg
	}

	// First pass: collect all functionResponse parts and their associated images
	// Gemini sends functionResponse and inlineData as separate parts in the same message
	type funcResponseInfo struct {
		partIndex int
		id        string
		response  string
	}
	var funcResponses []funcResponseInfo

	for partIdx, part := range parts.Array() {
		if text := part.Get("text"); text.Exists() && text.String() != "" {
			if part.Get("thought").Bool() {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: text.String()})
			} else {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: text.String()})
			}
		}

		if inlineData := part.Get("inlineData"); inlineData.Exists() {
			mimeType := inlineData.Get("mimeType").String()
			if mimeType == "" {
				mimeType = inlineData.Get("mime_type").String()
			}
			data := inlineData.Get("data").String()
			if data != "" {
				// Check if this is part of a user message with functionResponse
				// If we have pending function responses, this image belongs to the last one
				if len(funcResponses) > 0 {
					// Will be attached to the last function response after processing
					continue // Skip adding as standalone image, will be processed below
				}
				// Detect content type by MIME prefix
				if strings.HasPrefix(mimeType, "image/") {
					msg.Content = append(msg.Content, ir.ContentPart{
						Type:  ir.ContentTypeImage,
						Image: &ir.ImagePart{MimeType: mimeType, Data: data},
					})
				} else if strings.HasPrefix(mimeType, "audio/") {
					msg.Content = append(msg.Content, ir.ContentPart{
						Type:  ir.ContentTypeAudio,
						Audio: &ir.AudioPart{MimeType: mimeType, Data: data},
					})
				} else if strings.HasPrefix(mimeType, "video/") {
					msg.Content = append(msg.Content, ir.ContentPart{
						Type:  ir.ContentTypeVideo,
						Video: &ir.VideoPart{MimeType: mimeType, Data: data},
					})
				}
			}
		}

		if fileData := part.Get("fileData"); fileData.Exists() {
			if uri := fileData.Get("fileUri").String(); uri != "" {
				mimeType := fileData.Get("mimeType").String()
				// Check if this is part of a user message with functionResponse
				if len(funcResponses) > 0 {
					// Will be attached to the last function response after processing
					continue // Skip adding as standalone, will be processed below
				}
				// Detect content type by MIME prefix
				if strings.HasPrefix(mimeType, "image/") {
					msg.Content = append(msg.Content, ir.ContentPart{
						Type:  ir.ContentTypeImage,
						Image: &ir.ImagePart{URL: uri, MimeType: mimeType},
					})
				} else if strings.HasPrefix(mimeType, "audio/") {
					msg.Content = append(msg.Content, ir.ContentPart{
						Type:  ir.ContentTypeAudio,
						Audio: &ir.AudioPart{MimeType: mimeType, FileURI: uri},
					})
				} else if strings.HasPrefix(mimeType, "video/") {
					msg.Content = append(msg.Content, ir.ContentPart{
						Type:  ir.ContentTypeVideo,
						Video: &ir.VideoPart{MimeType: mimeType, FileURI: uri},
					})
				}
			}
		}

		if fc := part.Get("functionCall"); fc.Exists() {
			name := fc.Get("name").String()
			args := fc.Get("args").Raw
			if args == "" {
				args = "{}"
			}
			id := fc.Get("id").String()
			// Keep ID as-is (empty or provided) - BuildToolMaps handles legacy format
			// Extract thoughtSignature if present (Gemini format)
			ts := ir.ExtractThoughtSignature(part)
			msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{ID: id, Name: name, Args: args, ThoughtSignature: ts})
		}

		if fr := part.Get("functionResponse"); fr.Exists() {
			// Extract id field for proper tool result matching
			id := fr.Get("id").String()
			name := fr.Get("name").String()
			// Keep ID as-is (empty or provided) - BuildToolMaps handles legacy format
			// Use name as fallback only for the lookup key
			lookupID := id
			if lookupID == "" {
				lookupID = name
			}
			response := fr.Get("response").Raw
			if response == "" {
				response = "{}"
			}
			funcResponses = append(funcResponses, funcResponseInfo{
				partIndex: partIdx,
				id:        lookupID,
				response:  response,
			})
		}
	}

	// Second pass: for each function response, collect images that follow it
	// until the next functionResponse or end of parts
	partsArr := parts.Array()
	for i, fr := range funcResponses {
		toolResult := &ir.ToolResultPart{ToolCallID: fr.id, Result: fr.response}

		// Find the range of parts to check for images
		startIdx := fr.partIndex + 1
		endIdx := len(partsArr)
		if i+1 < len(funcResponses) {
			endIdx = funcResponses[i+1].partIndex
		}

		// Collect images between this functionResponse and the next one
		for j := startIdx; j < endIdx; j++ {
			part := partsArr[j]

			if inlineData := part.Get("inlineData"); inlineData.Exists() {
				mimeType := inlineData.Get("mimeType").String()
				if mimeType == "" {
					mimeType = inlineData.Get("mime_type").String()
				}
				data := inlineData.Get("data").String()
				if data != "" {
					toolResult.Images = append(toolResult.Images, &ir.ImagePart{
						MimeType: mimeType,
						Data:     data,
					})
				}
			}

			if fileData := part.Get("fileData"); fileData.Exists() {
				if uri := fileData.Get("fileUri").String(); uri != "" {
					mimeType := fileData.Get("mimeType").String()
					toolResult.Images = append(toolResult.Images, &ir.ImagePart{
						URL:      uri,
						MimeType: mimeType,
					})
				}
			}
		}

		msg.Content = append(msg.Content, ir.ContentPart{
			Type:       ir.ContentTypeToolResult,
			ToolResult: toolResult,
		})
	}

	return msg
}

// ParseGeminiResponse parses a non-streaming Gemini API response into unified format.
func ParseGeminiResponse(rawJSON []byte) (*ir.UnifiedChatRequest, []ir.Message, *ir.Usage, error) {
	messages, usage, _, err := ParseGeminiResponseMetaWithContext(rawJSON, nil)
	return nil, messages, usage, err
}

// ParseGeminiResponseCandidates parses all candidates from Gemini response.
// Use this when candidateCount > 1 to get multiple alternative responses.
func ParseGeminiResponseCandidates(rawJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.CandidateResult, *ir.Usage, *ir.OpenAIMeta, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil, nil, ir.ErrInvalidJSON
	}

	// Unwrap Antigravity envelope (zero-copy)
	parsed, _ := ir.UnwrapAntigravityEnvelope(rawJSON)
	meta := parseGeminiMeta(parsed)
	usage := parseGeminiUsage(parsed)

	candidates := parsed.Get("candidates").Array()
	if len(candidates) == 0 {
		return nil, usage, meta, nil
	}

	var results []ir.CandidateResult
	for i, candidate := range candidates {
		msg := parseGeminiCandidate(candidate, schemaCtx)
		if msg == nil {
			continue
		}

		finishReason := ir.FinishReasonStop
		if fr := candidate.Get("finishReason"); fr.Exists() {
			finishReason = ir.MapGeminiFinishReason(fr.String())
		}

		// Parse grounding metadata for this candidate
		var groundingMeta *ir.GroundingMetadata
		if gm := candidate.Get("groundingMetadata"); gm.Exists() {
			groundingMeta = parseGroundingMetadata(gm)
		}

		// Parse citation metadata if not already in grounding metadata
		if cm := parseCitationMetadata(candidate); cm != nil {
			if groundingMeta == nil {
				groundingMeta = &ir.GroundingMetadata{}
			}
			groundingMeta.CitationMetadata = cm
		}

		results = append(results, ir.CandidateResult{
			Index:             i,
			Messages:          []ir.Message{*msg},
			FinishReason:      finishReason,
			Logprobs:          parseGeminiLogprobs(candidate),
			GroundingMetadata: groundingMeta,
			SafetyRatings:     parseGeminiSafetyRatings(candidate),
		})
	}

	return results, usage, meta, nil
}

// parseGeminiCandidate parses a single candidate into a Message.
func parseGeminiCandidate(candidate gjson.Result, schemaCtx *ir.ToolSchemaContext) *ir.Message {
	parts := candidate.Get("content.parts").Array()
	if len(parts) == 0 {
		return nil
	}

	msg := &ir.Message{Role: ir.RoleAssistant}
	for _, part := range parts {
		ts := ir.ExtractThoughtSignature(part)

		if text := part.Get("text"); text.Exists() && text.String() != "" {
			if part.Get("thought").Bool() {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: text.String(), ThoughtSignature: ts})
			} else {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: text.String(), ThoughtSignature: ts})
			}
		} else if fc := part.Get("functionCall"); fc.Exists() {
			if name := fc.Get("name").String(); name != "" {
				args := fc.Get("args").Raw
				if args == "" {
					args = "{}"
				}
				if schemaCtx != nil {
					args = schemaCtx.NormalizeToolCallArgs(name, args)
				}
				id := fc.Get("id").String()
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{ID: id, Name: name, Args: args, ThoughtSignature: ts})
			}
		} else if ec := part.Get("executableCode"); ec.Exists() {
			msg.Content = append(msg.Content, ir.ContentPart{
				Type: ir.ContentTypeExecutableCode,
				CodeExecution: &ir.CodeExecutionPart{
					Language: ir.Language(ec.Get("language").String()),
					Code:     ec.Get("code").String(),
				},
				ThoughtSignature: ts,
			})
		} else if cer := part.Get("codeExecutionResult"); cer.Exists() {
			msg.Content = append(msg.Content, ir.ContentPart{
				Type: ir.ContentTypeCodeResult,
				CodeExecution: &ir.CodeExecutionPart{
					Outcome: ir.Outcome(cer.Get("outcome").String()),
					Output:  cer.Get("output").String(),
				},
				ThoughtSignature: ts,
			})
		} else if img := parseGeminiInlineImage(part); img != nil {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: img, ThoughtSignature: ts})
		}
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil
	}
	return msg
}

// ParseGeminiResponseMeta parses a non-streaming Gemini API response into unified format with metadata.
// Returns messages, usage, and response metadata (responseId, createTime, nativeFinishReason).
func ParseGeminiResponseMeta(rawJSON []byte) ([]ir.Message, *ir.Usage, *ir.OpenAIMeta, error) {
	return ParseGeminiResponseMetaWithContext(rawJSON, nil)
}

// ParseGeminiResponseMetaWithContext parses a non-streaming Gemini API response with schema context.
// The schemaCtx parameter allows normalizing tool call parameters based on the original request schema.
func ParseGeminiResponseMetaWithContext(rawJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.Message, *ir.Usage, *ir.OpenAIMeta, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil, nil, ir.ErrInvalidJSON
	}

	// Unwrap Antigravity envelope (zero-copy)
	parsed, _ := ir.UnwrapAntigravityEnvelope(rawJSON)
	meta := parseGeminiMeta(parsed)
	usage := parseGeminiUsage(parsed)

	candidates := parsed.Get("candidates").Array()
	if len(candidates) == 0 {
		return nil, usage, meta, nil
	}

	// Parse grounding metadata from candidates[0] (Gemini 3+) or root level (legacy)
	if gm := candidates[0].Get("groundingMetadata"); gm.Exists() {
		meta.GroundingMetadata = parseGroundingMetadata(gm)
	} else if gm := parsed.Get("groundingMetadata"); gm.Exists() {
		meta.GroundingMetadata = parseGroundingMetadata(gm)
	}

	// Parse prompt feedback
	meta.PromptFeedback = parsePromptFeedback(parsed)

	parts := candidates[0].Get("content.parts").Array()
	if len(parts) == 0 {
		return nil, usage, meta, nil
	}

	msg := ir.Message{Role: ir.RoleAssistant}
	for _, part := range parts {
		// Extract thought signature if present
		ts := ir.ExtractThoughtSignature(part)

		if text := part.Get("text"); text.Exists() && text.String() != "" {
			if part.Get("thought").Bool() {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: text.String(), ThoughtSignature: ts})
			} else {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: text.String(), ThoughtSignature: ts})
			}
		} else if fc := part.Get("functionCall"); fc.Exists() {
			if name := fc.Get("name").String(); name != "" {
				args := fc.Get("args").Raw
				if args == "" {
					args = "{}"
				}
				// Normalize tool call args based on schema context
				if schemaCtx != nil {
					args = schemaCtx.NormalizeToolCallArgs(name, args)
				}
				id := fc.Get("id").String()
				// Don't fallback ID to name here - BuildToolMaps() handles legacy format detection
				// and generates unique IDs when needed for providers that require them (e.g., Claude)
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{ID: id, Name: name, Args: args, ThoughtSignature: ts})
			}
		} else if ec := part.Get("executableCode"); ec.Exists() {
			// Gemini code execution: code to run
			msg.Content = append(msg.Content, ir.ContentPart{
				Type: ir.ContentTypeExecutableCode,
				CodeExecution: &ir.CodeExecutionPart{
					Language: ir.Language(ec.Get("language").String()),
					Code:     ec.Get("code").String(),
				},
				ThoughtSignature: ts,
			})
		} else if cer := part.Get("codeExecutionResult"); cer.Exists() {
			// Gemini code execution: result
			msg.Content = append(msg.Content, ir.ContentPart{
				Type: ir.ContentTypeCodeResult,
				CodeExecution: &ir.CodeExecutionPart{
					Outcome: ir.Outcome(cer.Get("outcome").String()),
					Output:  cer.Get("output").String(),
				},
				ThoughtSignature: ts,
			})
		} else if img := parseGeminiInlineImage(part); img != nil {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: img, ThoughtSignature: ts})
		} else if len(ts) > 0 {
			// Part with only thought signature (and maybe empty text)
			// Preserve it as a reasoning part with empty text
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: "", ThoughtSignature: ts})
		}
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil, usage, meta, nil
	}

	return []ir.Message{msg}, usage, meta, nil
}

// ParseGeminiChunk parses a streaming Gemini API chunk into events.
func ParseGeminiChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	return ParseGeminiChunkWithContext(rawJSON, nil)
}

// ParseGeminiChunkWithContext parses a streaming Gemini API chunk with schema context.
// The schemaCtx parameter allows normalizing tool call parameters based on the original request schema.
func ParseGeminiChunkWithContext(rawJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
	// Handle SSE format: "data: {...}" or "data:{...}"
	rawJSON = ir.ExtractSSEData(rawJSON)
	if len(rawJSON) == 0 {
		return nil, nil
	}
	if string(rawJSON) == "[DONE]" {
		return []ir.UnifiedEvent{{Type: ir.EventTypeFinish}}, nil
	}
	if !gjson.ValidBytes(rawJSON) {
		return nil, ir.ErrInvalidJSON
	}

	// Debug: log raw chunk for analysis
	if debugToolCalls {
		// Only log chunks that contain functionCall to reduce noise
		rawStr := ir.BytesToString(rawJSON)
		if gjson.Get(rawStr, "candidates.0.content.parts.#(functionCall)").Exists() ||
			gjson.Get(rawStr, "response.candidates.0.content.parts.#(functionCall)").Exists() {
			log.Debugf("gemini: RAW CHUNK WITH FUNCTION CALL:\n%s", rawStr)
		}
	}

	// Unwrap Antigravity envelope (zero-copy)
	parsed, _ := ir.UnwrapAntigravityEnvelope(rawJSON)

	var events []ir.UnifiedEvent
	var finishReason ir.FinishReason
	var usage *ir.Usage
	var toolCallIndex int // Track index for multi-tool calls

	// Parse usage metadata if present
	if u := parseGeminiUsage(parsed); u != nil {
		usage = u
	}

	if candidates := parsed.Get("candidates").Array(); len(candidates) > 0 {
		candidate := candidates[0]

		for _, part := range candidate.Get("content.parts").Array() {
			ts := ir.ExtractThoughtSignature(part)

			if text := part.Get("text"); text.Exists() && text.String() != "" {
				// Check for thinking content using multiple field patterns:
				// - "thought": true (standard Gemini format)
				// - presence of "thoughtSummary" (indicates thinking part)
				// - presence of "thoughtSignature" with no other indicators (fallback)
				isThinking := part.Get("thought").Bool() || part.Get("thoughtSummary").Exists()
				if isThinking {
					events = append(events, ir.UnifiedEvent{Type: ir.EventTypeReasoning, Reasoning: text.String(), ThoughtSignature: ts})
				} else {
					events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: text.String(), ThoughtSignature: ts})
				}
			} else if fc := part.Get("functionCall"); fc.Exists() {
				if name := fc.Get("name").String(); name != "" {
					// NOTE: We no longer emit a separate reasoning event for thoughtSignature here.
					// With include_thoughts=true, Gemini sends readable thoughts in separate parts
					// with "thought": true. The signature is preserved in ToolCall.ThoughtSignature
					// for history/context purposes.

					id := fc.Get("id").String()
					// Don't fallback ID to name here - BuildToolMaps() handles legacy format detection
					// and generates unique IDs when needed for providers that require them (e.g., Claude)
					args := fc.Get("args").Raw
					if args == "" {
						args = "{}"
					}

					// Debug: log raw tool call args before normalization
					if debugToolCalls {
						log.Debugf("gemini: RAW TOOL CALL - name=%s id=%s args=%s", name, id, args)
					}

					// Normalize tool call args based on schema context
					if schemaCtx != nil {
						normalizedArgs := schemaCtx.NormalizeToolCallArgs(name, args)
						if debugToolCalls && normalizedArgs != args {
							log.Debugf("gemini: NORMALIZED - name=%s original=%s normalized=%s", name, args, normalizedArgs)
						}
						args = normalizedArgs
					}

					var partialArgs string
					if pa := fc.Get("partialArgs"); pa.Exists() {
						partialArgs = pa.Raw
						// NOTE: Do NOT normalize partialArgs - they are incomplete JSON fragments
						// that cannot be safely parsed or modified. Only normalize complete args.
					}

					events = append(events, ir.UnifiedEvent{
						Type:             ir.EventTypeToolCall,
						ToolCall:         &ir.ToolCall{ID: id, Name: name, Args: args, PartialArgs: partialArgs, ThoughtSignature: ts},
						ToolCallIndex:    toolCallIndex,
						ThoughtSignature: ts,
					})
					toolCallIndex++
				}
			} else if ec := part.Get("executableCode"); ec.Exists() {
				// Gemini code execution: code to run
				events = append(events, ir.UnifiedEvent{
					Type: ir.EventTypeCodeExecution,
					CodeExecution: &ir.CodeExecutionPart{
						Language: ir.Language(ec.Get("language").String()),
						Code:     ec.Get("code").String(),
					},
					ThoughtSignature: ts,
				})
			} else if cer := part.Get("codeExecutionResult"); cer.Exists() {
				// Gemini code execution: result
				events = append(events, ir.UnifiedEvent{
					Type: ir.EventTypeCodeExecution,
					CodeExecution: &ir.CodeExecutionPart{
						Outcome: ir.Outcome(cer.Get("outcome").String()),
						Output:  cer.Get("output").String(),
					},
					ThoughtSignature: ts,
				})
			} else if len(ts) > 0 {
				// Part with only thought signature
				events = append(events, ir.UnifiedEvent{Type: ir.EventTypeReasoning, Reasoning: "", ThoughtSignature: ts})
			}
		}

		if fr := candidate.Get("finishReason"); fr.Exists() {
			frStr := fr.String()
			finishReason = ir.MapGeminiFinishReason(frStr)

			// Handle MALFORMED_FUNCTION_CALL - extract tool call from finishMessage
			if frStr == "MALFORMED_FUNCTION_CALL" {
				if fm := candidate.Get("finishMessage"); fm.Exists() {
					if funcName, argsJSON, ok := ir.ParseMalformedFunctionCall(fm.String()); ok {
						// Normalize args if schema context available
						if schemaCtx != nil {
							argsJSON = schemaCtx.NormalizeToolCallArgs(funcName, argsJSON)
						}
						events = append(events, ir.UnifiedEvent{
							Type: ir.EventTypeToolCall,
							ToolCall: &ir.ToolCall{
								ID:   ir.GenToolCallID(),
								Name: funcName,
								Args: argsJSON,
							},
							ToolCallIndex: toolCallIndex,
						})
						toolCallIndex++
					}
				}
			}
		}
	}

	var groundingMeta *ir.GroundingMetadata
	// groundingMetadata can be at root level OR inside candidates[0]
	if gm := parsed.Get("groundingMetadata"); gm.Exists() {
		groundingMeta = parseGroundingMetadata(gm)
	} else if candidates := parsed.Get("candidates").Array(); len(candidates) > 0 {
		if gm := candidates[0].Get("groundingMetadata"); gm.Exists() {
			groundingMeta = parseGroundingMetadata(gm)
		}
	}

	// Emit Finish event if we have an explicit finish reason from Gemini OR usage metadata (which implies end).
	if finishReason != "" || usage != nil {
		if finishReason == "" {
			finishReason = ir.FinishReasonStop
		}
		// Gemini returns STOP for both normal completion and tool calls.
		// Override to ToolCalls if we have any tool call events in this chunk.
		if finishReason == ir.FinishReasonStop {
			for _, ev := range events {
				if ev.Type == ir.EventTypeToolCall {
					finishReason = ir.FinishReasonToolCalls
					break
				}
			}
		}

		// Parse logprobs from candidate if present
		var logprobs any
		if candidates := parsed.Get("candidates").Array(); len(candidates) > 0 {
			logprobs = parseGeminiLogprobs(candidates[0])
		}

		events = append(events, ir.UnifiedEvent{
			Type:              ir.EventTypeFinish,
			Usage:             usage,
			FinishReason:      finishReason,
			GroundingMetadata: groundingMeta,
			Logprobs:          logprobs,
		})
	}

	return events, nil
}

// parseGeminiSafetyRatings parses safety ratings from a Gemini candidate
func parseGeminiSafetyRatings(candidate gjson.Result) []*ir.SafetyRating {
	ratings := candidate.Get("safetyRatings")
	if !ratings.Exists() || !ratings.IsArray() {
		return nil
	}
	var result []*ir.SafetyRating
	for _, r := range ratings.Array() {
		result = append(result, &ir.SafetyRating{
			Category:    r.Get("category").String(),
			Probability: r.Get("probability").String(),
			Blocked:     r.Get("blocked").Bool(),
			Severity:    r.Get("severity").String(),
		})
	}
	return result
}

// parsePromptFeedback parses prompt-level safety feedback from Gemini response
func parsePromptFeedback(parsed gjson.Result) *ir.PromptFeedback {
	pf := parsed.Get("promptFeedback")
	if !pf.Exists() {
		return nil
	}
	feedback := &ir.PromptFeedback{
		BlockReason: pf.Get("blockReason").String(),
	}
	if ratings := pf.Get("safetyRatings"); ratings.Exists() && ratings.IsArray() {
		for _, r := range ratings.Array() {
			feedback.SafetyRatings = append(feedback.SafetyRatings, &ir.SafetyRating{
				Category:    r.Get("category").String(),
				Probability: r.Get("probability").String(),
			})
		}
	}
	return feedback
}

// parseCitationMetadata parses citation metadata from a Gemini candidate
func parseCitationMetadata(candidate gjson.Result) *ir.CitationMetadata {
	cm := candidate.Get("citationMetadata")
	if !cm.Exists() {
		return nil
	}
	citations := cm.Get("citations")
	if !citations.Exists() || !citations.IsArray() {
		return nil
	}
	meta := &ir.CitationMetadata{}
	for _, c := range citations.Array() {
		meta.Citations = append(meta.Citations, &ir.Citation{
			StartIndex:      int32(c.Get("startIndex").Int()),
			EndIndex:        int32(c.Get("endIndex").Int()),
			URI:             c.Get("uri").String(),
			Title:           c.Get("title").String(),
			License:         c.Get("license").String(),
			PublicationDate: c.Get("publicationDate").String(),
		})
	}
	return meta
}

// parseGroundingMetadata extracts search grounding information from Gemini response.
func parseGroundingMetadata(gm gjson.Result) *ir.GroundingMetadata {
	meta := &ir.GroundingMetadata{}

	// Search entry point (rendered HTML for search widget)
	if sep := gm.Get("searchEntryPoint"); sep.Exists() {
		meta.SearchEntryPoint = &ir.SearchEntryPoint{
			RenderedContent: sep.Get("renderedContent").String(),
		}
	}

	// Grounding chunks (source URLs/titles)
	if chunks := gm.Get("groundingChunks"); chunks.Exists() && chunks.IsArray() {
		for _, chunk := range chunks.Array() {
			gc := ir.GroundingChunk{}
			if web := chunk.Get("web"); web.Exists() {
				gc.Web = &ir.WebGrounding{
					URI:    web.Get("uri").String(),
					Title:  web.Get("title").String(),
					Domain: web.Get("domain").String(),
				}
			}
			meta.GroundingChunks = append(meta.GroundingChunks, &gc)
		}
	}

	// Grounding supports (text segment to source mappings)
	if supports := gm.Get("groundingSupports"); supports.Exists() && supports.IsArray() {
		for _, support := range supports.Array() {
			gs := ir.GroundingSupport{}
			if segment := support.Get("segment"); segment.Exists() {
				gs.Segment = &ir.GroundingSegment{
					StartIndex: int32(segment.Get("startIndex").Int()),
					EndIndex:   int32(segment.Get("endIndex").Int()),
					PartIndex:  int32(segment.Get("partIndex").Int()),
					Text:       segment.Get("text").String(),
				}
			}
			if indices := support.Get("groundingChunkIndices"); indices.Exists() && indices.IsArray() {
				for _, idx := range indices.Array() {
					gs.GroundingChunkIndices = append(gs.GroundingChunkIndices, int32(idx.Int()))
				}
			}
			if scores := support.Get("confidenceScores"); scores.Exists() && scores.IsArray() {
				for _, s := range scores.Array() {
					gs.ConfidenceScores = append(gs.ConfidenceScores, float32(s.Float()))
				}
			}
			meta.GroundingSupports = append(meta.GroundingSupports, &gs)
		}
	}

	// Web search queries
	if queries := gm.Get("webSearchQueries"); queries.Exists() && queries.IsArray() {
		for _, q := range queries.Array() {
			meta.WebSearchQueries = append(meta.WebSearchQueries, q.String())
		}
	}

	// Parse retrievalMetadata
	if rm := gm.Get("retrievalMetadata"); rm.Exists() {
		meta.RetrievalMetadata = &ir.RetrievalMetadata{
			GoogleSearchDynamicRetrievalScore: rm.Get("googleSearchDynamicRetrievalScore").Float(),
		}
	}

	// Parse retrievalQueries
	if rq := gm.Get("retrievalQueries"); rq.Exists() && rq.IsArray() {
		for _, q := range rq.Array() {
			meta.RetrievalQueries = append(meta.RetrievalQueries, q.String())
		}
	}

	// Parse citation metadata
	if cm := gm.Get("citationMetadata"); cm.Exists() {
		citations := cm.Get("citations")
		if citations.Exists() && citations.IsArray() {
			meta.CitationMetadata = &ir.CitationMetadata{}
			for _, c := range citations.Array() {
				meta.CitationMetadata.Citations = append(meta.CitationMetadata.Citations, &ir.Citation{
					StartIndex:      int32(c.Get("startIndex").Int()),
					EndIndex:        int32(c.Get("endIndex").Int()),
					URI:             c.Get("uri").String(),
					Title:           c.Get("title").String(),
					License:         c.Get("license").String(),
					PublicationDate: c.Get("publicationDate").String(),
				})
			}
		}
	}

	return meta
}

// --- Helper Functions ---

func parseGeminiMeta(parsed gjson.Result) *ir.OpenAIMeta {
	meta := &ir.OpenAIMeta{}
	if rid := parsed.Get("responseId"); rid.Exists() {
		meta.ResponseID = rid.String()
	}
	if ct := parsed.Get("createTime"); ct.Exists() {
		if t, err := time.Parse(time.RFC3339Nano, ct.String()); err == nil {
			meta.CreateTime = t.Unix()
		}
	}
	if st := parsed.Get("service_tier"); st.Exists() {
		meta.ServiceTier = st.String()
	}
	if candidates := parsed.Get("candidates").Array(); len(candidates) > 0 {
		candidate := candidates[0]
		if fr := candidate.Get("finishReason"); fr.Exists() {
			meta.NativeFinishReason = fr.String()
		}
		// Parse logprobs from first candidate
		meta.Logprobs = parseGeminiLogprobs(candidate)
	}
	return meta
}

func parseGeminiUsage(parsed gjson.Result) *ir.Usage {
	u := parsed.Get("usageMetadata")
	if !u.Exists() {
		return nil
	}
	promptTokens := u.Get("promptTokenCount").Int()
	thoughtsTokens := int32(u.Get("thoughtsTokenCount").Int())
	usage := &ir.Usage{
		PromptTokens:       promptTokens,
		CompletionTokens:   u.Get("candidatesTokenCount").Int(),
		TotalTokens:        u.Get("totalTokenCount").Int(),
		ThoughtsTokenCount: thoughtsTokens,
	}

	// Parse prompt_tokens_details from Gemini
	if cachedContentTokens := u.Get("cachedContentTokenCount"); cachedContentTokens.Exists() && cachedContentTokens.Int() > 0 {
		if usage.PromptTokensDetails == nil {
			usage.PromptTokensDetails = &ir.PromptTokensDetails{}
		}
		usage.PromptTokensDetails.CachedTokens = cachedContentTokens.Int()
	}

	// Parse tool use tokens (Gemini-specific)
	if toolUseTokens := u.Get("toolUsePromptTokenCount"); toolUseTokens.Exists() && toolUseTokens.Int() > 0 {
		usage.ToolUsePromptTokens = toolUseTokens.Int()
	}

	// Parse completion_tokens_details from Gemini
	// Gemini returns reasoning tokens as part of candidatesTokenCount
	if thoughtsTokens > 0 {
		if usage.CompletionTokensDetails == nil {
			usage.CompletionTokensDetails = &ir.CompletionTokensDetails{}
		}
		usage.CompletionTokensDetails.ReasoningTokens = int64(thoughtsTokens)
	}

	return usage
}

// parseGeminiLogprobs extracts logprobs from Gemini candidate and converts to OpenAI format.
// Gemini returns avgLogprobs (float) and logprobsResult (detailed per-token).
func parseGeminiLogprobs(candidate gjson.Result) any {
	// Check if logprobsResult exists (detailed per-token logprobs)
	if lr := candidate.Get("logprobsResult"); lr.Exists() {
		return convertGeminiLogprobsToOpenAI(lr)
	}
	// Fall back to avgLogprobs if available
	if avg := candidate.Get("avgLogprobs"); avg.Exists() {
		// OpenAI doesn't have avgLogprobs equivalent, but we can include it
		return map[string]any{
			"avg_logprob": avg.Float(),
		}
	}
	return nil
}

// convertGeminiLogprobsToOpenAI converts Gemini logprobsResult to OpenAI logprobs format.
// Gemini format: {"chosenCandidates": [{"token": "...", "logProbability": -0.5, ...}], "topCandidates": [...]}
// OpenAI format: {"content": [{"token": "...", "logprob": -0.5, "top_logprobs": [...]}]}
func convertGeminiLogprobsToOpenAI(lr gjson.Result) map[string]any {
	var content []any

	// Parse chosen candidates (the actual tokens in the response)
	chosenCandidates := lr.Get("chosenCandidates")
	topCandidates := lr.Get("topCandidates")

	if chosenCandidates.Exists() && chosenCandidates.IsArray() {
		for i, chosen := range chosenCandidates.Array() {
			tokenEntry := map[string]any{
				"token":   chosen.Get("token").String(),
				"logprob": chosen.Get("logProbability").Float(),
			}

			// Add bytes field for non-text tokens
			if bytes := chosen.Get("bytes"); bytes.Exists() && bytes.IsArray() {
				var byteSlice []int
				for _, b := range bytes.Array() {
					byteSlice = append(byteSlice, int(b.Int()))
				}
				tokenEntry["bytes"] = byteSlice
			}

			// Add top_logprobs if available
			if topCandidates.Exists() && topCandidates.IsArray() {
				topArr := topCandidates.Array()
				if i < len(topArr) {
					var topLogprobs []any
					if candidates := topArr[i].Get("candidates"); candidates.Exists() && candidates.IsArray() {
						for _, c := range candidates.Array() {
							topLogprobs = append(topLogprobs, map[string]any{
								"token":   c.Get("token").String(),
								"logprob": c.Get("logProbability").Float(),
							})
						}
					}
					if len(topLogprobs) > 0 {
						tokenEntry["top_logprobs"] = topLogprobs
					}
				}
			}

			content = append(content, tokenEntry)
		}
	}

	if len(content) == 0 {
		return nil
	}
	return map[string]any{"content": content}
}

func parseGeminiInlineImage(part gjson.Result) *ir.ImagePart {
	inlineData := part.Get("inlineData")
	if !inlineData.Exists() {
		inlineData = part.Get("inline_data")
	}
	if !inlineData.Exists() {
		return nil
	}
	data := inlineData.Get("data").String()
	if data == "" {
		return nil
	}
	mimeType := inlineData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inlineData.Get("mime_type").String()
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	return &ir.ImagePart{MimeType: mimeType, Data: data}
}
