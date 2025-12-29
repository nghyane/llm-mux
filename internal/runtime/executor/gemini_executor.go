package executor

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
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/nghyane/llm-mux/internal/util"
	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

type GeminiExecutor struct {
	cfg *config.Config
}

func NewGeminiExecutor(cfg *config.Config) *GeminiExecutor { return &GeminiExecutor{cfg: cfg} }

type geminiStreamProcessor struct {
	translator *StreamTranslator
}

func (p *geminiStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	events, err := to_ir.ParseGeminiChunk(line)
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
	return p.translator.Flush(), nil
}

func (e *GeminiExecutor) Identifier() string { return "gemini" }

func (e *GeminiExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *GeminiExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, bearer := geminiCreds(auth)

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := TranslateToGemini(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return resp, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		body = util.ApplyGeminiThinkingConfig(body, budgetOverride, includeOverride)
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}
	baseURL := resolveGeminiBaseURL(auth)
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(GeminiGLAPIVersion)
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
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	} else if bearer != "" {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	applyGeminiHeaders(httpReq, auth)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return resp, NewTimeoutError("request timed out")
		}
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "gemini executor")
		return resp, result.Error
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}
	reporter.publish(ctx, extractUsageFromGeminiResponse(data))

	fromFormat := sdktranslator.FromString("gemini")
	translatedResp, err := TranslateResponseNonStream(e.cfg, fromFormat, from, data, req.Model)
	if err != nil {
		return resp, err
	}
	if translatedResp != nil {
		resp = cliproxyexecutor.Response{Payload: translatedResp}
	} else {
		resp = cliproxyexecutor.Response{Payload: data}
	}
	return resp, nil
}

func (e *GeminiExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiKey, bearer := geminiCreds(auth)

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat

	translation, err := TranslateToGeminiWithTokens(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}

	body := translation.Payload
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		body = util.ApplyGeminiThinkingConfig(body, budgetOverride, includeOverride)
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	baseURL := resolveGeminiBaseURL(auth)
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(GeminiGLAPIVersion)
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
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	applyGeminiHeaders(httpReq, auth)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewTimeoutError("request timed out")
		}
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "gemini executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}
	out := make(chan cliproxyexecutor.StreamChunk, 8)
	stream = out

	estimatedInputTokens := translation.EstimatedInputTokens

	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini executor: close response body error: %v", errClose)
			}
		}()
		buf := scannerBufferPool.Get().([]byte)
		defer scannerBufferPool.Put(buf)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(buf, DefaultStreamBufferSize)
		streamCtx := NewStreamContext()
		streamCtx.EstimatedInputTokens = estimatedInputTokens
		messageID := "chatcmpl-" + req.Model
		translator := NewStreamTranslator(e.cfg, from, from.String(), req.Model, messageID, streamCtx)
		processor := &geminiStreamProcessor{
			translator: translator,
		}

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Bytes()
			filtered := FilterSSEUsageMetadata(line)
			payload := jsonPayload(filtered)
			if len(payload) == 0 {
				continue
			}

			chunks, usage, err := processor.ProcessLine(bytes.Clone(payload))
			if err != nil {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: err}:
				case <-ctx.Done():
				}
				return
			}
			if usage != nil {
				reporter.publish(ctx, usage)
			}
			for _, chunk := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			reporter.publishFailure(ctx)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return stream, nil
}

func (e *GeminiExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey, bearer := geminiCreds(auth)

	from := opts.SourceFormat
	translatedReq, err := TranslateToGemini(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		translatedReq = util.ApplyGeminiThinkingConfig(translatedReq, budgetOverride, includeOverride)
	}
	translatedReq = util.StripThinkingConfigIfUnsupported(req.Model, translatedReq)
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "tools")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "generationConfig")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "safetySettings")

	baseURL := resolveGeminiBaseURL(auth)
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(GeminiGLAPIVersion)
	ub.WriteString("/models/")
	ub.WriteString(req.Model)
	ub.WriteString(":countTokens")
	url := ub.String()

	requestBody := bytes.NewReader(translatedReq)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, requestBody)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	} else {
		httpReq.Header.Set("Authorization", "Bearer "+bearer)
	}
	applyGeminiHeaders(httpReq, auth)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return cliproxyexecutor.Response{}, NewTimeoutError("request timed out")
		}
		return cliproxyexecutor.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("gemini executor: error status: %d, body: %s", resp.StatusCode, summarizeErrorBody(resp.Header.Get("Content-Type"), data))
		return cliproxyexecutor.Response{}, NewStatusError(resp.StatusCode, string(data), nil)
	}

	return cliproxyexecutor.Response{Payload: data}, nil
}

func (e *GeminiExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
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

	httpClient := util.SetProxy(&e.cfg.SDKConfig, &http.Client{})
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

func geminiCreds(a *cliproxyauth.Auth) (apiKey, bearer string) {
	token, _ := ExtractCreds(a, GeminiCredsConfig)
	if a != nil && a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
	}
	bearer = token
	return
}

func resolveGeminiBaseURL(auth *cliproxyauth.Auth) string {
	base := GeminiDefaultBaseURL
	if auth != nil && auth.Attributes != nil {
		if custom := strings.TrimSpace(auth.Attributes["base_url"]); custom != "" {
			base = strings.TrimRight(custom, "/")
		}
	}
	if base == "" {
		return GeminiDefaultBaseURL
	}
	return base
}

func applyGeminiHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
}

func FetchGeminiModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	apiKey, bearer := geminiCreds(auth)
	if apiKey == "" && bearer == "" {
		return nil
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	fetchCfg := GLAPIFetchConfig{
		BaseURL:      resolveGeminiBaseURL(auth),
		APIKey:       apiKey,
		Bearer:       bearer,
		ProviderType: "gemini",
	}

	return FetchGLAPIModels(ctx, httpClient, fetchCfg)
}
