package providers

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/sseutil"
	"github.com/nghyane/llm-mux/internal/streamutil"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/preprocess"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type GeminiExecutor struct {
	cfg *config.Config
}

func NewGeminiExecutor(cfg *config.Config) *GeminiExecutor { return &GeminiExecutor{cfg: cfg} }

type geminiStreamProcessor struct {
	translator *stream.StreamTranslator
}

func (p *geminiStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	events, err := to_ir.ParseGeminiChunkWithState(line, p.translator.Ctx.GeminiState)
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

func (p *geminiStreamProcessor) ProcessDone() ([][]byte, error) {
	return p.translator.Flush()
}

func (e *GeminiExecutor) Identifier() string { return "gemini" }

func (e *GeminiExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *GeminiExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	apiKey, bearer := geminiCreds(auth)

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := stream.TranslateToGemini(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return resp, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		body = util.ApplyGeminiThinkingConfig(body, budgetOverride, includeOverride)
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}
	baseURL := resolveGeminiBaseURL(auth)
	ub := executor.GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(executor.GeminiGLAPIVersion)
	ub.WriteString("/models/")
	ub.WriteString(req.Model)
	ub.WriteString(":")
	ub.WriteString(action)
	if opts.Alt != "" && action != "countTokens" {
		ub.WriteString("?$alt=")
		ub.WriteString(opts.Alt)
	}
	url := ub.String()

	body, _ = sjson.DeleteBytes(body, "session_id")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	executor.SetCommonHeaders(httpReq, "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	} else if bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	applyGeminiHeaders(httpReq, auth)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return resp, executor.NewTimeoutError("request timed out")
		}
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "gemini executor")
		return resp, result.Error
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}
	reporter.Publish(ctx, executor.ExtractUsageFromGeminiResponse(data))

	fromFormat := provider.FromString("gemini")
	translatedResp, err := stream.TranslateResponseNonStream(e.cfg, fromFormat, from, data, req.Model)
	if err != nil {
		return resp, err
	}
	if translatedResp != nil {
		resp = provider.Response{Payload: translatedResp}
	} else {
		resp = provider.Response{Payload: data}
	}
	return resp, nil
}

func (e *GeminiExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	apiKey, bearer := geminiCreds(auth)

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat

	translation, err := stream.TranslateToGeminiWithTokens(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}
	if translation.IR != nil {
		preprocess.Apply(translation.IR)
	}

	body := translation.Payload
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		body = util.ApplyGeminiThinkingConfig(body, budgetOverride, includeOverride)
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	baseURL := resolveGeminiBaseURL(auth)
	ub := executor.GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(executor.GeminiGLAPIVersion)
	ub.WriteString("/models/")
	ub.WriteString(req.Model)
	ub.WriteString(":streamGenerateContent")
	if opts.Alt == "" {
		ub.WriteString("?alt=sse")
	} else {
		ub.WriteString("?$alt=")
		ub.WriteString(opts.Alt)
	}
	url := ub.String()

	body, _ = sjson.DeleteBytes(body, "session_id")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	executor.SetCommonHeaders(httpReq, "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	applyGeminiHeaders(httpReq, auth)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, executor.NewTimeoutError("request timed out")
		}
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "gemini executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}
	estimatedInputTokens := translation.EstimatedInputTokens

	pipeline := streamutil.NewPipeline(ctx, streamutil.PipelineConfig{
		BufferSize: 128,
		OnError: func(err error) {
			log.Errorf("gemini executor: stream error: %v", err)
		},
	})

	pipeline.Go(func(ctx context.Context) error {
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("gemini executor: panic in stream goroutine: %v", r)
			}
		}()
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini executor: close response body error: %v", errClose)
			}
		}()
		buf := stream.ScannerBufferPool.Get().([]byte)
		defer stream.ScannerBufferPool.Put(buf)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(buf, executor.DefaultStreamBufferSize)
		streamCtx := stream.NewStreamContext()
		streamCtx.EstimatedInputTokens = estimatedInputTokens
		messageID := "chatcmpl-" + req.Model
		translator := stream.NewStreamTranslator(e.cfg, from, from.String(), req.Model, messageID, streamCtx)
		processor := &geminiStreamProcessor{
			translator: translator,
		}

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return nil
			default:
			}

			line := scanner.Bytes()
			filtered := sseutil.FilterSSEUsageMetadata(line)
			payload := sseutil.JSONPayload(filtered)
			if len(payload) == 0 {
				continue
			}

			chunks, usage, err := processor.ProcessLine(bytes.Clone(payload))
			if err != nil {
				if flushed, _ := processor.ProcessDone(); len(flushed) > 0 {
					for _, chunk := range flushed {
						if !pipeline.SendData(chunk) {
							return nil
						}
					}
				}
				pipeline.SendError(err)
				return nil
			}
			if usage != nil {
				reporter.Publish(ctx, usage)
			}
			for _, chunk := range chunks {
				if !pipeline.SendData(chunk) {
					return nil
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			reporter.PublishFailure(ctx)
			pipeline.SendError(errScan)
			return nil
		}
		if flushed, _ := processor.ProcessDone(); len(flushed) > 0 {
			for _, chunk := range flushed {
				if !pipeline.SendData(chunk) {
					return nil
				}
			}
		}
		return nil
	})

	go func() {
		pipeline.Close()
	}()

	return stream.ConvertPipelineToStreamChunk(pipeline.Output()), nil
}

