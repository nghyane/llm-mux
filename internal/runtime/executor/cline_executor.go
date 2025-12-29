/**
 * @file Cline executor implementation for API requests
 * @description Stateless executor for Cline API using OpenAI-compatible chat completions.
 * Handles both streaming and non-streaming requests, automatic token refresh, and usage tracking.
 * Cline API uses JWT tokens with "workos:" prefix for authentication and supports various AI models
 * including Minimax, Grok, Claude, and OpenRouter models through a unified interface.
 */

package executor

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
	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ClineExecutor is a stateless executor for Cline API using OpenAI-compatible chat completions.
type ClineExecutor struct {
	cfg *config.Config
}

// NewClineExecutor creates a new Cline executor instance.
func NewClineExecutor(cfg *config.Config) *ClineExecutor {
	return &ClineExecutor{cfg: cfg}
}

// Identifier returns the provider identifier for this executor.
func (e *ClineExecutor) Identifier() string {
	return "cline"
}

// PrepareRequest prepares the HTTP request with necessary headers and authentication.
func (e *ClineExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

// Execute performs a non-streaming request to Cline API.
func (e *ClineExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, baseURL := clineCredentials(auth)
	if token == "" {
		return resp, fmt.Errorf("cline access token not available")
	}

	if baseURL == "" {
		baseURL = ClineDefaultBaseURL
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, nil)
	if err != nil {
		return resp, err
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/api/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}

	e.applyClineHeaders(httpReq, token, false)

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
			log.Errorf("cline executor: close response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "cline executor")
		return resp, result.Error
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}

	reporter.publish(ctx, extractUsageFromOpenAIResponse(data))

	fromOpenAI := sdktranslator.FromString("openai")
	translatedResp, err := TranslateResponseNonStream(e.cfg, fromOpenAI, from, data, req.Model)
	if err != nil {
		return resp, err
	}
	if translatedResp != nil {
		resp = cliproxyexecutor.Response{Payload: translatedResp}
	} else {
		resp = cliproxyexecutor.Response{Payload: data} // passthrough
	}
	return resp, nil
}

// ExecuteStream performs a streaming request to Cline API.
func (e *ClineExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	token, baseURL := clineCredentials(auth)
	if token == "" {
		return nil, fmt.Errorf("cline access token not available")
	}

	if baseURL == "" {
		baseURL = ClineDefaultBaseURL
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, nil)
	if err != nil {
		return nil, err
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/api/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	e.applyClineHeaders(httpReq, token, true)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewTimeoutError("request timed out")
		}
		return nil, err
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "cline executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	messageID := "chatcmpl-" + req.Model
	processor := NewOpenAIStreamProcessor(e.cfg, from, req.Model, messageID)
	processor.Preprocess = clinePreprocess

	return RunSSEStream(ctx, httpResp.Body, reporter, processor, StreamConfig{
		ExecutorName:       "cline executor",
		Preprocessor:       ClineDataTagPreprocessor(),
		SkipDoneInData:     true,
		PassthroughOnEmpty: true,
	}), nil
}

// ClineDataTagPreprocessor creates a preprocessor that handles Cline's SSE format.
// It strips "data:" prefix and skips [DONE] signals.
func ClineDataTagPreprocessor() StreamPreprocessor {
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

// clinePreprocess transforms Cline-specific payload format to OpenAI format.
func clinePreprocess(line []byte, firstChunk bool) []byte {
	payload := convertClineReasoningToOpenAI(line, firstChunk)

	if !firstChunk && shouldSkipEmptyContentChunk(payload) {
		return nil
	}

	// Add data: prefix back for the translator
	return append([]byte("data: "), payload...)
}

// clineCredentials extracts access token and base URL from auth metadata.
// Delegates to the common ExtractCreds function with Cline configuration.
func clineCredentials(a *cliproxyauth.Auth) (token, baseURL string) {
	return ExtractCreds(a, ClineCredsConfig)
}

// applyClineHeaders applies necessary headers for Cline API requests.
func (e *ClineExecutor) applyClineHeaders(req *http.Request, token string, stream bool) {
	ApplyAPIHeaders(req, HeaderConfig{
		Token: token,
		StreamHeaders: map[string]string{
			"Cache-Control": "no-cache",
			"Connection":    "keep-alive",
		},
	}, stream)
}

// convertClineReasoningToOpenAI converts Cline API's "reasoning" field to OpenAI's "reasoning_content" field
// and removes "role" from non-first chunks to comply with OpenAI streaming standard.
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

// shouldSkipEmptyContentChunk determines if a chunk with empty/null content should be skipped.
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

// CountTokens counts tokens in the request for Cline models.
func (e *ClineExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return CountTokensForOpenAIProvider(ctx, e.cfg, "cline executor", opts.SourceFormat, req.Model, req.Payload, nil)
}

// Refresh refreshes the Cline authentication tokens.
func (e *ClineExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("cline executor: auth is nil")
	}

	refreshToken, ok := ExtractRefreshToken(auth)
	if !ok {
		return auth, nil
	}

	svc := clineauth.NewClineAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	UpdateRefreshMetadata(auth, map[string]any{
		"access_token":  td.AccessToken,
		"refresh_token": td.RefreshToken,
		"email":         td.Email,
		"expired":       td.Expire,
	}, "cline")

	return auth, nil
}
