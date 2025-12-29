package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/oauth"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/util"

	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/singleflight"
)

// Note: This executor uses the canonical translator (TranslateToGeminiCLI) for request/response translation.
// The "antigravity" format is used for upstream communication with the google antigravity.

const (
	antigravityStreamPath   = "/v1internal:streamGenerateContent"
	antigravityGeneratePath = "/v1internal:generateContent"
	antigravityModelsPath   = "/v1internal:fetchAvailableModels"
	antigravityAuthType     = "antigravity"
)

// =============================================================================
// Model Alias Functions - Use registry functions instead of hardcoded maps
// =============================================================================

// modelName2Alias converts upstream model name to user-facing alias.
// Returns empty string if the model should be hidden.
func modelName2Alias(upstreamName string) string {
	return registry.AntigravityUpstreamToID(upstreamName)
}

// alias2ModelName converts user-facing alias to upstream model name.
func alias2ModelName(modelID string) string {
	return registry.AntigravityIDToUpstream(modelID)
}

// Note: We use crypto/rand via uuid package for thread-safe random generation
// instead of math/rand which requires mutex protection

// AntigravityExecutor proxies requests to the antigravity upstream.
type AntigravityExecutor struct {
	cfg     *config.Config
	sfGroup singleflight.Group // Prevents concurrent token refresh stampede
}

// NewAntigravityExecutor constructs a new executor instance.
func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg}
}

// Identifier implements ProviderExecutor.
func (e *AntigravityExecutor) Identifier() string { return antigravityAuthType }

