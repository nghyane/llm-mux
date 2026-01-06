package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	copilotauth "github.com/nghyane/llm-mux/internal/auth/copilot"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/sseutil"
	"github.com/tidwall/sjson"
)

type CopilotExecutor struct {
	cfg          *config.Config
	mu           sync.RWMutex
	cache        map[string]*cachedCopilotToken
	tokenRefresh *executor.TokenRefreshGroup
}

type cachedCopilotToken struct {
	token     string
	expiresAt time.Time
}

func NewCopilotExecutor(cfg *config.Config) *CopilotExecutor {
	return &CopilotExecutor{cfg: cfg, cache: make(map[string]*cachedCopilotToken), tokenRefresh: executor.NewTokenRefreshGroup()}
}

func (e *CopilotExecutor) Identifier() string { return executor.GitHubCopilotAuthType }

func (e *CopilotExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *CopilotExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, errTranslate := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, nil)
	if errTranslate != nil {
		return resp, errTranslate
	}
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)
	body, _ = sjson.SetBytes(body, "stream", false)

	url := executor.GitHubCopilotDefaultBaseURL + executor.GitHubCopilotChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyCopilotHeaders(httpReq, apiToken, false)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return resp, executor.NewTimeoutError("request timed out")
		}
		return resp, err
	}
	defer func() { _ = httpResp.Body.Close() }()

	if !isHTTPSuccessCode(httpResp.StatusCode) {
		result := executor.HandleHTTPError(httpResp, "github-copilot executor")
		return resp, result.Error
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}

	detail := executor.ExtractUsageFromOpenAIResponse(data)
	if detail != nil && detail.TotalTokens > 0 {
		reporter.Publish(ctx, detail)
	}

	fromOpenAI := provider.FromString("openai")
	translatedResp, errTranslate := stream.TranslateResponseNonStream(e.cfg, fromOpenAI, from, data, req.Model)
	if errTranslate != nil {
		return resp, errTranslate
	}
	if translatedResp != nil {
		resp = provider.Response{Payload: translatedResp}
	} else {
		resp = provider.Response{Payload: data}
	}
	reporter.EnsurePublished(ctx)
	return resp, nil
}

func (e *CopilotExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}

	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, errTranslate := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, nil)
	if errTranslate != nil {
		return nil, errTranslate
	}
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)

	url := executor.GitHubCopilotDefaultBaseURL + executor.GitHubCopilotChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyCopilotHeaders(httpReq, apiToken, true)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, executor.NewTimeoutError("request timed out")
		}
		return nil, err
	}

	if !isHTTPSuccessCode(httpResp.StatusCode) {
		result := executor.HandleHTTPError(httpResp, "github-copilot executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	messageID := uuid.NewString()
	processor := stream.NewOpenAIStreamProcessor(e.cfg, from, req.Model, messageID)

	return stream.RunSSEStream(ctx, httpResp.Body, reporter, processor, stream.StreamConfig{
		ExecutorName:    "github-copilot executor",
		SkipDoneInData:  true,
		EnsurePublished: true,
	}), nil
}

func (e *CopilotExecutor) CountTokens(_ context.Context, _ *provider.Auth, _ provider.Request, _ provider.Options) (provider.Response, error) {
	return provider.Response{}, executor.NewStatusError(http.StatusNotImplemented, "count tokens not supported for github-copilot", nil)
}

func (e *CopilotExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	if auth == nil {
		return nil, executor.NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}

	accessToken := executor.MetaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return auth, nil
	}

	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	_, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return nil, executor.NewStatusError(http.StatusUnauthorized, fmt.Sprintf("github-copilot token validation failed: %v", err), nil)
	}

	return auth, nil
}

func (e *CopilotExecutor) ensureAPIToken(ctx context.Context, auth *provider.Auth) (string, error) {
	if auth == nil {
		return "", executor.NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}

	accessToken := executor.MetaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return "", executor.NewStatusError(http.StatusUnauthorized, "missing github access token", nil)
	}

	e.mu.RLock()
	if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(executor.TokenExpiryBuffer)) {
		e.mu.RUnlock()
		return cached.token, nil
	}
	e.mu.RUnlock()

	result, err := e.tokenRefresh.Do(accessToken, func(tokenCtx context.Context) (any, error) {
		e.mu.RLock()
		if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(executor.TokenExpiryBuffer)) {
			e.mu.RUnlock()
			return cached.token, nil
		}
		e.mu.RUnlock()

		copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
		apiToken, err := copilotAuth.GetCopilotAPIToken(tokenCtx, accessToken)
		if err != nil {
			return "", executor.NewStatusError(http.StatusUnauthorized, fmt.Sprintf("failed to get copilot api token: %v", err), nil)
		}

		expiresAt := time.Now().Add(executor.GitHubCopilotTokenCacheTTL)
		if apiToken.ExpiresAt > 0 {
			expiresAt = time.Unix(apiToken.ExpiresAt, 0)
		}
		e.mu.Lock()
		e.cache[accessToken] = &cachedCopilotToken{
			token:     apiToken.Token,
			expiresAt: expiresAt,
		}
		e.mu.Unlock()

		return apiToken.Token, nil
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

func applyCopilotHeaders(r *http.Request, apiToken string, stream bool) {
	executor.ApplyAPIHeaders(r, executor.HeaderConfig{
		Token:     apiToken,
		UserAgent: executor.DefaultCopilotUserAgent,
		ExtraHeaders: map[string]string{
			"Editor-Version":         executor.CopilotEditorVersion,
			"Editor-Plugin-Version":  executor.CopilotPluginVersion,
			"Openai-Intent":          executor.CopilotOpenAIIntent,
			"Copilot-Integration-Id": executor.CopilotIntegrationID,
			"X-Request-Id":           uuid.NewString(),
		},
	}, stream)
}

func isHTTPSuccessCode(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
