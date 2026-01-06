package providers

import (
	"bytes"
	"context"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/sseutil"
	"github.com/nghyane/llm-mux/internal/streamutil"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/nghyane/llm-mux/internal/wsrelay"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type AIStudioExecutor struct {
	cfg      *config.Config
	provider string
	relay    *wsrelay.Manager
}

func NewAIStudioExecutor(cfg *config.Config, provider string, relay *wsrelay.Manager) *AIStudioExecutor {
	return &AIStudioExecutor{cfg: cfg, provider: strings.ToLower(provider), relay: relay}
}

func (e *AIStudioExecutor) Identifier() string { return "aistudio" }

func (e *AIStudioExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *AIStudioExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

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
		return resp, executor.NewStatusError(wsResp.Status, string(wsResp.Body), nil)
	}
	reporter.Publish(ctx, executor.ExtractUsageFromGeminiResponse(wsResp.Body))

	fromFormat := provider.FromString("gemini")
	translatedResp, err := stream.TranslateResponseNonStream(e.cfg, fromFormat, opts.SourceFormat, wsResp.Body, req.Model)
	if err != nil {
		return resp, err
	}
	if translatedResp != nil {
		resp = provider.Response{Payload: ensureColonSpacedJSON(translatedResp)}
	} else {
		resp = provider.Response{Payload: ensureColonSpacedJSON(wsResp.Body)}
	}
	return resp, nil
}

func (e *AIStudioExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

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
			return nil, executor.NewStatusError(firstEvent.Status, body.String(), nil)
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
		return nil, executor.NewStatusError(firstEvent.Status, body.String(), nil)
	}
	pipeline := streamutil.NewPipeline(ctx, streamutil.PipelineConfig{
		BufferSize: 128,
		OnError: func(err error) {
			log.Errorf("aistudio executor: stream error: %v", err)
		},
	})

	pipeline.Go(func(ctx context.Context) error {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("aistudio executor: panic in stream goroutine: %v", r)
			}
		}()

		streamCtx := stream.NewStreamContext()
		streamCtx.EstimatedInputTokens = estimatedInputTokens
		messageID := "chatcmpl-" + req.Model
		translator := stream.NewStreamTranslator(e.cfg, opts.SourceFormat, opts.SourceFormat.String(), req.Model, messageID, streamCtx)
		processor := &aistudioStreamProcessor{
			translator: translator,
		}

		processEvent := func(event wsrelay.StreamEvent) bool {
			if event.Err != nil {
				reporter.PublishFailure(ctx)
				pipeline.SendError(fmt.Errorf("wsrelay: %v", event.Err))
				return false
			}
			switch event.Type {
			case wsrelay.MessageTypeStreamStart:
			case wsrelay.MessageTypeStreamChunk:
				if len(event.Payload) > 0 {
					filtered := sseutil.FilterSSEUsageMetadata(event.Payload)

					chunks, usage, err := processor.ProcessLine(bytes.Clone(filtered))
					if err != nil {
						pipeline.SendError(err)
						return false
					}
					if usage != nil {
						reporter.Publish(ctx, usage)
					}
					for _, chunk := range chunks {
						if !pipeline.SendData(ensureColonSpacedJSON(chunk)) {
							return false
						}
					}
					break
				}
			case wsrelay.MessageTypeStreamEnd:
				if chunks, err := processor.ProcessDone(); err != nil {
					pipeline.SendError(err)
					return false
				} else {
					for _, chunk := range chunks {
						if !pipeline.SendData(ensureColonSpacedJSON(chunk)) {
							return false
						}
					}
				}
				return false
			case wsrelay.MessageTypeHTTPResp:
				fromFormat := provider.FromString("gemini")
				translatedResp, err := stream.TranslateResponseNonStream(e.cfg, fromFormat, opts.SourceFormat, event.Payload, req.Model)
				if err != nil {
					pipeline.SendError(err)
					return false
				}
				if translatedResp != nil {
					if !pipeline.SendData(ensureColonSpacedJSON(translatedResp)) {
						return false
					}
				} else {
					if !pipeline.SendData(ensureColonSpacedJSON(event.Payload)) {
						return false
					}
				}
				reporter.Publish(ctx, executor.ExtractUsageFromGeminiResponse(event.Payload))
				return false
			case wsrelay.MessageTypeError:
				reporter.PublishFailure(ctx)
				pipeline.SendError(fmt.Errorf("wsrelay: %v", event.Err))
				return false
			}
			return true
		}
		if !processEvent(firstEvent) {
			return nil
		}
		for event := range wsStream {
			if !processEvent(event) {
				return nil
			}
		}
		return nil
	})

	go func() {
		pipeline.Close()
	}()

	return stream.ConvertPipelineToStreamChunk(pipeline.Output()), nil
}

