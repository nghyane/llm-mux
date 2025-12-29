package to_ir

import (
	"os"
	"strings"
	"time"

	"github.com/tidwall/gjson"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

var debugToolCalls = os.Getenv("DEBUG_TOOL_CALLS") == "1"

func ParseGeminiRequest(rawJSON []byte) (*ir.UnifiedChatRequest, error) {
	parsed, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, err
	}

	if requestWrapper := parsed.Get("request"); requestWrapper.Exists() {
		parsed = requestWrapper
	}

	req := &ir.UnifiedChatRequest{
		Model: parsed.Get("model").String(),
	}

	if gc := parsed.Get("generationConfig"); gc.Exists() {
		if v := gc.Get("maxOutputTokens"); v.Exists() {
			req.MaxTokens = ir.Ptr(int(v.Int()))
		}
		if v := gc.Get("temperature"); v.Exists() {
			req.Temperature = ir.Ptr(v.Float())
		}
		if v := gc.Get("topP"); v.Exists() {
			req.TopP = ir.Ptr(v.Float())
		}
		if v := gc.Get("topK"); v.Exists() {
			req.TopK = ir.Ptr(int(v.Int()))
		}
		for _, s := range gc.Get("stopSequences").Array() {
			req.StopSequences = append(req.StopSequences, s.String())
		}

		if tc := gc.Get("thinkingConfig"); tc.Exists() {
			req.Thinking = &ir.ThinkingConfig{
				ThinkingBudget:  ir.Ptr(int32(tc.Get("thinkingBudget").Int())),
				IncludeThoughts: tc.Get("includeThoughts").Bool(),
				ThinkingLevel:   ir.ThinkingLevel(tc.Get("thinkingLevel").String()),
			}
		}

		for _, m := range gc.Get("responseModalities").Array() {
			req.ResponseModality = append(req.ResponseModality, m.String())
		}

		rs := gc.Get("responseJsonSchema")
		if !rs.Exists() {
			rs = gc.Get("responseSchema")
		}
		if rs.Exists() && rs.IsObject() {
			var schema map[string]any
			if err := json.Unmarshal([]byte(rs.Raw), &schema); err == nil {
				req.ResponseSchema = schema
			}
		}
	}

	if si := parsed.Get("systemInstruction"); si.Exists() {
		if text := parseGeminiSystemInstruction(si); text != "" {
			req.Messages = append(req.Messages, ir.Message{
				Role:    ir.RoleSystem,
				Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: text}},
			})
		}
	}

	for _, c := range parsed.Get("contents").Array() {
		if msg := parseGeminiContent(c); msg.Role != "" {
			req.Messages = append(req.Messages, msg)
		}
	}

	req.Metadata = make(map[string]any)
	for _, t := range parsed.Get("tools").Array() {
		fds := t.Get("functionDeclarations")
		if !fds.Exists() {
			fds = t.Get("function_declarations")
		}
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

		toolKeys := map[string]string{
			"googleSearch":          ir.MetaGoogleSearch,
			"googleSearchRetrieval": ir.MetaGoogleSearchRetrieval,
			"codeExecution":         ir.MetaCodeExecution,
			"urlContext":            ir.MetaURLContext,
			"fileSearch":            ir.MetaFileSearch,
		}
		for k, metaKey := range toolKeys {
			if val := t.Get(k); val.Exists() {
				var v any
				if val.IsObject() {
					json.Unmarshal([]byte(val.Raw), &v)
				} else {
					v = map[string]any{}
				}
				req.Metadata[metaKey] = v
			}
		}
	}

	if v := parsed.Get("cachedContent").String(); v != "" {
		req.Metadata[ir.MetaGeminiCachedContent] = v
	}
	if v := parsed.Get("labels"); v.Exists() && v.IsObject() {
		var labels map[string]any
		if json.Unmarshal([]byte(v.Raw), &labels) == nil {
			req.Metadata[ir.MetaGeminiLabels] = labels
		}
	}

	if tc := parsed.Get("toolConfig.functionCallingConfig"); tc.Exists() {
		mode := strings.ToUpper(tc.Get("mode").String())
		switch mode {
		case "AUTO":
			req.ToolChoice = "auto"
		case "ANY":
			req.ToolChoice = "required"
		case "NONE":
			req.ToolChoice = "none"
		default:
			req.ToolChoice = strings.ToLower(mode)
		}
		for _, name := range tc.Get("allowedFunctionNames").Array() {
			req.AllowedTools = append(req.AllowedTools, name.String())
		}
		for _, name := range tc.Get("allowed_function_names").Array() {
			req.AllowedTools = append(req.AllowedTools, name.String())
		}
	}

	return req, nil
}