// PrepareRequest implements ProviderExecutor.
func (e *AntigravityExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

// =============================================================================
// Antigravity Stream Processor (implements StreamProcessor interface)
// =============================================================================

// antigravityStreamProcessor processes Antigravity SSE stream lines.
type antigravityStreamProcessor struct {
	translator *StreamTranslator
}

// ProcessLine implements StreamProcessor.ProcessLine for Antigravity streams.
func (p *antigravityStreamProcessor) ProcessLine(payload []byte) ([][]byte, *ir.Usage, error) {
	// Parse Gemini CLI chunk to IR events
	var events []ir.UnifiedEvent
	var err error
	if p.translator.ctx.ToolSchemaCtx != nil {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunkWithContext(payload, p.translator.ctx.ToolSchemaCtx)
	} else {
		events, err = (&from_ir.GeminiCLIProvider{}).ParseStreamChunk(payload)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("failed to parse chunk: %w", err)
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

// ProcessDone implements StreamProcessor.ProcessDone - flushes any pending chunks.
func (p *antigravityStreamProcessor) ProcessDone() ([][]byte, error) {
	return p.translator.Flush(), nil
}

// =============================================================================
// Execute Method (Non-Streaming) - Uses RetryHandler
// =============================================================================

// Execute handles non-streaming requests via the antigravity generate endpoint.
func (e *AntigravityExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return resp, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat

	// Translate request using canonical translator
	translated, errTranslate := TranslateToGeminiCLI(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if errTranslate != nil {
		return resp, fmt.Errorf("failed to translate request: %w", errTranslate)
	}

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	handler := NewRetryHandler(AntigravityRetryConfig())

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx := 0; idx < len(baseURLs); idx++ {
		handler.Reset()
		baseURL := baseURLs[idx]
		hasNext := idx+1 < len(baseURLs)

		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, false, opts.Alt, baseURL)
		if errReq != nil {
			return resp, errReq
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			lastStatus, lastBody, lastErr = 0, nil, errDo
			action, ctxErr := handler.HandleError(ctx, errDo, hasNext)
			if ctxErr != nil {
				return resp, ctxErr
			}
			switch action {
			case RetryActionContinueNext:
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback", baseURL)
				continue
			case RetryActionRetryCurrent:
				idx--
				continue
			default:
				if errors.Is(errDo, context.DeadlineExceeded) {
					return resp, NewTimeoutError("request timed out")
				}
				return resp, errDo
			}
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			return resp, errRead
		}

		// Handle response using RetryHandler
		action, ctxErr := handler.HandleResponse(ctx, httpResp.StatusCode, bodyBytes, hasNext)
		if ctxErr != nil {
			return resp, ctxErr
		}

		switch action {
		case RetryActionSuccess:
			// Success path - translate and return
			reporter.publish(ctx, extractUsageFromGeminiResponse(bodyBytes))
			translatedResp, errTranslateResp := TranslateGeminiCLIResponseNonStream(e.cfg, from, bodyBytes, req.Model)
			if errTranslateResp != nil {
				return resp, fmt.Errorf("failed to translate response: %w", errTranslateResp)
			}
			if translatedResp != nil {
				resp = cliproxyexecutor.Response{Payload: translatedResp}
			} else {
				resp = cliproxyexecutor.Response{Payload: bodyBytes}
			}
			reporter.ensurePublished(ctx)
			return resp, nil

		case RetryActionContinueNext:
			log.Debugf("antigravity executor: status %d on %s, trying next base url", httpResp.StatusCode, baseURL)
			lastStatus, lastBody, lastErr = httpResp.StatusCode, append([]byte(nil), bodyBytes...), nil
			continue

		case RetryActionRetryCurrent:
			lastStatus, lastBody, lastErr = httpResp.StatusCode, append([]byte(nil), bodyBytes...), nil
			idx--
			continue

		case RetryActionFail:
			log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
			retryAfter := ParseQuotaRetryDelay(bodyBytes)
			return resp, NewStatusError(httpResp.StatusCode, string(bodyBytes), retryAfter)
		}
	}

	// All base URLs exhausted
	switch {
	case lastStatus != 0:
		retryAfter := ParseQuotaRetryDelay(lastBody)
		err = NewStatusError(lastStatus, string(lastBody), retryAfter)
	case lastErr != nil:
		err = lastErr
	default:
		err = NewStatusError(http.StatusServiceUnavailable, "antigravity executor: no base url available", nil)
	}
	return resp, err
}

// =============================================================================
// ExecuteStream Method - Uses RetryHandler and RunSSEStream
// =============================================================================

// ExecuteStream handles streaming requests via the antigravity upstream.
func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	ctx = context.WithValue(ctx, altContextKey{}, "")

	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return nil, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat

	// Translate request and count tokens in one operation (uses shared IR)
	translation, errTranslate := TranslateToGeminiCLIWithTokens(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if errTranslate != nil {
		return nil, fmt.Errorf("failed to translate request: %w", errTranslate)
	}
	translated := translation.Payload
	estimatedInputTokens := translation.EstimatedInputTokens

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	handler := NewRetryHandler(AntigravityRetryConfig())

	var lastStatus int
	var lastBody []byte
	var lastErr error

	for idx := 0; idx < len(baseURLs); idx++ {
		handler.Reset()
		baseURL := baseURLs[idx]
		hasNext := idx+1 < len(baseURLs)

		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, true, opts.Alt, baseURL)
		if errReq != nil {
			return nil, errReq
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			lastStatus, lastBody, lastErr = 0, nil, errDo
			action, ctxErr := handler.HandleError(ctx, errDo, hasNext)
			if ctxErr != nil {
				return nil, ctxErr
			}
			switch action {
			case RetryActionContinueNext:
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback", baseURL)
				continue
			case RetryActionRetryCurrent:
				idx--
				continue
			default:
				if errors.Is(errDo, context.DeadlineExceeded) {
					return nil, NewTimeoutError("request timed out")
				}
				return nil, errDo
			}
		}

		// Handle non-2xx responses
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			bodyBytes, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				lastStatus, lastBody, lastErr = 0, nil, errRead
				if hasNext {
					log.Debugf("antigravity executor: read error on base url %s, retrying with fallback", baseURL)
					continue
				}
				return nil, errRead
			}

			action, ctxErr := handler.HandleResponse(ctx, httpResp.StatusCode, bodyBytes, hasNext)
			if ctxErr != nil {
				return nil, ctxErr
			}

			switch action {
			case RetryActionContinueNext:
				log.Debugf("antigravity executor: status %d on %s, trying next base url", httpResp.StatusCode, baseURL)
				lastStatus, lastBody, lastErr = httpResp.StatusCode, append([]byte(nil), bodyBytes...), nil
				continue
			case RetryActionRetryCurrent:
				lastStatus, lastBody, lastErr = httpResp.StatusCode, append([]byte(nil), bodyBytes...), nil
				idx--
				continue
			case RetryActionFail:
				retryAfter := ParseQuotaRetryDelay(bodyBytes)
				return nil, NewStatusError(httpResp.StatusCode, string(bodyBytes), retryAfter)
			}
		}

		// Success - create stream processor and run SSE stream
		streamCtx := NewStreamContextWithTools(opts.OriginalRequest)
		streamCtx.EstimatedInputTokens = estimatedInputTokens
		messageID := "chatcmpl-" + req.Model

		translator := NewStreamTranslator(e.cfg, from, from.String(), req.Model, messageID, streamCtx)
		processor := &antigravityStreamProcessor{
			translator: translator,
		}

		stream = RunSSEStream(ctx, httpResp.Body, reporter, processor, StreamConfig{
			ExecutorName:    "antigravity",
			Preprocessor:    GeminiPreprocessor(),
			EnsurePublished: true,
		})
		return stream, nil
	}

	// All base URLs exhausted
	switch {
	case lastStatus != 0:
		retryAfter := ParseQuotaRetryDelay(lastBody)
		err = NewStatusError(lastStatus, string(lastBody), retryAfter)
	case lastErr != nil:
		err = lastErr
	default:
		err = NewStatusError(http.StatusServiceUnavailable, "antigravity executor: no base url available", nil)
	}
	return nil, err
}

