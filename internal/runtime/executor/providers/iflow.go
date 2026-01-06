package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	iflowauth "github.com/nghyane/llm-mux/internal/auth/iflow"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/sseutil"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type IFlowExecutor struct {
	cfg *config.Config
}

func NewIFlowExecutor(cfg *config.Config) *IFlowExecutor { return &IFlowExecutor{cfg: cfg} }

func (e *IFlowExecutor) Identifier() string { return "iflow" }

func (e *IFlowExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *IFlowExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	apiKey, baseURL := iflowCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		err = fmt.Errorf("iflow executor: missing api key")
		return resp, err
	}
	if baseURL == "" {
		baseURL = iflowauth.DefaultAPIBaseURL
	}

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return resp, err
	}
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	endpoint := strings.TrimSuffix(baseURL, "/") + executor.IFlowDefaultEndpoint

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyIFlowHeaders(httpReq, apiKey, false)

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
			log.Errorf("iflow executor: close response body error: %v", errClose)
		}
	}()

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "iflow executor")
		return resp, result.Error
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}
	reporter.Publish(ctx, executor.ExtractUsageFromOpenAIResponse(data))
	reporter.EnsurePublished(ctx)

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

func (e *IFlowExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	apiKey, baseURL := iflowCreds(auth)
	if strings.TrimSpace(apiKey) == "" {
		err = fmt.Errorf("iflow executor: missing api key")
		return nil, err
	}
	if baseURL == "" {
		baseURL = iflowauth.DefaultAPIBaseURL
	}

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if err != nil {
		return nil, err
	}

	toolsResult := gjson.GetBytes(body, "tools")
	if toolsResult.Exists() && toolsResult.IsArray() && len(toolsResult.Array()) == 0 {
		body = ensureToolsArray(body)
	}
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	endpoint := strings.TrimSuffix(baseURL, "/") + executor.IFlowDefaultEndpoint

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyIFlowHeaders(httpReq, apiKey, true)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, executor.NewTimeoutError("request timed out")
		}
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "iflow executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	messageID := "chatcmpl-" + req.Model
	processor := stream.NewOpenAIStreamProcessor(e.cfg, from, req.Model, messageID)

	return stream.RunSSEStream(ctx, httpResp.Body, reporter, processor, stream.StreamConfig{
		ExecutorName:    "iflow executor",
		EnsurePublished: true,
	}), nil
}

func (e *IFlowExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	return executor.CountTokensForOpenAIProvider(ctx, e.cfg, "iflow executor", opts.SourceFormat, req.Model, req.Payload, req.Metadata)
}

func (e *IFlowExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("iflow executor: auth is nil")
	}

	var cookie string
	var email string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["cookie"].(string); ok {
			cookie = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["email"].(string); ok {
			email = strings.TrimSpace(v)
		}
	}

	if cookie != "" && email != "" {
		return e.refreshCookieBased(ctx, auth, cookie, email)
	}

	return e.refreshOAuthBased(ctx, auth)
}

func (e *IFlowExecutor) refreshCookieBased(ctx context.Context, auth *provider.Auth, cookie, email string) (*provider.Auth, error) {
	log.Debugf("iflow executor: checking refresh need for cookie-based API key for user: %s", email)

	var currentExpire string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["expired"].(string); ok {
			currentExpire = strings.TrimSpace(v)
		}
	}

	needsRefresh, _, err := iflowauth.ShouldRefreshAPIKey(currentExpire)
	if err != nil {
		log.Warnf("iflow executor: failed to check refresh need: %v", err)
	} else if !needsRefresh {
		log.Debugf("iflow executor: no refresh needed for user: %s", email)
		return auth, nil
	}

	log.Infof("iflow executor: refreshing cookie-based API key for user: %s", email)

	svc := iflowauth.NewIFlowAuth(e.cfg)
	keyData, err := svc.RefreshAPIKey(ctx, cookie, email)
	if err != nil {
		log.Errorf("iflow executor: cookie-based API key refresh failed: %v", err)
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["api_key"] = keyData.APIKey
	auth.Metadata["expired"] = keyData.ExpireTime
	auth.Metadata["type"] = "iflow"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	auth.Metadata["cookie"] = cookie
	auth.Metadata["email"] = email

	log.Infof("iflow executor: cookie-based API key refreshed successfully, new expiry: %s", keyData.ExpireTime)

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["api_key"] = keyData.APIKey

	return auth, nil
}

func (e *IFlowExecutor) refreshOAuthBased(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	refreshToken := ""
	oldAccessToken := ""
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok {
			refreshToken = strings.TrimSpace(v)
		}
		if v, ok := auth.Metadata["access_token"].(string); ok {
			oldAccessToken = strings.TrimSpace(v)
		}
	}
	if refreshToken == "" {
		return auth, nil
	}

	if oldAccessToken != "" {
		log.Debugf("iflow executor: refreshing access token, old: %s", util.HideAPIKey(oldAccessToken))
	}

	svc := iflowauth.NewIFlowAuth(e.cfg)
	tokenData, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		log.Errorf("iflow executor: token refresh failed: %v", err)
		return nil, err
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenData.AccessToken
	if tokenData.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenData.RefreshToken
	}
	if tokenData.APIKey != "" {
		auth.Metadata["api_key"] = tokenData.APIKey
	}
	auth.Metadata["expired"] = tokenData.Expire
	auth.Metadata["type"] = "iflow"
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)

	log.Debugf("iflow executor: token refresh successful, new: %s", util.HideAPIKey(tokenData.AccessToken))

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if tokenData.APIKey != "" {
		auth.Attributes["api_key"] = tokenData.APIKey
	}

	return auth, nil
}

func applyIFlowHeaders(r *http.Request, apiKey string, stream bool) {
	executor.ApplyAPIHeaders(r, executor.HeaderConfig{
		Token:     apiKey,
		UserAgent: executor.DefaultIFlowUserAgent,
	}, stream)
}

func iflowCreds(a *provider.Auth) (apiKey, baseURL string) {
	return executor.ExtractCreds(a, executor.IFlowCredsConfig)
}

func ensureToolsArray(body []byte) []byte {
	placeholder := `[{"type":"function","function":{"name":"noop","description":"Placeholder tool to stabilise streaming","parameters":{"type":"object"}}}]`
	updated, err := sjson.SetRawBytes(body, "tools", []byte(placeholder))
	if err != nil {
		return body
	}
	return updated
}