func parseGeminiSystemInstruction(si gjson.Result) string {
	if si.Type == gjson.String {
		return si.String()
	}
	var texts []string
	for _, p := range si.Get("parts").Array() {
		if text := p.Get("text").String(); text != "" {
			texts = append(texts, text)
		}
	}
	return strings.Join(texts, "\n")
}

func parseGeminiContent(c gjson.Result) ir.Message {
	role := ir.RoleUser
	if c.Get("role").String() == "model" {
		role = ir.RoleAssistant
	}

	msg := ir.Message{Role: role}
	parts := c.Get("parts").Array()
	if len(parts) == 0 {
		return msg
	}

	type funcResponseInfo struct {
		idx      int
		id       string
		response string
	}
	var funcResponses []funcResponseInfo

	for i, part := range parts {
		ts := ir.ExtractThoughtSignature(part)
		text := part.Get("text").String()
		isThought := part.Get("thought").Bool()

		if text != "" {
			if isThought {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: text, ThoughtSignature: ts})
			} else {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: text, ThoughtSignature: ts})
			}
		} else if isThought && len(ts) > 0 {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: "", ThoughtSignature: ts})
		}

		if data := part.Get("data").String(); data != "" {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeRedactedThinking, RedactedData: data})
		}

		if inlineData := part.Get("inlineData"); !inlineData.Exists() {
			inlineData = part.Get("inline_data")
		} else if inlineData.Exists() {
			mimeType := inlineData.Get("mimeType").String()
			if mimeType == "" {
				mimeType = inlineData.Get("mime_type").String()
			}
			data := inlineData.Get("data").String()
			if data != "" {
				if len(funcResponses) > 0 {
					continue
				}
				if strings.HasPrefix(mimeType, "image/") {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{MimeType: mimeType, Data: data}})
				} else if strings.HasPrefix(mimeType, "audio/") {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeAudio, Audio: &ir.AudioPart{MimeType: mimeType, Data: data}})
				} else if strings.HasPrefix(mimeType, "video/") {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeVideo, Video: &ir.VideoPart{MimeType: mimeType, Data: data}})
				}
			}
		}

		if fileData := part.Get("fileData"); fileData.Exists() {
			uri := fileData.Get("fileUri").String()
			mimeType := fileData.Get("mimeType").String()
			if uri != "" {
				if len(funcResponses) > 0 {
					continue
				}
				if strings.HasPrefix(mimeType, "image/") {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeImage, Image: &ir.ImagePart{URL: uri, MimeType: mimeType}})
				} else if strings.HasPrefix(mimeType, "audio/") {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeAudio, Audio: &ir.AudioPart{MimeType: mimeType, FileURI: uri}})
				} else if strings.HasPrefix(mimeType, "video/") {
					msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeVideo, Video: &ir.VideoPart{MimeType: mimeType, FileURI: uri}})
				}
			}
		}

		if fc := part.Get("functionCall"); fc.Exists() {
			name := fc.Get("name").String()
			args := fc.Get("args").Raw
			if args == "" {
				args = "{}"
			}
			msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
				ID:               fc.Get("id").String(),
				Name:             name,
				Args:             args,
				ThoughtSignature: ir.ExtractThoughtSignature(part),
			})
		}

		if fr := part.Get("functionResponse"); fr.Exists() {
			id := fr.Get("id").String()
			if id == "" {
				id = fr.Get("name").String()
			}
			resp := fr.Get("response").Raw
			if resp == "" {
				resp = "{}"
			}
			funcResponses = append(funcResponses, funcResponseInfo{idx: i, id: id, response: resp})
		}
	}

	for i, fr := range funcResponses {
		toolResult := &ir.ToolResultPart{ToolCallID: fr.id, Result: fr.response}
		start := fr.idx + 1
		end := len(parts)
		if i+1 < len(funcResponses) {
			end = funcResponses[i+1].idx
		}

		for j := start; j < end; j++ {
			p := parts[j]
			if data := p.Get("inlineData"); data.Exists() {
				toolResult.Images = append(toolResult.Images, &ir.ImagePart{
					MimeType: data.Get("mimeType").String(),
					Data:     data.Get("data").String(),
				})
			}
			if data := p.Get("fileData"); data.Exists() {
				toolResult.Images = append(toolResult.Images, &ir.ImagePart{
					URL:      data.Get("fileUri").String(),
					MimeType: data.Get("mimeType").String(),
				})
			}
		}
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeToolResult, ToolResult: toolResult})
	}

	return msg
}