// =============================================================================
// Other Methods
// =============================================================================

// Refresh refreshes the OAuth token using the refresh token.
func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return auth, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone())
	if errRefresh != nil {
		return nil, errRefresh
	}
	return updated, nil
}

// CountTokens is not supported for the antigravity provider.
func (e *AntigravityExecutor) CountTokens(context.Context, *cliproxyauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, NewStatusError(http.StatusNotImplemented, "count tokens not supported", nil)
}

// FetchAntigravityModels retrieves available models using the supplied auth.
func FetchAntigravityModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	exec := &AntigravityExecutor{cfg: cfg}
	token, updatedAuth, errToken := exec.ensureAccessToken(ctx, auth)
	if errToken != nil || token == "" {
		return nil
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	fetchCfg := CloudCodeFetchConfig{
		BaseURLs:     antigravityBaseURLFallbackOrder(auth),
		Token:        token,
		ProviderType: antigravityAuthType,
		UserAgent:    resolveUserAgent(auth),
		Host:         resolveHost(antigravityBaseURLFallbackOrder(auth)[0]),
		AliasFunc:    modelName2Alias,
	}

	return FetchCloudCodeModels(ctx, httpClient, fetchCfg)
}

// =============================================================================
// Token Management
// =============================================================================

// tokenRefreshResult holds the result of a token refresh operation for singleflight.
type tokenRefreshResult struct {
	token string
	auth  *cliproxyauth.Auth
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *cliproxyauth.Auth) (string, *cliproxyauth.Auth, error) {
	if auth == nil {
		return "", nil, NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}

	// Fast path: token still valid
	accessToken := metaStringValue(auth.Metadata, "access_token")
	expiry := tokenExpiry(auth.Metadata)
	if accessToken != "" && expiry.After(time.Now().Add(DefaultRefreshSkew)) {
		return accessToken, nil, nil
	}

	// Use singleflight to prevent concurrent refresh stampede for same auth
	result, err, _ := e.sfGroup.Do(auth.ID, func() (interface{}, error) {
		// Double-check inside singleflight - another goroutine may have just refreshed
		accessToken := metaStringValue(auth.Metadata, "access_token")
		expiry := tokenExpiry(auth.Metadata)
		if accessToken != "" && expiry.After(time.Now().Add(DefaultRefreshSkew)) {
			return tokenRefreshResult{token: accessToken, auth: nil}, nil
		}

		updated, errRefresh := e.refreshToken(ctx, auth.Clone())
		if errRefresh != nil {
			return nil, errRefresh
		}
		return tokenRefreshResult{
			token: metaStringValue(updated.Metadata, "access_token"),
			auth:  updated,
		}, nil
	})

	if err != nil {
		return "", nil, err
	}

	res := result.(tokenRefreshResult)
	return res.token, res.auth, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, NewStatusError(http.StatusUnauthorized, "missing refresh token", nil)
	}

	form := url.Values{}
	form.Set("client_id", oauth.AntigravityClientID)
	form.Set("client_secret", oauth.AntigravityClientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if errReq != nil {
		return auth, errReq
	}
	httpReq.Header.Set("Host", "oauth2.googleapis.com")
	httpReq.Header.Set("User-Agent", DefaultAntigravityUserAgent)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		if errors.Is(errDo, context.DeadlineExceeded) {
			return auth, NewTimeoutError("request timed out")
		}
		return auth, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
	}()

	bodyBytes, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return auth, errRead
	}

	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return auth, NewStatusError(httpResp.StatusCode, string(bodyBytes), nil)
	}

	var tokenResp struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
		TokenType    string `json:"token_type"`
	}
	if errUnmarshal := json.Unmarshal(bodyBytes, &tokenResp); errUnmarshal != nil {
		return auth, errUnmarshal
	}

	// Validate token response
	if tokenResp.AccessToken == "" {
		return auth, NewStatusError(http.StatusUnauthorized, "invalid token response: missing access_token", nil)
	}
	if tokenResp.ExpiresIn < 0 {
		return auth, NewStatusError(http.StatusUnauthorized, "invalid token response: negative expires_in", nil)
	}
	if tokenResp.ExpiresIn == 0 {
		tokenResp.ExpiresIn = 3600
		log.Debugf("antigravity: token response missing expires_in, using default 3600s")
	}

	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = tokenResp.AccessToken
	if tokenResp.RefreshToken != "" {
		auth.Metadata["refresh_token"] = tokenResp.RefreshToken
	}
	auth.Metadata["expires_in"] = tokenResp.ExpiresIn
	auth.Metadata["timestamp"] = time.Now().UnixMilli()
	auth.Metadata["expired"] = time.Now().Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339)
	auth.Metadata["type"] = antigravityAuthType
	return auth, nil
}

