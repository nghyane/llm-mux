package executor

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
	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/singleflight"
)

// GitHubCopilotExecutor handles requests to the GitHub Copilot API.
type GitHubCopilotExecutor struct {
	cfg     *config.Config
	mu      sync.RWMutex
	cache   map[string]*cachedCopilotToken
	sfGroup singleflight.Group
}

type cachedCopilotToken struct {
	token     string
	expiresAt time.Time
}

func NewGitHubCopilotExecutor(cfg *config.Config) *GitHubCopilotExecutor {
	return &GitHubCopilotExecutor{
		cfg:   cfg,
		cache: make(map[string]*cachedCopilotToken),
	}
}

func (e *GitHubCopilotExecutor) Identifier() string { return GitHubCopilotAuthType }

func (e *GitHubCopilotExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

func (e *GitHubCopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	// Translate to OpenAI format (Copilot uses OpenAI-compatible API)
	body, errTranslate := TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, nil)
	if errTranslate != nil {
		return resp, errTranslate
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)
	body, _ = sjson.SetBytes(body, "stream", false)

	url := GitHubCopilotDefaultBaseURL + GitHubCopilotChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyCopilotHeaders(httpReq, apiToken, false)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return resp, NewTimeoutError("request timed out")
		}
		return resp, err
	}
	defer func() { _ = httpResp.Body.Close() }()

	if !isHTTPSuccessCode(httpResp.StatusCode) {
		result := HandleHTTPError(httpResp, "github-copilot executor")
		return resp, result.Error
	}

	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}

	detail := extractUsageFromOpenAIResponse(data)
	if detail != nil && detail.TotalTokens > 0 {
		reporter.publish(ctx, detail)
	}

	// Translate response back from OpenAI format to source format
	fromOpenAI := sdktranslator.FromString("openai")
	translatedResp, errTranslate := TranslateResponseNonStream(e.cfg, fromOpenAI, from, data, req.Model)
	if errTranslate != nil {
		return resp, errTranslate
	}
	if translatedResp != nil {
		resp = cliproxyexecutor.Response{Payload: translatedResp}
	} else {
		resp = cliproxyexecutor.Response{Payload: data}
	}
	reporter.ensurePublished(ctx)
	return resp, nil
}

func (e *GitHubCopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiToken, errToken := e.ensureAPIToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	body, errTranslate := TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, nil)
	if errTranslate != nil {
		return nil, errTranslate
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)
	body, _ = sjson.SetBytes(body, "stream", true)
	body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)

	url := GitHubCopilotDefaultBaseURL + GitHubCopilotChatPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyCopilotHeaders(httpReq, apiToken, true)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewTimeoutError("request timed out")
		}
		return nil, err
	}

	if !isHTTPSuccessCode(httpResp.StatusCode) {
		result := HandleHTTPError(httpResp, "github-copilot executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	messageID := uuid.NewString()
	processor := NewOpenAIStreamProcessor(e.cfg, from, req.Model, messageID)

	return RunSSEStream(ctx, httpResp.Body, reporter, processor, StreamConfig{
		ExecutorName:    "github-copilot executor",
		SkipDoneInData:  true,
		EnsurePublished: true,
	}), nil
}

func (e *GitHubCopilotExecutor) CountTokens(_ context.Context, _ *cliproxyauth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, NewStatusError(http.StatusNotImplemented, "count tokens not supported for github-copilot", nil)
}

func (e *GitHubCopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}

	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return auth, nil
	}

	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	_, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
	if err != nil {
		return nil, NewStatusError(http.StatusUnauthorized, fmt.Sprintf("github-copilot token validation failed: %v", err), nil)
	}

	return auth, nil
}

func (e *GitHubCopilotExecutor) ensureAPIToken(ctx context.Context, auth *cliproxyauth.Auth) (string, error) {
	if auth == nil {
		return "", NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}

	accessToken := metaStringValue(auth.Metadata, "access_token")
	if accessToken == "" {
		return "", NewStatusError(http.StatusUnauthorized, "missing github access token", nil)
	}

	// Check cache first
	e.mu.RLock()
	if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(TokenExpiryBuffer)) {
		e.mu.RUnlock()
		return cached.token, nil
	}
	e.mu.RUnlock()

	// Use singleflight to prevent cache stampede: only one goroutine fetches token while others wait
	result, err, _ := e.sfGroup.Do(accessToken, func() (interface{}, error) {
		// Check cache again in case another goroutine just filled it
		e.mu.RLock()
		if cached, ok := e.cache[accessToken]; ok && cached.expiresAt.After(time.Now().Add(TokenExpiryBuffer)) {
			e.mu.RUnlock()
			return cached.token, nil
		}
		e.mu.RUnlock()

		copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
		apiToken, err := copilotAuth.GetCopilotAPIToken(ctx, accessToken)
		if err != nil {
			return "", NewStatusError(http.StatusUnauthorized, fmt.Sprintf("failed to get copilot api token: %v", err), nil)
		}

		expiresAt := time.Now().Add(GitHubCopilotTokenCacheTTL)
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
	ApplyAPIHeaders(r, HeaderConfig{
		Token:     apiToken,
		UserAgent: DefaultCopilotUserAgent,
		ExtraHeaders: map[string]string{
			"Editor-Version":         CopilotEditorVersion,
			"Editor-Plugin-Version":  CopilotPluginVersion,
			"Openai-Intent":          CopilotOpenAIIntent,
			"Copilot-Integration-Id": CopilotIntegrationID,
			"X-Request-Id":           uuid.NewString(),
		},
	}, stream)
}

func isHTTPSuccessCode(statusCode int) bool {
	return statusCode >= 200 && statusCode < 300
}