func ParseGeminiResponse(rawJSON []byte) (*ir.UnifiedChatRequest, []ir.Message, *ir.Usage, error) {
	messages, usage, _, err := ParseGeminiResponseMetaWithContext(rawJSON, nil)
	return nil, messages, usage, err
}

func ParseGeminiResponseCandidates(rawJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.CandidateResult, *ir.Usage, *ir.OpenAIMeta, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil, nil, ir.ErrInvalidJSON
	}

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

		var groundingMeta *ir.GroundingMetadata
		if gm := candidate.Get("groundingMetadata"); gm.Exists() {
			groundingMeta = parseGroundingMetadata(gm)
		}
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

func parseGeminiCandidate(candidate gjson.Result, schemaCtx *ir.ToolSchemaContext) *ir.Message {
	parts := candidate.Get("content.parts").Array()
	if len(parts) == 0 {
		return nil
	}

	msg := &ir.Message{Role: ir.RoleAssistant}
	for _, part := range parts {
		ts := ir.ExtractThoughtSignature(part)
		isThought := part.Get("thought").Bool()

		if text := part.Get("text"); text.Exists() && text.String() != "" {
			if isThought {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: text.String(), ThoughtSignature: ts})
			} else {
				msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: text.String(), ThoughtSignature: ts})
			}
		} else if fc := part.Get("functionCall"); fc.Exists() {
			name := fc.Get("name").String()
			if name != "" {
				args := fc.Get("args").Raw
				if args == "" {
					args = "{}"
				}
				if schemaCtx != nil {
					args = schemaCtx.NormalizeToolCallArgs(name, args)
				}
				msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{ID: fc.Get("id").String(), Name: name, Args: args, ThoughtSignature: ts})
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
		} else if len(ts) > 0 {
			msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeReasoning, Reasoning: "", ThoughtSignature: ts})
		}
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil
	}
	return msg
}

func ParseGeminiResponseMeta(rawJSON []byte) ([]ir.Message, *ir.Usage, *ir.OpenAIMeta, error) {
	return ParseGeminiResponseMetaWithContext(rawJSON, nil)
}

func ParseGeminiResponseMetaWithContext(rawJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.Message, *ir.Usage, *ir.OpenAIMeta, error) {
	if !gjson.ValidBytes(rawJSON) {
		return nil, nil, nil, ir.ErrInvalidJSON
	}

	parsed, _ := ir.UnwrapAntigravityEnvelope(rawJSON)
	meta := parseGeminiMeta(parsed)
	usage := parseGeminiUsage(parsed)

	candidates := parsed.Get("candidates").Array()
	if len(candidates) == 0 {
		return nil, usage, meta, nil
	}

	if gm := candidates[0].Get("groundingMetadata"); gm.Exists() {
		meta.GroundingMetadata = parseGroundingMetadata(gm)
	} else if gm := parsed.Get("groundingMetadata"); gm.Exists() {
		meta.GroundingMetadata = parseGroundingMetadata(gm)
	}

	meta.PromptFeedback = parsePromptFeedback(parsed)

	msg := parseGeminiCandidate(candidates[0], schemaCtx)
	if msg == nil {
		return nil, usage, meta, nil
	}

	return []ir.Message{*msg}, usage, meta, nil
}

func ParseGeminiChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	return ParseGeminiChunkWithContext(rawJSON, nil)
}

func ParseGeminiChunkWithContext(rawJSON []byte, schemaCtx *ir.ToolSchemaContext) ([]ir.UnifiedEvent, error) {
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

	parsed, _ := ir.UnwrapAntigravityEnvelope(rawJSON)

	var events []ir.UnifiedEvent
	var finishReason ir.FinishReason
	var toolCallIndex int

	usage := parseGeminiUsage(parsed)

	if candidates := parsed.Get("candidates").Array(); len(candidates) > 0 {
		candidate := candidates[0]

		for _, part := range candidate.Get("content.parts").Array() {
			ts := ir.ExtractThoughtSignature(part)

			if text := part.Get("text"); text.Exists() && text.String() != "" {
				if part.Get("thought").Bool() || part.Get("thoughtSummary").Exists() {
					events = append(events, ir.UnifiedEvent{Type: ir.EventTypeReasoning, Reasoning: text.String(), ThoughtSignature: ts})
				} else {
					events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: text.String(), ThoughtSignature: ts})
				}
			} else if fc := part.Get("functionCall"); fc.Exists() {
				name := fc.Get("name").String()
				if name != "" {
					id := fc.Get("id").String()
					args := fc.Get("args").Raw
					if args == "" {
						args = "{}"
					}
					if schemaCtx != nil {
						args = schemaCtx.NormalizeToolCallArgs(name, args)
					}
					var partialArgs string
					if pa := fc.Get("partialArgs"); pa.Exists() {
						partialArgs = pa.Raw
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
				events = append(events, ir.UnifiedEvent{
					Type: ir.EventTypeCodeExecution,
					CodeExecution: &ir.CodeExecutionPart{
						Language: ir.Language(ec.Get("language").String()),
						Code:     ec.Get("code").String(),
					},
					ThoughtSignature: ts,
				})
			} else if cer := part.Get("codeExecutionResult"); cer.Exists() {
				events = append(events, ir.UnifiedEvent{
					Type: ir.EventTypeCodeExecution,
					CodeExecution: &ir.CodeExecutionPart{
						Outcome: ir.Outcome(cer.Get("outcome").String()),
						Output:  cer.Get("output").String(),
					},
					ThoughtSignature: ts,
				})
			} else if len(ts) > 0 {
				events = append(events, ir.UnifiedEvent{Type: ir.EventTypeReasoning, Reasoning: "", ThoughtSignature: ts})
			}
		}

		if fr := candidate.Get("finishReason"); fr.Exists() {
			frStr := fr.String()
			finishReason = ir.MapGeminiFinishReason(frStr)

			if frStr == "MALFORMED_FUNCTION_CALL" {
				if fm := candidate.Get("finishMessage"); fm.Exists() {
					if funcName, argsJSON, ok := ir.ParseMalformedFunctionCall(fm.String()); ok {
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
	if gm := parsed.Get("groundingMetadata"); gm.Exists() {
		groundingMeta = parseGroundingMetadata(gm)
	} else if candidates := parsed.Get("candidates").Array(); len(candidates) > 0 {
		if gm := candidates[0].Get("groundingMetadata"); gm.Exists() {
			groundingMeta = parseGroundingMetadata(gm)
		}
	}

	if finishReason != "" || usage != nil {
		if finishReason == "" {
			finishReason = ir.FinishReasonStop
		}
		if finishReason == ir.FinishReasonStop {
			for _, ev := range events {
				if ev.Type == ir.EventTypeToolCall {
					finishReason = ir.FinishReasonToolCalls
					break
				}
			}
		}

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

func parseGeminiSafetyRatings(candidate gjson.Result) []*ir.SafetyRating {
	ratings := candidate.Get("safetyRatings").Array()
	if len(ratings) == 0 {
		return nil
	}
	var result []*ir.SafetyRating
	for _, r := range ratings {
		result = append(result, &ir.SafetyRating{
			Category:    r.Get("category").String(),
			Probability: r.Get("probability").String(),
			Blocked:     r.Get("blocked").Bool(),
			Severity:    r.Get("severity").String(),
		})
	}
	return result
}

func parsePromptFeedback(parsed gjson.Result) *ir.PromptFeedback {
	pf := parsed.Get("promptFeedback")
	if !pf.Exists() {
		return nil
	}
	feedback := &ir.PromptFeedback{
		BlockReason: pf.Get("blockReason").String(),
	}
	for _, r := range pf.Get("safetyRatings").Array() {
		feedback.SafetyRatings = append(feedback.SafetyRatings, &ir.SafetyRating{
			Category:    r.Get("category").String(),
			Probability: r.Get("probability").String(),
		})
	}
	return feedback
}

func parseCitationMetadata(candidate gjson.Result) *ir.CitationMetadata {
	cm := candidate.Get("citationMetadata")
	if !cm.Exists() {
		return nil
	}
	citations := cm.Get("citations").Array()
	if len(citations) == 0 {
		return nil
	}
	meta := &ir.CitationMetadata{}
	for _, c := range citations {
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

func parseGroundingMetadata(gm gjson.Result) *ir.GroundingMetadata {
	meta := &ir.GroundingMetadata{}
	if sep := gm.Get("searchEntryPoint"); sep.Exists() {
		meta.SearchEntryPoint = &ir.SearchEntryPoint{RenderedContent: sep.Get("renderedContent").String()}
	}

	for _, chunk := range gm.Get("groundingChunks").Array() {
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

	for _, support := range gm.Get("groundingSupports").Array() {
		gs := ir.GroundingSupport{}
		if segment := support.Get("segment"); segment.Exists() {
			gs.Segment = &ir.GroundingSegment{
				StartIndex: int32(segment.Get("startIndex").Int()),
				EndIndex:   int32(segment.Get("endIndex").Int()),
				PartIndex:  int32(segment.Get("partIndex").Int()),
				Text:       segment.Get("text").String(),
			}
		}
		for _, idx := range support.Get("groundingChunkIndices").Array() {
			gs.GroundingChunkIndices = append(gs.GroundingChunkIndices, int32(idx.Int()))
		}
		for _, s := range support.Get("confidenceScores").Array() {
			gs.ConfidenceScores = append(gs.ConfidenceScores, float32(s.Float()))
		}
		meta.GroundingSupports = append(meta.GroundingSupports, &gs)
	}

	for _, q := range gm.Get("webSearchQueries").Array() {
		meta.WebSearchQueries = append(meta.WebSearchQueries, q.String())
	}
	if rm := gm.Get("retrievalMetadata"); rm.Exists() {
		meta.RetrievalMetadata = &ir.RetrievalMetadata{GoogleSearchDynamicRetrievalScore: rm.Get("googleSearchDynamicRetrievalScore").Float()}
	}
	for _, q := range gm.Get("retrievalQueries").Array() {
		meta.RetrievalQueries = append(meta.RetrievalQueries, q.String())
	}

	if cm := gm.Get("citationMetadata"); cm.Exists() {
		meta.CitationMetadata = &ir.CitationMetadata{}
		for _, c := range cm.Get("citations").Array() {
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
	return meta
}

func parseGeminiMeta(parsed gjson.Result) *ir.OpenAIMeta {
	meta := &ir.OpenAIMeta{
		ResponseID: parsed.Get("responseId").String(),
	}
	if ct := parsed.Get("createTime").String(); ct != "" {
		if t, err := time.Parse(time.RFC3339Nano, ct); err == nil {
			meta.CreateTime = t.Unix()
		}
	}
	meta.ServiceTier = parsed.Get("service_tier").String()
	if candidates := parsed.Get("candidates").Array(); len(candidates) > 0 {
		meta.NativeFinishReason = candidates[0].Get("finishReason").String()
		meta.Logprobs = parseGeminiLogprobs(candidates[0])
	}
	return meta
}

func parseGeminiUsage(parsed gjson.Result) *ir.Usage {
	u := parsed.Get("usageMetadata")
	if !u.Exists() {
		return nil
	}
	thoughtsTokens := int32(u.Get("thoughtsTokenCount").Int())
	usage := &ir.Usage{
		PromptTokens:       u.Get("promptTokenCount").Int(),
		CompletionTokens:   u.Get("candidatesTokenCount").Int(),
		TotalTokens:        u.Get("totalTokenCount").Int(),
		ThoughtsTokenCount: thoughtsTokens,
	}

	if tokens := u.Get("cachedContentTokenCount").Int(); tokens > 0 {
		usage.PromptTokensDetails = &ir.PromptTokensDetails{CachedTokens: tokens}
	}
	if tokens := u.Get("toolUsePromptTokenCount").Int(); tokens > 0 {
		usage.ToolUsePromptTokens = tokens
	}
	if thoughtsTokens > 0 {
		usage.CompletionTokensDetails = &ir.CompletionTokensDetails{ReasoningTokens: int64(thoughtsTokens)}
	}
	return usage
}

func parseGeminiLogprobs(candidate gjson.Result) any {
	if lr := candidate.Get("logprobsResult"); lr.Exists() {
		return convertGeminiLogprobsToOpenAI(lr)
	}
	if avg := candidate.Get("avgLogprobs"); avg.Exists() {
		return map[string]any{"avg_logprob": avg.Float()}
	}
	return nil
}

func convertGeminiLogprobsToOpenAI(lr gjson.Result) map[string]any {
	var content []any
	chosenCandidates := lr.Get("chosenCandidates").Array()
	topCandidates := lr.Get("topCandidates").Array()

	for i, chosen := range chosenCandidates {
		tokenEntry := map[string]any{
			"token":   chosen.Get("token").String(),
			"logprob": chosen.Get("logProbability").Float(),
		}
		if bytes := chosen.Get("bytes").Array(); len(bytes) > 0 {
			var byteSlice []int
			for _, b := range bytes {
				byteSlice = append(byteSlice, int(b.Int()))
			}
			tokenEntry["bytes"] = byteSlice
		}
		if i < len(topCandidates) {
			var topLogprobs []any
			for _, c := range topCandidates[i].Get("candidates").Array() {
				topLogprobs = append(topLogprobs, map[string]any{
					"token":   c.Get("token").String(),
					"logprob": c.Get("logProbability").Float(),
				})
			}
			if len(topLogprobs) > 0 {
				tokenEntry["top_logprobs"] = topLogprobs
			}
		}
		content = append(content, tokenEntry)
	}
	if len(content) == 0 {
		return nil
	}
	return map[string]any{"content": content}
}

func parseGeminiInlineImage(part gjson.Result) *ir.ImagePart {
	data := part.Get("inlineData")
	if !data.Exists() {
		data = part.Get("inline_data")
	}
	if !data.Exists() {
		return nil
	}
	mimeType := data.Get("mimeType").String()
	if mimeType == "" {
		mimeType = data.Get("mime_type").String()
	}
	if mimeType == "" {
		mimeType = "image/png"
	}
	return &ir.ImagePart{MimeType: mimeType, Data: data.Get("data").String()}
}

func MergeConsecutiveModelThinking(messages []ir.Message) []ir.Message {
	if len(messages) < 2 {
		return messages
	}

	result := make([]ir.Message, 0, len(messages))
	i := 0
	for i < len(messages) {
		msg := messages[i]
		if msg.Role != ir.RoleAssistant || !isThinkingOnlyMessage(msg) {
			result = append(result, msg)
			i++
			continue
		}

		mergeStart, mergeEnd := i, i
		for j := i + 1; j < len(messages); j++ {
			next := messages[j]
			if next.Role != ir.RoleAssistant {
				break
			}
			if isThinkingOnlyMessage(next) || isFinalThinkingMessage(next) {
				mergeEnd = j
				if len(next.ToolCalls) > 0 || hasNonReasoningText(next) {
					break
				}
			} else {
				break
			}
		}

		if mergeEnd == mergeStart {
			result = append(result, msg)
			i++
		} else {
			result = append(result, mergeThinkingMessages(messages[mergeStart:mergeEnd+1]))
			i = mergeEnd + 1
		}
	}
	return result
}

func isThinkingOnlyMessage(msg ir.Message) bool {
	if len(msg.ToolCalls) > 0 {
		return false
	}
	if len(msg.Content) == 0 {
		return true
	}
	hasThinking := false
	for _, part := range msg.Content {
		switch part.Type {
		case ir.ContentTypeReasoning, ir.ContentTypeRedactedThinking:
			hasThinking = true
		case ir.ContentTypeText:
			if part.Text == "" && len(part.ThoughtSignature) > 0 {
				hasThinking = true
			} else if part.Text != "" {
				return false
			}
		default:
			return false
		}
	}
	return hasThinking
}

func isFinalThinkingMessage(msg ir.Message) bool {
	if len(msg.ToolCalls) > 0 {
		return true
	}
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeReasoning || part.Type == ir.ContentTypeRedactedThinking ||
			(part.Type == ir.ContentTypeText && part.Text == "" && len(part.ThoughtSignature) > 0) {
			return true
		}
	}
	return false
}

func hasNonReasoningText(msg ir.Message) bool {
	for _, part := range msg.Content {
		if part.Type == ir.ContentTypeText && part.Text != "" {
			return true
		}
	}
	return false
}

func mergeThinkingMessages(messages []ir.Message) ir.Message {
	if len(messages) == 1 {
		return messages[0]
	}
	merged := ir.Message{Role: ir.RoleAssistant}
	var buffer []string
	var sig []byte

	flush := func() {
		if len(buffer) > 0 || len(sig) > 0 {
			merged.Content = append(merged.Content, ir.ContentPart{
				Type:             ir.ContentTypeReasoning,
				Reasoning:        strings.Join(buffer, ""),
				ThoughtSignature: sig,
			})
			buffer, sig = nil, nil
		}
	}

	for _, msg := range messages {
		for _, part := range msg.Content {
			switch part.Type {
			case ir.ContentTypeReasoning:
				if part.Reasoning != "" {
					buffer = append(buffer, part.Reasoning)
				}
				if len(part.ThoughtSignature) > 0 {
					sig = part.ThoughtSignature
				}
			case ir.ContentTypeRedactedThinking:
				flush()
				merged.Content = append(merged.Content, part)
			case ir.ContentTypeText:
				if part.Text == "" && len(part.ThoughtSignature) > 0 {
					sig = part.ThoughtSignature
				} else if part.Text != "" {
					flush()
					merged.Content = append(merged.Content, part)
				}
			default:
				flush()
				merged.Content = append(merged.Content, part)
			}
		}
		for _, tc := range msg.ToolCalls {
			if len(tc.ThoughtSignature) > 0 && len(sig) == 0 {
				sig = tc.ThoughtSignature
			}
			merged.ToolCalls = append(merged.ToolCalls, tc)
		}
	}
	flush()

	var lastSig []byte
	for i := len(merged.Content) - 1; i >= 0; i-- {
		if len(merged.Content[i].ThoughtSignature) > 0 {
			lastSig = merged.Content[i].ThoughtSignature
			break
		}
	}
	if len(lastSig) > 0 {
		for i := range merged.ToolCalls {
			if len(merged.ToolCalls[i].ThoughtSignature) == 0 {
				merged.ToolCalls[i].ThoughtSignature = lastSig
			}
		}
	}
	return merged
}
