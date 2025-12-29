package executor

import (
	"bytes"
	"context"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/nghyane/llm-mux/internal/wsrelay"
	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type AIStudioExecutor struct {
	provider string
	relay    *wsrelay.Manager
	cfg      *config.Config
}

func NewAIStudioExecutor(cfg *config.Config, provider string, relay *wsrelay.Manager) *AIStudioExecutor {
	return &AIStudioExecutor{provider: strings.ToLower(provider), relay: relay, cfg: cfg}
}

func (e *AIStudioExecutor) Identifier() string { return "aistudio" }

func (e *AIStudioExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

func (e *AIStudioExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	_, body, err := e.translateRequest(req, opts, false)
	if err != nil {
		return resp, err
	}
	endpoint := e.buildEndpoint(req.Model, body.action, opts.Alt)
	wsReq := &wsrelay.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body.payload,
	}

	var authID string
	if auth != nil {
		authID = auth.ID
	}

	wsResp, err := e.relay.NonStream(ctx, authID, wsReq)
	if err != nil {
		return resp, err
	}
	if wsResp.Status < 200 || wsResp.Status >= 300 {
		return resp, NewStatusError(wsResp.Status, string(wsResp.Body), nil)
	}
	reporter.publish(ctx, extractUsageFromGeminiResponse(wsResp.Body))

	fromFormat := sdktranslator.FromString("gemini")
	translatedResp, err := TranslateResponseNonStream(e.cfg, fromFormat, opts.SourceFormat, wsResp.Body, req.Model)
	if err != nil {
		return resp, err
	}
	if translatedResp != nil {
		resp = cliproxyexecutor.Response{Payload: ensureColonSpacedJSON(translatedResp)}
	} else {
		resp = cliproxyexecutor.Response{Payload: ensureColonSpacedJSON(wsResp.Body)}
	}
	return resp, nil
}

func (e *AIStudioExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	body, estimatedInputTokens, err := e.translateRequestWithTokens(req, opts, true)
	if err != nil {
		return nil, err
	}

	endpoint := e.buildEndpoint(req.Model, body.action, opts.Alt)
	wsReq := &wsrelay.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body.payload,
	}
	var authID string
	if auth != nil {
		authID = auth.ID
	}
	wsStream, err := e.relay.Stream(ctx, authID, wsReq)
	if err != nil {
		return nil, err
	}
	firstEvent, ok := <-wsStream
	if !ok {
		err = fmt.Errorf("wsrelay: stream closed before start")
		return nil, err
	}
	if firstEvent.Status > 0 && firstEvent.Status != http.StatusOK {
		var body bytes.Buffer
		if len(firstEvent.Payload) > 0 {
			body.Write(firstEvent.Payload)
		}
		if firstEvent.Type == wsrelay.MessageTypeStreamEnd {
			return nil, NewStatusError(firstEvent.Status, body.String(), nil)
		}
		for event := range wsStream {
			if event.Err != nil {
				if body.Len() == 0 {
					body.WriteString(event.Err.Error())
				}
				break
			}
			if len(event.Payload) > 0 {
				body.Write(event.Payload)
			}
			if event.Type == wsrelay.MessageTypeStreamEnd {
				break
			}
		}
		return nil, NewStatusError(firstEvent.Status, body.String(), nil)
	}
	out := make(chan cliproxyexecutor.StreamChunk, 8)
	stream = out

	go func(first wsrelay.StreamEvent, inputTokens int64) {
		defer close(out)

		streamCtx := NewStreamContext()
		streamCtx.EstimatedInputTokens = inputTokens
		messageID := "chatcmpl-" + req.Model
		translator := NewStreamTranslator(e.cfg, opts.SourceFormat, opts.SourceFormat.String(), req.Model, messageID, streamCtx)
		processor := &aistudioStreamProcessor{
			translator: translator,
		}

		processEvent := func(event wsrelay.StreamEvent) bool {
			if event.Err != nil {
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("wsrelay: %v", event.Err)}
				return false
			}
			switch event.Type {
			case wsrelay.MessageTypeStreamStart:
			case wsrelay.MessageTypeStreamChunk:
				if len(event.Payload) > 0 {
					filtered := FilterSSEUsageMetadata(event.Payload)

					chunks, usage, err := processor.ProcessLine(bytes.Clone(filtered))
					if err != nil {
						out <- cliproxyexecutor.StreamChunk{Err: err}
						return false
					}
					if usage != nil {
						reporter.publish(ctx, usage)
					}
					for _, chunk := range chunks {
						out <- cliproxyexecutor.StreamChunk{Payload: ensureColonSpacedJSON(chunk)}
					}
					break
				}
			case wsrelay.MessageTypeStreamEnd:
				if chunks, err := processor.ProcessDone(); err != nil {
					out <- cliproxyexecutor.StreamChunk{Err: err}
					return false
				} else {
					for _, chunk := range chunks {
						out <- cliproxyexecutor.StreamChunk{Payload: ensureColonSpacedJSON(chunk)}
					}
				}
				return false
			case wsrelay.MessageTypeHTTPResp:
				fromFormat := sdktranslator.FromString("gemini")
				translatedResp, err := TranslateResponseNonStream(e.cfg, fromFormat, opts.SourceFormat, event.Payload, req.Model)
				if err != nil {
					out <- cliproxyexecutor.StreamChunk{Err: err}
					return false
				}
				if translatedResp != nil {
					out <- cliproxyexecutor.StreamChunk{Payload: ensureColonSpacedJSON(translatedResp)}
				} else {
					out <- cliproxyexecutor.StreamChunk{Payload: ensureColonSpacedJSON(event.Payload)}
				}
				reporter.publish(ctx, extractUsageFromGeminiResponse(event.Payload))
				return false
			case wsrelay.MessageTypeError:
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("wsrelay: %v", event.Err)}
				return false
			}
			return true
		}
		if !processEvent(first) {
			return
		}
		for event := range wsStream {
			if !processEvent(event) {
				return
			}
		}
	}(firstEvent, estimatedInputTokens)
	return stream, nil
}