// =============================================================================
// Request Building
// =============================================================================

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string) (*http.Request, error) {
	if token == "" {
		return nil, NewStatusError(http.StatusUnauthorized, "missing access token", nil)
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(128) // estimate URL length
	ub.WriteString(base)
	ub.WriteString(path)
	if stream {
		if alt != "" {
			ub.WriteString("?$alt=")
			ub.WriteString(url.QueryEscape(alt))
		} else {
			ub.WriteString("?alt=sse")
		}
	} else if alt != "" {
		ub.WriteString("?$alt=")
		ub.WriteString(url.QueryEscape(alt))
	}

	// Extract project_id from auth metadata if available
	projectID := ""
	if auth != nil && auth.Metadata != nil {
		if pid, ok := auth.Metadata["project_id"].(string); ok {
			projectID = strings.TrimSpace(pid)
		}
	}
	payload = geminiToAntigravity(modelName, payload, projectID)
	payload, _ = sjson.SetBytes(payload, "model", alias2ModelName(modelName))

	if strings.Contains(modelName, "claude") {
		strJSON := string(payload)
		paths := make([]string, 0)
		util.Walk(gjson.ParseBytes(payload), "", "parametersJsonSchema", &paths)
		for _, p := range paths {
			strJSON, _ = util.RenameKey(strJSON, p, p[:len(p)-len("parametersJsonSchema")]+"parameters")
		}

		strJSON = util.DeleteKey(strJSON, "$schema")
		strJSON = util.DeleteKey(strJSON, "maxItems")
		strJSON = util.DeleteKey(strJSON, "minItems")
		strJSON = util.DeleteKey(strJSON, "minLength")
		strJSON = util.DeleteKey(strJSON, "maxLength")
		strJSON = util.DeleteKey(strJSON, "exclusiveMinimum")
		strJSON = util.DeleteKey(strJSON, "exclusiveMaximum")
		strJSON = util.DeleteKey(strJSON, "$ref")
		strJSON = util.DeleteKey(strJSON, "$defs")

		payload = []byte(strJSON)
	}

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, ub.String(), bytes.NewReader(payload))
	if errReq != nil {
		return nil, errReq
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if host := resolveHost(base); host != "" {
		httpReq.Host = host
	}

	return httpReq, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

func tokenExpiry(metadata map[string]any) time.Time {
	if metadata == nil {
		return time.Time{}
	}
	if expStr, ok := metadata["expired"].(string); ok {
		expStr = strings.TrimSpace(expStr)
		if expStr != "" {
			if parsed, errParse := time.Parse(time.RFC3339, expStr); errParse == nil {
				return parsed
			}
		}
	}
	expiresIn, hasExpires := int64Value(metadata["expires_in"])
	tsMs, hasTimestamp := int64Value(metadata["timestamp"])
	if hasExpires && hasTimestamp {
		return time.Unix(0, tsMs*int64(time.Millisecond)).Add(time.Duration(expiresIn) * time.Second)
	}
	return time.Time{}
}

func metaStringValue(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata[key]; ok {
		switch typed := v.(type) {
		case string:
			return strings.TrimSpace(typed)
		case []byte:
			return strings.TrimSpace(string(typed))
		}
	}
	return ""
}

func int64Value(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		if i, errParse := typed.Int64(); errParse == nil {
			return i, true
		}
	case string:
		if strings.TrimSpace(typed) == "" {
			return 0, false
		}
		if i, errParse := strconv.ParseInt(strings.TrimSpace(typed), 10, 64); errParse == nil {
			return i, true
		}
	}
	return 0, false
}

func buildBaseURL(auth *cliproxyauth.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return AntigravityBaseURLDaily
}

func resolveHost(base string) string {
	parsed, errParse := url.Parse(base)
	if errParse != nil {
		return ""
	}
	if parsed.Host != "" {
		return parsed.Host
	}
	return strings.TrimPrefix(strings.TrimPrefix(base, "https://"), "http://")
}

func resolveUserAgent(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if auth.Attributes != nil {
			if ua := strings.TrimSpace(auth.Attributes["user_agent"]); ua != "" {
				return ua
			}
		}
		if auth.Metadata != nil {
			if ua, ok := auth.Metadata["user_agent"].(string); ok && strings.TrimSpace(ua) != "" {
				return strings.TrimSpace(ua)
			}
		}
	}
	return DefaultAntigravityUserAgent
}

