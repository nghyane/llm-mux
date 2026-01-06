package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	clineauth "github.com/nghyane/llm-mux/internal/auth/cline"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/sseutil"
)

type ClineExecutor struct {
	cfg *config.Config
}

func NewClineExecutor(cfg *config.Config) *ClineExecutor { return &ClineExecutor{cfg: cfg} }

func (e *ClineExecutor) Identifier() string { return "cline" }

func (e *ClineExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *ClineExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	token, baseURL := clineCredentials(auth)
	if token == "" {
		return resp, fmt.Errorf("cline access token not available")
	}

	if baseURL == "" {
		baseURL = executor.ClineDefaultBaseURL
	}

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, nil)
	if err != nil {
		return resp, err
	}
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/api/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}

	e.applyClineHeaders(httpReq, token, false)

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
			log.Errorf("cline executor: close response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "cline executor")
		return resp, result.Error
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}

	reporter.Publish(ctx, executor.ExtractUsageFromOpenAIResponse(data))

	fromOpenAI := provider.FromString("openai")
	translatedResp, err := stream.TranslateResponseNonStream(e.cfg, fromOpenAI, from, data, req.Model)
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

func (e *ClineExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	token, baseURL := clineCredentials(auth)
	if token == "" {
		return nil, fmt.Errorf("cline access token not available")
	}

	if baseURL == "" {
		baseURL = executor.ClineDefaultBaseURL
	}

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, nil)
	if err != nil {
		return nil, err
	}
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/api/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	e.applyClineHeaders(httpReq, token, true)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, executor.NewTimeoutError("request timed out")
		}
		return nil, err
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "cline executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	messageID := "chatcmpl-" + req.Model
	processor := stream.NewOpenAIStreamProcessor(e.cfg, from, req.Model, messageID)
	processor.Preprocess = clinePreprocess

	return stream.RunSSEStream(ctx, httpResp.Body, reporter, processor, stream.StreamConfig{
		ExecutorName:       "cline executor",
		Preprocessor:       ClineDataTagPreprocessor(),
		SkipDoneInData:     true,
		PassthroughOnEmpty: true,
	}), nil
}

func ClineDataTagPreprocessor() stream.StreamPreprocessor {
	return func(line []byte) (payload []byte, skip bool) {
		payload = line
		if bytes.HasPrefix(line, []byte("data: ")) {
			payload = bytes.TrimSpace(line[6:])
		} else if bytes.HasPrefix(line, []byte("data:")) {
			payload = bytes.TrimSpace(line[5:])
		}

		if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
			return nil, true
		}
		return payload, false
	}
}

func clinePreprocess(line []byte, firstChunk bool) []byte {
	payload := convertClineReasoningToOpenAI(line, firstChunk)

	if !firstChunk && shouldSkipEmptyContentChunk(payload) {
		return nil
	}

	return append([]byte("data: "), payload...)
}

func clineCredentials(a *provider.Auth) (token, baseURL string) {
	return executor.ExtractCreds(a, executor.ClineCredsConfig)
}

func (e *ClineExecutor) applyClineHeaders(req *http.Request, token string, stream bool) {
	executor.ApplyAPIHeaders(req, executor.HeaderConfig{
		Token: token,
		StreamHeaders: map[string]string{
			"Cache-Control": "no-cache",
		},
	}, stream)
}

func convertClineReasoningToOpenAI(payload []byte, isFirstChunk bool) []byte {
	if bytes.Contains(payload, []byte(`"reasoning":`)) && !bytes.Contains(payload, []byte(`"reasoning_content":`)) {
		payload = bytes.ReplaceAll(payload, []byte(`"reasoning":`), []byte(`"reasoning_content":`))
	}

	if !isFirstChunk && bytes.Contains(payload, []byte(`"role":"assistant"`)) {
		payload = bytes.ReplaceAll(payload, []byte(`"role":"assistant",`), []byte{})
		payload = bytes.ReplaceAll(payload, []byte(`,"role":"assistant"`), []byte{})
	}

	return payload
}

func shouldSkipEmptyContentChunk(payload []byte) bool {
	hasEmptyContent := bytes.Contains(payload, []byte(`"content":""`)) ||
		bytes.Contains(payload, []byte(`"content":null`))

	if !hasEmptyContent {
		return false
	}

	if bytes.Contains(payload, []byte(`"tool_calls"`)) {
		return false
	}

	if bytes.Contains(payload, []byte(`"finish_reason"`)) {
		return false
	}

	if bytes.Contains(payload, []byte(`"usage"`)) {
		return false
	}

	return true
}

func (e *ClineExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	return executor.CountTokensForOpenAIProvider(ctx, e.cfg, "cline executor", opts.SourceFormat, req.Model, req.Payload, nil)
}

func (e *ClineExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("cline executor: auth is nil")
	}

	refreshToken, ok := executor.ExtractRefreshToken(auth)
	if !ok {
		return auth, nil
	}

	svc := clineauth.NewClineAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	executor.UpdateRefreshMetadata(auth, map[string]any{
		"access_token":  td.AccessToken,
		"refresh_token": td.RefreshToken,
		"email":         td.Email,
		"expired":       td.Expire,
	}, "cline")

	return auth, nil
}