func (e *AIStudioExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_, body, err := e.translateRequest(req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	body.payload, _ = sjson.DeleteBytes(body.payload, "generationConfig")
	body.payload, _ = sjson.DeleteBytes(body.payload, "tools")
	body.payload, _ = sjson.DeleteBytes(body.payload, "safetySettings")

	endpoint := e.buildEndpoint(req.Model, "countTokens", "")
	wsReq := &wsrelay.HTTPRequest{
		Method:  http.MethodPost,
		URL:     endpoint,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
		Body:    body.payload,
	}
	var authID string
	if auth != nil {
		authID = auth.ID
	}
	resp, err := e.relay.NonStream(ctx, authID, wsReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return cliproxyexecutor.Response{}, NewStatusError(resp.Status, string(resp.Body), nil)
	}
	totalTokens := gjson.GetBytes(resp.Body, "totalTokens").Int()
	if totalTokens <= 0 {
		return cliproxyexecutor.Response{}, fmt.Errorf("wsrelay: totalTokens missing in response")
	}
	return cliproxyexecutor.Response{Payload: resp.Body}, nil
}

func (e *AIStudioExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	_ = ctx
	return auth, nil
}

type translatedPayload struct {
	payload  []byte
	action   string
	toFormat sdktranslator.Format
}

type aistudioStreamProcessor struct {
	translator *StreamTranslator
}

func (p *aistudioStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	var events []ir.UnifiedEvent
	var err error
	if p.translator.ctx.ToolSchemaCtx != nil {
		events, err = to_ir.ParseGeminiChunkWithContext(line, p.translator.ctx.ToolSchemaCtx)
	} else {
		events, err = to_ir.ParseGeminiChunk(line)
	}
	if err != nil {
		return nil, nil, err
	}
	if len(events) == 0 {
		return nil, nil, nil
	}

	result, err := p.translator.Translate(events)
	if err != nil {
		return nil, nil, err
	}
	return result.Chunks, result.Usage, nil
}

func (p *aistudioStreamProcessor) ProcessDone() ([][]byte, error) {
	return p.translator.Flush(), nil
}

func (e *AIStudioExecutor) translateRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) ([]byte, translatedPayload, error) {
	from := opts.SourceFormat
	formatGemini := sdktranslator.FromString("gemini")
	payload, err := TranslateToGemini(e.cfg, from, req.Model, req.Payload, stream, req.Metadata)
	if err != nil {
		return nil, translatedPayload{}, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		payload = util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
	}
	payload = util.StripThinkingConfigIfUnsupported(req.Model, payload)
	payload = applyPayloadConfig(e.cfg, req.Model, payload)
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.maxOutputTokens")
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.responseMimeType")
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.responseJsonSchema")
	metadataAction := "generateContent"
	if req.Metadata != nil {
		if action, _ := req.Metadata["action"].(string); action == "countTokens" {
			metadataAction = action
		}
	}
	action := metadataAction
	if stream && action != "countTokens" {
		action = "streamGenerateContent"
	}
	payload, _ = sjson.DeleteBytes(payload, "session_id")
	return payload, translatedPayload{payload: payload, action: action, toFormat: formatGemini}, nil
}