func antigravityBaseURLFallbackOrder(auth *cliproxyauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{
		AntigravityBaseURLDaily,
		AntigravityBaseURLProd,
	}
}

func resolveCustomAntigravityBaseURL(auth *cliproxyauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["base_url"]); v != "" {
			return strings.TrimSuffix(v, "/")
		}
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["base_url"].(string); ok {
			v = strings.TrimSpace(v)
			if v != "" {
				return strings.TrimSuffix(v, "/")
			}
		}
	}
	return ""
}

// =============================================================================
// Payload Transformation
// =============================================================================

func geminiToAntigravity(modelName string, payload []byte, projectID string) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		// If unmarshal fails, return original payload
		return payload
	}

	// Set top-level fields
	data["model"] = modelName
	data["userAgent"] = "antigravity"
	if projectID != "" {
		data["project"] = projectID
	} else {
		data["project"] = generateProjectID()
	}
	data["requestId"] = generateRequestID()

	// Modify request object
	if req, ok := data["request"].(map[string]interface{}); ok {
		req["sessionId"] = generateSessionID()
		delete(req, "safetySettings")

		// Set toolConfig
		if toolConfig, ok := req["toolConfig"].(map[string]interface{}); ok {
			if funcCallingConfig, ok := toolConfig["functionCallingConfig"].(map[string]interface{}); ok {
				funcCallingConfig["mode"] = "VALIDATED"
			} else {
				toolConfig["functionCallingConfig"] = map[string]interface{}{"mode": "VALIDATED"}
			}
		} else {
			req["toolConfig"] = map[string]interface{}{
				"functionCallingConfig": map[string]interface{}{"mode": "VALIDATED"},
			}
		}

		if strings.Contains(modelName, "claude") {
			// Handle tools modification
			if tools, ok := req["tools"].([]interface{}); ok {
				for _, tool := range tools {
					if toolMap, ok := tool.(map[string]interface{}); ok {
						if funcDecls, ok := toolMap["functionDeclarations"].([]interface{}); ok {
							for _, funcDecl := range funcDecls {
								if funcDeclMap, ok := funcDecl.(map[string]interface{}); ok {
									if paramsSchema, exists := funcDeclMap["parametersJsonSchema"]; exists {
										funcDeclMap["parameters"] = paramsSchema
										delete(funcDeclMap, "parametersJsonSchema")
										// Delete $schema from parameters if it exists
										if params, ok := funcDeclMap["parameters"].(map[string]interface{}); ok {
											delete(params, "$schema")
										}
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Marshal the modified data
	result, err := json.Marshal(data)
	if err != nil {
		// If marshal fails, return original payload
		return payload
	}

	if strings.Contains(modelName, "claude") {
		// Batch delete operations using util.DeleteKey
		strJSON := string(result)
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.$schema")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.$ref")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.$defs")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.exclusiveMinimum")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.exclusiveMaximum")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.minItems")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.maxItems")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.minLength")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.maxLength")
		result = []byte(strJSON)
	}

	return result
}

func generateRequestID() string {
	return "agent-" + uuid.NewString()
}

func generateSessionID() string {
	uuidStr := uuid.NewString()
	return "-" + uuidStr[:8] + uuidStr[9:13] + uuidStr[14:18]
}

var (
	projectIDAdjectives = []string{"useful", "bright", "swift", "calm", "bold"}
	projectIDNouns      = []string{"fuze", "wave", "spark", "flow", "core"}
)

func generateProjectID() string {
	uuidBytes := []byte(uuid.NewString())
	adj := projectIDAdjectives[int(uuidBytes[0])%len(projectIDAdjectives)]
	noun := projectIDNouns[int(uuidBytes[1])%len(projectIDNouns)]
	randomPart := strings.ToLower(uuid.NewString())[:5]
	return adj + "-" + noun + "-" + randomPart
}