func (e *AIStudioExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	_, body, err := e.translateRequest(req, opts, false)
	if err != nil {
		return provider.Response{}, err
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
		return provider.Response{}, err
	}
	if resp.Status < 200 || resp.Status >= 300 {
		return provider.Response{}, executor.NewStatusError(resp.Status, string(resp.Body), nil)
	}
	totalTokens := gjson.GetBytes(resp.Body, "totalTokens").Int()
	if totalTokens <= 0 {
		return provider.Response{}, fmt.Errorf("wsrelay: totalTokens missing in response")
	}
	return provider.Response{Payload: resp.Body}, nil
}

func (e *AIStudioExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	_ = ctx
	return auth, nil
}

type translatedPayload struct {
	payload  []byte
	action   string
	toFormat provider.Format
}

type aistudioStreamProcessor struct {
	translator *stream.StreamTranslator
}

func (p *aistudioStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	state := p.translator.Ctx.GeminiState
	var events []ir.UnifiedEvent
	var err error
	if p.translator.Ctx.ToolSchemaCtx != nil {
		events, err = to_ir.ParseGeminiChunkWithStateContext(line, state, p.translator.Ctx.ToolSchemaCtx)
	} else {
		events, err = to_ir.ParseGeminiChunkWithState(line, state)
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
	return p.translator.Flush()
}

func (e *AIStudioExecutor) translateRequest(req provider.Request, opts provider.Options, isStreaming bool) ([]byte, translatedPayload, error) {
	from := opts.SourceFormat
	formatGemini := provider.FromString("gemini")
	payload, err := stream.TranslateToGemini(e.cfg, from, req.Model, req.Payload, isStreaming, req.Metadata)
	if err != nil {
		return nil, translatedPayload{}, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		payload = util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
	}
	payload = util.StripThinkingConfigIfUnsupported(req.Model, payload)
	payload = sseutil.ApplyPayloadConfig(e.cfg, req.Model, payload)
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
	if isStreaming && action != "countTokens" {
		action = "streamGenerateContent"
	}
	payload, _ = sjson.DeleteBytes(payload, "session_id")
	return payload, translatedPayload{payload: payload, action: action, toFormat: formatGemini}, nil
}

func (e *AIStudioExecutor) translateRequestWithTokens(req provider.Request, opts provider.Options, isStreaming bool) (translatedPayload, int64, error) {
	from := opts.SourceFormat
	formatGemini := provider.FromString("gemini")

	translation, err := stream.TranslateToGeminiWithTokens(e.cfg, from, req.Model, req.Payload, isStreaming, req.Metadata)
	if err != nil {
		return translatedPayload{}, 0, fmt.Errorf("translate request: %w", err)
	}

	payload := translation.Payload
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		payload = util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
	}
	payload = util.StripThinkingConfigIfUnsupported(req.Model, payload)
	payload = sseutil.ApplyPayloadConfig(e.cfg, req.Model, payload)
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
	if isStreaming && action != "countTokens" {
		action = "streamGenerateContent"
	}
	payload, _ = sjson.DeleteBytes(payload, "session_id")

	return translatedPayload{payload: payload, action: action, toFormat: formatGemini}, translation.EstimatedInputTokens, nil
}

func (e *AIStudioExecutor) buildEndpoint(model, action, alt string) string {
	ub := executor.GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(executor.GeminiDefaultBaseURL)
	ub.WriteString("/")
	ub.WriteString(executor.GeminiGLAPIVersion)
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

func FetchAIStudioModels(ctx context.Context, auth *provider.Auth, relay *wsrelay.Manager) []*registry.ModelInfo {
	if relay == nil {
		return nil
	}

	var authID string
	if auth != nil {
		authID = auth.ID
	}

	modelsURL := executor.GeminiDefaultBaseURL + GLAPIModelsPath
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
