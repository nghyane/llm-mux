package executor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/oauth"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/ir"

	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/tidwall/sjson"
)

const (
	antigravityStreamPath      = "/v1internal:streamGenerateContent"
	antigravityGeneratePath    = "/v1internal:generateContent"
	antigravityCountTokensPath = "/v1internal:countTokens"
	antigravityModelsPath      = "/v1internal:fetchAvailableModels"
	antigravityAuthType        = "antigravity"
)

func modelName2Alias(upstreamName string) string {
	return registry.AntigravityUpstreamToID(upstreamName)
}

func alias2ModelName(modelID string) string {
	return registry.AntigravityIDToUpstream(modelID)
}

type AntigravityExecutor struct {
	cfg          *config.Config
	tokenRefresh *TokenRefreshGroup
}

func NewAntigravityExecutor(cfg *config.Config) *AntigravityExecutor {
	return &AntigravityExecutor{cfg: cfg, tokenRefresh: NewTokenRefreshGroup()}
}

func (e *AntigravityExecutor) Identifier() string { return antigravityAuthType }

func (e *AntigravityExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *AntigravityExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
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

		action, ctxErr := handler.HandleResponse(ctx, httpResp.StatusCode, bodyBytes, hasNext)
		if ctxErr != nil {
			return resp, ctxErr
		}

		switch action {
		case RetryActionSuccess:
			reporter.publish(ctx, extractUsageFromGeminiResponse(bodyBytes))
			fromFormat := provider.FromString("gemini-cli")
			translatedResp, errTranslateResp := TranslateResponseNonStream(e.cfg, fromFormat, from, bodyBytes, req.Model)
			if errTranslateResp != nil {
				return resp, fmt.Errorf("failed to translate response: %w", errTranslateResp)
			}
			if translatedResp != nil {
				resp = provider.Response{Payload: translatedResp}
			} else {
				resp = provider.Response{Payload: bodyBytes}
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

func (e *AntigravityExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (stream <-chan provider.StreamChunk, err error) {
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

		reqStart := time.Now()
		log.Debugf("antigravity stream: starting request to %s, payload size: %d bytes", baseURL, len(translated))
		httpResp, errDo := httpClient.Do(httpReq)
		ttfb := time.Since(reqStart)
		if httpResp != nil {
			log.Debugf("antigravity stream: TTFB=%v, status=%d", ttfb, httpResp.StatusCode)
		} else {
			log.Debugf("antigravity stream: TTFB=%v, err=%v", ttfb, errDo)
		}
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

		streamCtx := NewStreamContextWithTools(opts.OriginalRequest)
		streamCtx.EstimatedInputTokens = estimatedInputTokens
		messageID := "chatcmpl-" + req.Model

		translator := NewStreamTranslator(e.cfg, from, from.String(), req.Model, messageID, streamCtx)
		processor := NewGeminiCLIStreamProcessor(translator)

		stream = RunSSEStream(ctx, httpResp.Body, reporter, processor, StreamConfig{
			ExecutorName:    "antigravity",
			Preprocessor:    GeminiPreprocessor(),
			EnsurePublished: true,
		})
		return stream, nil
	}

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

func (e *AntigravityExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	if auth == nil {
		return auth, nil
	}
	updated, errRefresh := e.refreshToken(ctx, auth.Clone())
	if errRefresh != nil {
		return nil, errRefresh
	}
	return updated, nil
}

func (e *AntigravityExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	token, updatedAuth, errToken := e.ensureAccessToken(ctx, auth)
	if errToken != nil {
		return provider.Response{}, errToken
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	from := opts.SourceFormat
	translated, errTranslate := TranslateToGeminiCLI(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if errTranslate != nil {
		return provider.Response{}, fmt.Errorf("failed to translate request: %w", errTranslate)
	}

	translated = deleteJSONField(translated, "project")
	translated = deleteJSONField(translated, "model")
	translated = deleteJSONField(translated, "request.safetySettings")

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var lastStatus int
	var lastBody []byte

	for idx := 0; idx < len(baseURLs); idx++ {
		baseURL := baseURLs[idx]

		httpReq, errReq := e.buildCountTokensRequest(ctx, token, translated, baseURL)
		if errReq != nil {
			return provider.Response{}, errReq
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			if errors.Is(errDo, context.DeadlineExceeded) {
				return provider.Response{}, NewTimeoutError("count tokens request timed out")
			}
			continue
		}

		body, _ := io.ReadAll(httpResp.Body)
		httpResp.Body.Close()

		lastStatus = httpResp.StatusCode
		lastBody = body

		if httpResp.StatusCode == http.StatusOK {
			return provider.Response{Payload: body}, nil
		}

		if httpResp.StatusCode >= 500 && idx+1 < len(baseURLs) {
			continue
		}
	}

	if lastStatus == 0 {
		return provider.Response{}, NewStatusError(http.StatusServiceUnavailable, "all endpoints failed", nil)
	}
	return provider.Response{}, NewStatusError(lastStatus, string(lastBody), nil)
}

func (e *AntigravityExecutor) buildCountTokensRequest(ctx context.Context, token string, payload []byte, baseURL string) (*http.Request, error) {
	if token == "" {
		return nil, NewStatusError(http.StatusUnauthorized, "missing access token", nil)
	}

	base := strings.TrimSuffix(baseURL, "/")
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(128)
	ub.WriteString(base)
	ub.WriteString(antigravityCountTokensPath)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, ub.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}

	SetCommonHeaders(httpReq, "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("Accept", "application/json")
	applyGeminiCLIHeaders(httpReq)

	return httpReq, nil
}

func FetchAntigravityModels(ctx context.Context, auth *provider.Auth, cfg *config.Config) []*registry.ModelInfo {
	exec := NewAntigravityExecutor(cfg)
	token, updatedAuth, errToken := exec.ensureAccessToken(ctx, auth)
	if errToken != nil || token == "" {
		return nil
	}
	if updatedAuth != nil {
		auth = updatedAuth
	}

	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	fetchCfg := CloudCodeFetchConfig{
		BaseURLs:     baseURLs,
		Token:        token,
		ProviderType: antigravityAuthType,
		UserAgent:    resolveUserAgent(auth),
		Host:         ResolveHost(baseURLs[0]),
		AliasFunc:    modelName2Alias,
	}

	return FetchCloudCodeModels(ctx, httpClient, fetchCfg)
}

type tokenRefreshResult struct {
	token string
	auth  *provider.Auth
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *provider.Auth) (string, *provider.Auth, error) {
	if auth == nil {
		return "", nil, NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}

	accessToken := MetaStringValue(auth.Metadata, "access_token")
	expiry := TokenExpiry(auth.Metadata)
	if accessToken != "" && expiry.After(time.Now().Add(DefaultRefreshSkew)) {
		return accessToken, nil, nil
	}

	result, err := e.tokenRefresh.Do(auth.ID, func(refreshCtx context.Context) (any, error) {
		accessToken := MetaStringValue(auth.Metadata, "access_token")
		expiry := TokenExpiry(auth.Metadata)
		if accessToken != "" && expiry.After(time.Now().Add(DefaultRefreshSkew)) {
			return tokenRefreshResult{token: accessToken, auth: nil}, nil
		}

		updated, errRefresh := e.refreshToken(refreshCtx, auth.Clone())
		if errRefresh != nil {
			return nil, errRefresh
		}
		return tokenRefreshResult{
			token: MetaStringValue(updated.Metadata, "access_token"),
			auth:  updated,
		}, nil
	})

	if err != nil {
		return "", nil, err
	}

	res := result.(tokenRefreshResult)
	return res.token, res.auth, nil
}

func (e *AntigravityExecutor) refreshToken(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	if auth == nil {
		return nil, NewStatusError(http.StatusUnauthorized, "missing auth", nil)
	}
	refreshToken := MetaStringValue(auth.Metadata, "refresh_token")
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

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *provider.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string) (*http.Request, error) {
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
	ub.Grow(128)
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

	projectID := MetaStringValue(auth.Metadata, "project_id")
	payload = geminiToAntigravity(modelName, payload, projectID)
	payload, _ = sjson.SetBytes(payload, "model", alias2ModelName(modelName))

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, ub.String(), bytes.NewReader(payload))
	if errReq != nil {
		return nil, errReq
	}
	SetCommonHeaders(httpReq, "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+token)
	httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
	if stream {
		httpReq.Header.Set("Accept", "text/event-stream")
	} else {
		httpReq.Header.Set("Accept", "application/json")
	}
	if host := ResolveHost(base); host != "" {
		httpReq.Host = host
	}

	return httpReq, nil
}

func buildBaseURL(auth *provider.Auth) string {
	if baseURLs := antigravityBaseURLFallbackOrder(auth); len(baseURLs) > 0 {
		return baseURLs[0]
	}
	return AntigravityBaseURLDaily
}

func resolveUserAgent(auth *provider.Auth) string {
	if auth != nil {
		if ua := AttrStringValue(auth.Attributes, "user_agent"); ua != "" {
			return ua
		}
		if ua := MetaStringValue(auth.Metadata, "user_agent"); ua != "" {
			return ua
		}
	}
	return DefaultAntigravityUserAgent
}

func antigravityBaseURLFallbackOrder(auth *provider.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	return []string{
		AntigravityBaseURLDaily,
		AntigravityBaseURLProd,
	}
}

func resolveCustomAntigravityBaseURL(auth *provider.Auth) string {
	if auth == nil {
		return ""
	}
	if v := AttrStringValue(auth.Attributes, "base_url"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	if v := MetaStringValue(auth.Metadata, "base_url"); v != "" {
		return strings.TrimSuffix(v, "/")
	}
	return ""
}

// defaultAntigravityStopSequences prevents model from hallucinating role markers.
// These are mandatory for Antigravity IDE models (Gemini/Claude variants).
var defaultAntigravityStopSequences = []string{
	"<|user|>",
	"<|bot|>",
	"<|context_request|>",
	"<|endoftext|>",
	"<|end_of_turn|>",
}

func geminiToAntigravity(modelName string, payload []byte, projectID string) []byte {
	var data map[string]interface{}
	if err := json.Unmarshal(payload, &data); err != nil {
		return payload
	}

	data["model"] = modelName
	data["userAgent"] = "antigravity"
	if projectID != "" {
		data["project"] = projectID
	} else {
		data["project"] = generateProjectID()
	}
	data["requestId"] = generateRequestID()

	if req, ok := data["request"].(map[string]interface{}); ok {
		req["session_id"] = deriveStableSessionID(req)

		delete(req, "safetySettings")

		applyAntigravityGenerationDefaults(req)
		wrapFunctionDeclarationsIndependently(req)
		applyToolConfig(req)

		if strings.Contains(modelName, "claude") {
			convertParametersJsonSchemaForClaude(req)
		}
	}

	result, err := json.Marshal(data)
	if err != nil {
		return payload
	}

	validated, report := ir.ValidateAntigravityPayload(result, modelName)
	report.LogDebug()

	return validated
}

// applyAntigravityGenerationDefaults applies Antigravity-specific generation config defaults.
func applyAntigravityGenerationDefaults(req map[string]interface{}) {
	genConfig, ok := req["generationConfig"].(map[string]interface{})
	if !ok {
		genConfig = make(map[string]interface{})
		req["generationConfig"] = genConfig
	}

	if _, exists := genConfig["topP"]; !exists {
		genConfig["topP"] = float64(1)
	}
	if _, exists := genConfig["topK"]; !exists {
		genConfig["topK"] = float64(40)
	}
	if _, exists := genConfig["candidateCount"]; !exists {
		genConfig["candidateCount"] = 1
	}
	if _, exists := genConfig["temperature"]; !exists {
		genConfig["temperature"] = 0.4
	}

	existing := make(map[string]bool)
	var merged []string

	if stopSeqs, ok := genConfig["stopSequences"].([]interface{}); ok {
		for _, s := range stopSeqs {
			if str, ok := s.(string); ok {
				existing[str] = true
				merged = append(merged, str)
			}
		}
	}

	for _, seq := range defaultAntigravityStopSequences {
		if !existing[seq] {
			merged = append(merged, seq)
		}
	}

	genConfig["stopSequences"] = merged
}

func wrapFunctionDeclarationsIndependently(req map[string]interface{}) {
	tools, ok := req["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return
	}

	var allFuncDecls []interface{}
	var nonFunctionTools []interface{}

	// Collect all function declarations and separate non-function tools
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			nonFunctionTools = append(nonFunctionTools, tool)
			continue
		}

		funcDecls, hasFuncDecls := toolMap["functionDeclarations"].([]interface{})
		if hasFuncDecls && len(funcDecls) > 0 {
			allFuncDecls = append(allFuncDecls, funcDecls...)
		} else {
			// Non-function tool (googleSearch, codeExecution, etc.)
			nonFunctionTools = append(nonFunctionTools, tool)
		}
	}

	if len(allFuncDecls) == 0 {
		return
	}

	// Create new tools array with each function wrapped independently
	newTools := make([]interface{}, 0, len(allFuncDecls)+len(nonFunctionTools))
	for _, funcDecl := range allFuncDecls {
		newTools = append(newTools, map[string]interface{}{
			"functionDeclarations": []interface{}{funcDecl},
		})
	}
	newTools = append(newTools, nonFunctionTools...)

	req["tools"] = newTools
}

func applyToolConfig(req map[string]interface{}) {
	tools, ok := req["tools"].([]interface{})
	if !ok || len(tools) == 0 {
		return
	}

	for _, tool := range tools {
		if toolMap, ok := tool.(map[string]interface{}); ok {
			if _, exists := toolMap["functionDeclarations"]; exists {
				req["toolConfig"] = map[string]interface{}{
					"functionCallingConfig": map[string]interface{}{
						"mode": "VALIDATED",
					},
				}
				return
			}
		}
	}
}

func convertParametersJsonSchemaForClaude(req map[string]interface{}) {
	tools, ok := req["tools"].([]interface{})
	if !ok {
		return
	}
	for _, tool := range tools {
		toolMap, ok := tool.(map[string]interface{})
		if !ok {
			continue
		}
		funcDecls, ok := toolMap["functionDeclarations"].([]interface{})
		if !ok {
			continue
		}
		for _, funcDecl := range funcDecls {
			funcDeclMap, ok := funcDecl.(map[string]interface{})
			if !ok {
				continue
			}
			if paramsSchema, exists := funcDeclMap["parametersJsonSchema"]; exists {
				funcDeclMap["parameters"] = paramsSchema
				delete(funcDeclMap, "parametersJsonSchema")
			}
		}
	}
}

func generateRequestID() string {
	return "req-" + uuid.NewString()
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

// deriveStableSessionID generates a stable session ID from the first user message content.
// This enables prompt caching as cache is scoped to session + organization.
// Returns "session-{hash32}" where hash32 is first 32 chars of SHA256 hex digest.
// Falls back to random UUID if no user message content found.
func deriveStableSessionID(req map[string]interface{}) string {
	contents, ok := req["contents"].([]interface{})
	if !ok || len(contents) == 0 {
		return "session-" + uuid.NewString()
	}

	for _, content := range contents {
		contentMap, ok := content.(map[string]interface{})
		if !ok {
			continue
		}

		role, _ := contentMap["role"].(string)
		if role != "user" {
			continue
		}

		parts, ok := contentMap["parts"].([]interface{})
		if !ok || len(parts) == 0 {
			continue
		}

		var textContent strings.Builder
		for _, part := range parts {
			partMap, ok := part.(map[string]interface{})
			if !ok {
				continue
			}
			if text, hasText := partMap["text"].(string); hasText && text != "" {
				textContent.WriteString(text)
			}
		}

		if textContent.Len() > 0 {
			hash := sha256.Sum256([]byte(textContent.String()))
			return "session-" + hex.EncodeToString(hash[:])[:32]
		}
	}

	return "session-" + uuid.NewString()
}