func (e *AIStudioExecutor) translateRequestWithTokens(req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (translatedPayload, int64, error) {
	from := opts.SourceFormat
	formatGemini := sdktranslator.FromString("gemini")

	translation, err := TranslateToGeminiWithTokens(e.cfg, from, req.Model, req.Payload, stream, req.Metadata)
	if err != nil {
		return translatedPayload{}, 0, fmt.Errorf("translate request: %w", err)
	}

	payload := translation.Payload
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		payload = util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
	}
	payload = util.StripThinkingConfigIfUnsupported(req.Model, payload)
	payload = applyPayloadConfig(e.cfg, req.Model, payload)
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.maxOutputTokens")
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.responseMimeType")
	payload, _ = sjson.DeleteBytes(payload, "generationConfig.responseJsonSchema")

	metadataAction := "generateContent"
	if req.Metadata != nil {
		if action, _ := req.Metadata["action"].(string); action == "countTokens" {
			metadataAction = action
		}
	}
	action := metadataAction
	if stream && action != "countTokens" {
		action = "streamGenerateContent"
	}
	payload, _ = sjson.DeleteBytes(payload, "session_id")

	return translatedPayload{payload: payload, action: action, toFormat: formatGemini}, translation.EstimatedInputTokens, nil
}

func (e *AIStudioExecutor) buildEndpoint(model, action, alt string) string {
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(GeminiDefaultBaseURL)
	ub.WriteString("/")
	ub.WriteString(GeminiGLAPIVersion)
	ub.WriteString("/models/")
	ub.WriteString(model)
	ub.WriteString(":")
	ub.WriteString(action)
	base := ub.String()
	if action == "streamGenerateContent" {
		if alt == "" {
			return base + "?alt=sse"
		}
		return base + "?$alt=" + url.QueryEscape(alt)
	}
	if alt != "" && action != "countTokens" {
		return base + "?$alt=" + url.QueryEscape(alt)
	}
	return base
}

func ensureColonSpacedJSON(payload []byte) []byte {
	trimmed := bytes.TrimSpace(payload)
	if len(trimmed) == 0 {
		return payload
	}

	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return payload
	}

	indented, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return payload
	}

	compacted := make([]byte, 0, len(indented))
	inString := false
	skipSpace := false

	for i := 0; i < len(indented); i++ {
		ch := indented[i]
		if ch == '"' && (i == 0 || indented[i-1] != '\\') {
			inString = !inString
		}

		if !inString {
			if ch == '\n' || ch == '\r' {
				skipSpace = true
				continue
			}
			if skipSpace {
				if ch == ' ' || ch == '\t' {
					continue
				}
				skipSpace = false
			}
		}

		compacted = append(compacted, ch)
	}

	return compacted
}

func FetchAIStudioModels(ctx context.Context, auth *cliproxyauth.Auth, relay *wsrelay.Manager) []*registry.ModelInfo {
	if relay == nil {
		return nil
	}

	var authID string
	if auth != nil {
		authID = auth.ID
	}

	modelsURL := GeminiDefaultBaseURL + glAPIModelsPath
	wsReq := &wsrelay.HTTPRequest{
		Method:  http.MethodGet,
		URL:     modelsURL,
		Headers: http.Header{"Content-Type": []string{"application/json"}},
	}

	resp, err := relay.NonStream(ctx, authID, wsReq)
	if err != nil {
		return nil
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return nil
	}

	return ParseGLAPIModels(resp.Body, "aistudio")
}