func (e *GeminiExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	apiKey, bearer := geminiCreds(auth)

	from := opts.SourceFormat
	translatedReq, err := stream.TranslateToGemini(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return provider.Response{}, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		translatedReq = util.ApplyGeminiThinkingConfig(translatedReq, budgetOverride, includeOverride)
	}
	translatedReq = util.StripThinkingConfigIfUnsupported(req.Model, translatedReq)
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "tools")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "generationConfig")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "safetySettings")

	baseURL := resolveGeminiBaseURL(auth)
	ub := executor.GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(executor.GeminiGLAPIVersion)
	ub.WriteString("/models/")
	ub.WriteString(req.Model)
	ub.WriteString(":countTokens")
	url := ub.String()

	requestBody := bytes.NewReader(translatedReq)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, requestBody)
	if err != nil {
		return provider.Response{}, err
	}
	executor.SetCommonHeaders(httpReq, "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	applyGeminiHeaders(httpReq, auth)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return provider.Response{}, executor.NewTimeoutError("request timed out")
		}
		return provider.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return provider.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("gemini executor: error status: %d, body: %s", resp.StatusCode, executor.SummarizeErrorBody(resp.Header.Get("Content-Type"), data))
		return provider.Response{}, executor.NewStatusError(resp.StatusCode, string(data), nil)
	}

	return provider.Response{Payload: data}, nil
}

func (e *GeminiExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("gemini executor: auth is nil")
	}
	if auth.Metadata == nil {
		return auth, nil
	}
	tokenMap, _ := auth.Metadata["token"].(map[string]any)
	var refreshToken, accessToken, clientID, clientSecret, tokenURI, expiryStr string
	if tokenMap != nil {
		if v, ok := tokenMap["refresh_token"].(string); ok {
			refreshToken = v
		}
		if v, ok := tokenMap["access_token"].(string); ok {
			accessToken = v
		}
		if v, ok := tokenMap["client_id"].(string); ok {
			clientID = v
		}
		if v, ok := tokenMap["client_secret"].(string); ok {
			clientSecret = v
		}
		if v, ok := tokenMap["token_uri"].(string); ok {
			tokenURI = v
		}
		if v, ok := tokenMap["expiry"].(string); ok {
			expiryStr = v
		}
	} else {
		if v, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = v
		}
		if v, ok := auth.Metadata["access_token"].(string); ok {
			accessToken = v
		}
		if v, ok := auth.Metadata["client_id"].(string); ok {
			clientID = v
		}
		if v, ok := auth.Metadata["client_secret"].(string); ok {
			clientSecret = v
		}
		if v, ok := auth.Metadata["token_uri"].(string); ok {
			tokenURI = v
		}
		if v, ok := auth.Metadata["expiry"].(string); ok {
			expiryStr = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}

	endpoint := google.Endpoint
	if tokenURI != "" {
		endpoint.TokenURL = tokenURI
	}
	conf := &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: endpoint}

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)

	tok := &oauth2.Token{AccessToken: accessToken, RefreshToken: refreshToken}
	if t, err := time.Parse(time.RFC3339, expiryStr); err == nil {
		tok.Expiry = t
	}
	newTok, err := conf.TokenSource(ctx, tok).Token()
	if err != nil {
		return nil, err
	}

	if tokenMap == nil {
		tokenMap = make(map[string]any)
	}
	tokenMap["access_token"] = newTok.AccessToken
	tokenMap["refresh_token"] = newTok.RefreshToken
	tokenMap["expiry"] = newTok.Expiry.Format(time.RFC3339)
	if clientID != "" {
		tokenMap["client_id"] = clientID
	}
	if clientSecret != "" {
		tokenMap["client_secret"] = clientSecret
	}
	if tokenURI != "" {
		tokenMap["token_uri"] = tokenURI
	}
	auth.Metadata["token"] = tokenMap

	if _, ok := auth.Metadata["access_token"]; ok {
		auth.Metadata["access_token"] = newTok.AccessToken
	}
	return auth, nil
}

func geminiCreds(a *provider.Auth) (apiKey, bearer string) {
	token, _ := executor.ExtractCreds(a, executor.GeminiCredsConfig)
	if a != nil && a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
	}
	bearer = token
	return
}

func resolveGeminiBaseURL(auth *provider.Auth) string {
	base := executor.GeminiDefaultBaseURL
	if auth != nil {
		if custom := executor.AttrStringValue(auth.Attributes, "base_url"); custom != "" {
			base = strings.TrimRight(custom, "/")
		}
	}
	if base == "" {
		return executor.GeminiDefaultBaseURL
	}
	return base
}

func applyGeminiHeaders(req *http.Request, auth *provider.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
}

func FetchGeminiModels(ctx context.Context, auth *provider.Auth, cfg *config.Config) []*registry.ModelInfo {
	apiKey, bearer := geminiCreds(auth)
	if apiKey == "" && bearer == "" {
		return nil
	}

	httpClient := executor.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)

	fetchCfg := GLAPIFetchConfig{
		BaseURL:      resolveGeminiBaseURL(auth),
		APIKey:       apiKey,
		Bearer:       bearer,
		ProviderType: "gemini",
	}

	return FetchGLAPIModels(ctx, httpClient, fetchCfg)
}

func convertPipelineToStreamChunk(input <-chan streamutil.Chunk) <-chan provider.StreamChunk {
	out := make(chan provider.StreamChunk, 128)
	go func() {
		defer close(out)
		for chunk := range input {
			out <- provider.StreamChunk{Payload: chunk.Data, Err: chunk.Err}
		}
	}()
	return out
}
