package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/nghyane/llm-mux/internal/util"

	// "github.com/nghyane/llm-mux/internal/translator/ir"
	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/sync/singleflight"
)

// Note: This executor uses the canonical translator (TranslateToGeminiCLI) for request/response translation.
// The "antigravity" format is used for upstream communication with the Google Cloud Code API.

const (
	antigravityBaseURLDaily = "https://daily-cloudcode-pa.sandbox.googleapis.com"
	antigravityBaseURLProd  = "https://cloudcode-pa.googleapis.com"
	antigravityStreamPath   = "/v1internal:streamGenerateContent"
	antigravityGeneratePath = "/v1internal:generateContent"
	antigravityModelsPath   = "/v1internal:fetchAvailableModels"
	defaultAntigravityAgent = "antigravity/1.11.5 windows/amd64"
	antigravityAuthType     = "antigravity"
	refreshSkew             = 3000 * time.Second
)

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
	translated, errTranslate := TranslateToGeminiCLI(e.cfg, from, req.Model, bytes.Clone(req.Payload), false, req.Metadata)
	if errTranslate != nil {
		return resp, fmt.Errorf("failed to translate request: %w", errTranslate)
	}

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var lastStatus int
	var lastBody []byte
	var lastErr error
	retrier := &rateLimitRetrier{}

	for idx := 0; idx < len(baseURLs); idx++ {
		baseURL := baseURLs[idx]
		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, false, opts.Alt, baseURL)
		if errReq != nil {
			err = errReq
			return resp, err
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = errDo
			return resp, err
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			err = errRead
			return resp, err
		}

		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			log.Debugf("antigravity executor: upstream error status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), bodyBytes))
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), bodyBytes...)
			lastErr = nil
			if httpResp.StatusCode == http.StatusTooManyRequests {
				hasNextBaseURL := idx+1 < len(baseURLs)
				if hasNextBaseURL {
					log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				}
				action, ctxErr := retrier.handleRateLimit(ctx, hasNextBaseURL, bodyBytes)
				if ctxErr != nil {
					err = ctxErr
					return resp, err
				}
				switch action {
				case rateLimitActionContinue:
					continue
				case rateLimitActionRetry:
					idx--
					continue
				}
				// rateLimitActionMaxExceeded - fall through to error
			}
			err = newCategorizedError(httpResp.StatusCode, string(bodyBytes), nil)
			return resp, err
		}

		reporter.publish(ctx, parseAntigravityUsage(bodyBytes))

		// Debug: Log raw response for Claude models (if needed for debugging)
		// Note: Detailed tracing confirms that Antigravity backend generates thinking tokens (usage count is high)
		// but currently does not return the thinking content in the response body for Claude models,
		// even with includeThoughts=true. This is a server-side limitation.
		// if strings.Contains(req.Model, "claude") {
		// log.Infof("antigravity executor [CLAUDE]: raw response body: %s", string(bodyBytes))
		// }

		// Translate response using canonical translator
		// Note: For Claude Thinking models on Antigravity, thinking content is ONLY returned
		// in Streaming mode (SSE). Non-stream responses currently strip thinking content.
		translatedResp, errTranslateResp := TranslateGeminiCLIResponseNonStream(e.cfg, from, bodyBytes, req.Model)
		if errTranslateResp != nil {
			return resp, fmt.Errorf("failed to translate response: %w", errTranslateResp)
		}
		if translatedResp != nil {
			resp = cliproxyexecutor.Response{Payload: translatedResp}
		} else {
			// Translator returned nil - pass through raw response
			resp = cliproxyexecutor.Response{Payload: bodyBytes}
		}
		reporter.ensurePublished(ctx)
		return resp, nil
	}

	switch {
	case lastStatus != 0:
		err = newCategorizedError(lastStatus, string(lastBody), nil)
	case lastErr != nil:
		err = lastErr
	default:
		err = newCategorizedError(http.StatusServiceUnavailable, "antigravity executor: no base url available", nil)
	}
	return resp, err
}

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

	// Translate request using canonical translator
	translated, errTranslate := TranslateToGeminiCLI(e.cfg, from, req.Model, bytes.Clone(req.Payload), true, req.Metadata)
	if errTranslate != nil {
		return nil, fmt.Errorf("failed to translate request: %w", errTranslate)
	}

	// Debug trace: log final request payload for Claude thinking models
	if debugThinking {
		logThinkingRequest(translated, req.Model)
	}

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)

	var lastStatus int
	var lastBody []byte
	var lastErr error
	retrier := &rateLimitRetrier{}

	for idx := 0; idx < len(baseURLs); idx++ {
		baseURL := baseURLs[idx]
		httpReq, errReq := e.buildRequest(ctx, auth, token, req.Model, translated, true, opts.Alt, baseURL)
		if errReq != nil {
			err = errReq
			return nil, err
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			lastStatus = 0
			lastBody = nil
			lastErr = errDo
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			err = errDo
			return nil, err
		}
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			bodyBytes, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("antigravity executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				lastStatus = 0
				lastBody = nil
				lastErr = errRead
				if idx+1 < len(baseURLs) {
					log.Debugf("antigravity executor: read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
					continue
				}
				err = errRead
				return nil, err
			}
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), bodyBytes...)
			lastErr = nil
			if httpResp.StatusCode == http.StatusTooManyRequests {
				hasNextBaseURL := idx+1 < len(baseURLs)
				if hasNextBaseURL {
					log.Debugf("antigravity executor: rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				}
				action, ctxErr := retrier.handleRateLimit(ctx, hasNextBaseURL, bodyBytes)
				if ctxErr != nil {
					err = ctxErr
					return nil, err
				}
				switch action {
				case rateLimitActionContinue:
					continue
				case rateLimitActionRetry:
					idx--
					continue
				}
				// rateLimitActionMaxExceeded - fall through to error
			} else if (httpResp.StatusCode == http.StatusNotFound || httpResp.StatusCode >= 500) && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: error %d on base url %s, retrying with fallback base url: %s", httpResp.StatusCode, baseURL, baseURLs[idx+1])
				continue
			}
			err = newCategorizedError(httpResp.StatusCode, string(bodyBytes), nil)
			return nil, err
		}

		out := make(chan cliproxyexecutor.StreamChunk)
		stream = out
		go func(resp *http.Response) {
			defer close(out)
			defer func() {
				if errClose := resp.Body.Close(); errClose != nil {
					log.Errorf("antigravity executor: close response body error: %v", errClose)
				}
			}()
			scanner := bufio.NewScanner(resp.Body)
			scanner.Buffer(make([]byte, 64*1024), DefaultStreamBufferSize)

			// Initialize streaming state with schema context from original request for tool call normalization
			streamState := NewAntigravityStreamState(opts.OriginalRequest)
			messageID := "chatcmpl-" + req.Model

			for scanner.Scan() {
				// Check context cancellation before processing each line
				select {
				case <-ctx.Done():
					return
				default:
				}

				line := scanner.Bytes()

				// Debug trace: log raw SSE for Claude thinking models
				if debugThinking {
					logThinkingRawSSE(line, req.Model)
				}

				// Filter usage metadata for all models
				// Only retain usage statistics in the terminal chunk
				filteredLine := FilterSSEUsageMetadata(line)

				// Extract JSON payload from SSE line (strips "data: " prefix)
				payload := jsonPayload(filteredLine)
				if payload == nil {
					continue // Skip non-JSON lines (empty, comments, etc.)
				}

				// Debug trace: log parsed payload for Claude thinking models
				if debugThinking {
					logThinkingPayload(payload, req.Model)
				}

				// Validate JSON to handle malformed SSE data gracefully
				if !gjson.ValidBytes(payload) {
					log.Debugf("antigravity executor: skipping malformed SSE payload")
					continue
				}

				if detail, ok := parseAntigravityStreamUsage(payload); ok {
					reporter.publish(ctx, detail)
				}

				// Translate stream chunk using canonical translator
				// Pass JSON payload (not raw SSE line) for proper parsing
				translatedChunks, errTranslateChunk := TranslateGeminiCLIResponseStream(e.cfg, from, payload, req.Model, messageID, streamState)
				if errTranslateChunk != nil {
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: fmt.Errorf("failed to translate chunk: %w", errTranslateChunk)}:
					case <-ctx.Done():
						return
					}
					continue
				}
				for _, chunk := range translatedChunks {
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
			} else {
				reporter.ensurePublished(ctx)
			}
		}(httpResp)
		return stream, nil
	}

	switch {
	case lastStatus != 0:
		err = newCategorizedError(lastStatus, string(lastBody), nil)
	case lastErr != nil:
		err = lastErr
	default:
		err = newCategorizedError(http.StatusServiceUnavailable, "antigravity executor: no base url available", nil)
	}
	return nil, err
}

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
	return cliproxyexecutor.Response{}, newCategorizedError(http.StatusNotImplemented, "count tokens not supported", nil)
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

	baseURLs := antigravityBaseURLFallbackOrder(auth)
	httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

	for idx, baseURL := range baseURLs {
		modelsURL := baseURL + antigravityModelsPath
		httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, modelsURL, bytes.NewReader([]byte(`{}`)))
		if errReq != nil {
			return nil
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+token)
		httpReq.Header.Set("User-Agent", resolveUserAgent(auth))
		if host := resolveHost(baseURL); host != "" {
			httpReq.Host = host
		}

		httpResp, errDo := httpClient.Do(httpReq)
		if errDo != nil {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return nil
		}

		bodyBytes, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("antigravity executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			if idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models read error on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return nil
		}
		if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
			if httpResp.StatusCode == http.StatusTooManyRequests && idx+1 < len(baseURLs) {
				log.Debugf("antigravity executor: models request rate limited on base url %s, retrying with fallback base url: %s", baseURL, baseURLs[idx+1])
				continue
			}
			return nil
		}

		result := gjson.GetBytes(bodyBytes, "models")
		if !result.Exists() {
			return nil
		}

		now := time.Now().Unix()
		models := make([]*registry.ModelInfo, 0, len(result.Map()))

		// Build a lookup map from static Gemini model definitions to inherit
		// Thinking support and other metadata. Antigravity uses Google Cloud Code API
		// which serves the same Gemini models, so we reuse GetGeminiCLIModels() definitions.
		staticModels := registry.GetGeminiCLIModels()
		staticModelMap := make(map[string]*registry.ModelInfo, len(staticModels))
		for _, m := range staticModels {
			if m != nil {
				staticModelMap[m.ID] = m
			}
		}

		for originalName := range result.Map() {
			aliasName := modelName2Alias(originalName)
			if aliasName == "" {
				continue
			}

			modelInfo := &registry.ModelInfo{
				ID:          aliasName,
				Name:        aliasName,
				Description: aliasName,
				DisplayName: aliasName,
				Version:     aliasName,
				Object:      "model",
				Created:     now,
				OwnedBy:     antigravityAuthType,
				Type:        antigravityAuthType,
			}

			// Set CanonicalID for Claude models to support both prefixed and non-prefixed names
			// E.g., "gemini-claude-opus-4-5-thinking" canonical to "claude-opus-4-5-thinking"
			if strings.HasPrefix(aliasName, "gemini-claude-") {
				canonicalName := strings.TrimPrefix(aliasName, "gemini-")
				modelInfo.CanonicalID = canonicalName
			}

			// Inherit metadata from static model definitions if available
			if staticModel, ok := staticModelMap[aliasName]; ok {
				modelInfo.Description = staticModel.Description
				modelInfo.DisplayName = staticModel.DisplayName
				modelInfo.Version = staticModel.Version
				modelInfo.InputTokenLimit = staticModel.InputTokenLimit
				modelInfo.OutputTokenLimit = staticModel.OutputTokenLimit
				modelInfo.SupportedGenerationMethods = staticModel.SupportedGenerationMethods
				modelInfo.Thinking = staticModel.Thinking
			}

			models = append(models, modelInfo)
		}
		return models
	}
	return nil
}

// tokenRefreshResult holds the result of a token refresh operation for singleflight.
type tokenRefreshResult struct {
	token string
	auth  *cliproxyauth.Auth
}

func (e *AntigravityExecutor) ensureAccessToken(ctx context.Context, auth *cliproxyauth.Auth) (string, *cliproxyauth.Auth, error) {
	if auth == nil {
		return "", nil, newCategorizedError(http.StatusUnauthorized, "missing auth", nil)
	}

	// Fast path: token still valid
	accessToken := metaStringValue(auth.Metadata, "access_token")
	expiry := tokenExpiry(auth.Metadata)
	if accessToken != "" && expiry.After(time.Now().Add(refreshSkew)) {
		return accessToken, nil, nil
	}

	// Use singleflight to prevent concurrent refresh stampede for same auth
	result, err, _ := e.sfGroup.Do(auth.ID, func() (interface{}, error) {
		// Double-check inside singleflight - another goroutine may have just refreshed
		accessToken := metaStringValue(auth.Metadata, "access_token")
		expiry := tokenExpiry(auth.Metadata)
		if accessToken != "" && expiry.After(time.Now().Add(refreshSkew)) {
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
		return nil, newCategorizedError(http.StatusUnauthorized, "missing auth", nil)
	}
	refreshToken := metaStringValue(auth.Metadata, "refresh_token")
	if refreshToken == "" {
		return auth, newCategorizedError(http.StatusUnauthorized, "missing refresh token", nil)
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
	httpReq.Header.Set("User-Agent", defaultAntigravityAgent)
	httpReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
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
		// Use centralized error categorization
		return auth, newCategorizedError(httpResp.StatusCode, string(bodyBytes), nil)
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
		return auth, newCategorizedError(http.StatusUnauthorized,
			"invalid token response: missing access_token", nil)
	}
	if tokenResp.ExpiresIn < 0 {
		return auth, newCategorizedError(http.StatusUnauthorized,
			"invalid token response: negative expires_in", nil)
	}
	if tokenResp.ExpiresIn == 0 {
		// Default to 1 hour for Google OAuth if not specified
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

func (e *AntigravityExecutor) buildRequest(ctx context.Context, auth *cliproxyauth.Auth, token, modelName string, payload []byte, stream bool, alt, baseURL string) (*http.Request, error) {
	if token == "" {
		return nil, newCategorizedError(http.StatusUnauthorized, "missing access token", nil)
	}

	base := strings.TrimSuffix(baseURL, "/")
	if base == "" {
		base = buildBaseURL(auth)
	}
	path := antigravityGeneratePath
	if stream {
		path = antigravityStreamPath
	}
	var requestURL strings.Builder
	requestURL.WriteString(base)
	requestURL.WriteString(path)
	if stream {
		if alt != "" {
			requestURL.WriteString("?$alt=")
			requestURL.WriteString(url.QueryEscape(alt))
		} else {
			requestURL.WriteString("?alt=sse")
		}
	} else if alt != "" {
		requestURL.WriteString("?$alt=")
		requestURL.WriteString(url.QueryEscape(alt))
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

		paths = make([]string, 0)
		util.Walk(gjson.Parse(strJSON), "", "anyOf", &paths)
		for _, p := range paths {
			anyOf := gjson.Get(strJSON, p)
			if anyOf.IsArray() {
				anyOfItems := anyOf.Array()
				if len(anyOfItems) > 0 {
					strJSON, _ = sjson.SetRaw(strJSON, p[:len(p)-len(".anyOf")], anyOfItems[0].Raw)
				}
			}
		}

		payload = []byte(strJSON)
	}

	// Debug: Log request payload for Claude models
	// if strings.Contains(modelName, "claude") {
	// 	log.Infof("antigravity executor [CLAUDE REQUEST]: payload (first 2000 chars): %s", truncateString(string(payload), 2000))
	// }

	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(payload))
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
	return antigravityBaseURLDaily
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
	return defaultAntigravityAgent
}

func antigravityBaseURLFallbackOrder(auth *cliproxyauth.Auth) []string {
	if base := resolveCustomAntigravityBaseURL(auth); base != "" {
		return []string{base}
	}
	// Daily endpoint first (matches original CLIProxyAPI behavior)
	// Production endpoint as fallback
	return []string{
		antigravityBaseURLDaily,
		antigravityBaseURLProd,
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

func geminiToAntigravity(modelName string, payload []byte, projectID string) []byte {
	template, _ := sjson.Set(string(payload), "model", modelName)
	template, _ = sjson.Set(template, "userAgent", "antigravity")

	// Use real project ID from auth if available, otherwise generate random (legacy fallback)
	if projectID != "" {
		template, _ = sjson.Set(template, "project", projectID)
	} else {
		template, _ = sjson.Set(template, "project", generateProjectID())
	}
	template, _ = sjson.Set(template, "requestId", generateRequestID())
	template, _ = sjson.Set(template, "request.sessionId", generateSessionID())

	template, _ = sjson.Delete(template, "request.safetySettings")
	template, _ = sjson.Set(template, "request.toolConfig.functionCallingConfig.mode", "VALIDATED")

	// Handle thinkingConfig and maxOutputTokens for different model types

	if strings.Contains(modelName, "claude") {
		// Schema cleanup for Vertex AI Claude API (OpenAPI 3.0 subset)
		// Vertex AI Claude supports: type, properties, description, required, anyOf
		// Does NOT support: $schema, $ref, $defs, exclusiveMin/Max
		// Ignores (but accepts): minItems, maxItems, minLength, maxLength

		gjson.Get(template, "request.tools").ForEach(func(key, tool gjson.Result) bool {
			tool.Get("functionDeclarations").ForEach(func(funKey, funcDecl gjson.Result) bool {
				if funcDecl.Get("parametersJsonSchema").Exists() {
					template, _ = sjson.SetRaw(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parameters", key.Int(), funKey.Int()), funcDecl.Get("parametersJsonSchema").Raw)
					template, _ = sjson.Delete(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parameters.$schema", key.Int(), funKey.Int()))
					template, _ = sjson.Delete(template, fmt.Sprintf("request.tools.%d.functionDeclarations.%d.parametersJsonSchema", key.Int(), funKey.Int()))
				}
				return true
			})
			return true
		})

		strJSON := string(template)
		// Remove unsupported JSON Schema fields (based on Vertex AI OpenAPI 3.0 subset)
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.$schema")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.$ref")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.$defs")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.exclusiveMinimum")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.exclusiveMaximum")

		// Optional: Remove validation constraints (ignored by Vertex AI but safe to keep)
		// Removing them keeps schema minimal and avoids potential future issues
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.minItems")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.maxItems")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.minLength")
		strJSON = util.DeleteKey(strJSON, "request.tools.#.functionDeclarations.#.parameters.maxLength")

		// NOTE: anyOf is SUPPORTED by Vertex AI Claude - do NOT remove it
		template = strJSON

	} else {
		// template, _ = sjson.Delete(template, "request.generationConfig.maxOutputTokens")
	}

	return []byte(template)
}

func generateRequestID() string {
	return "agent-" + uuid.NewString()
}

func generateSessionID() string {
	// Use uuid for thread-safe random generation instead of math/rand
	// Format: negative number string (mimics original behavior)
	uuidStr := uuid.NewString()
	// Convert first 16 hex chars to int64-like string
	return "-" + uuidStr[:8] + uuidStr[9:13] + uuidStr[14:18]
}

// projectIDAdjectives and projectIDNouns are used for generating random project IDs (legacy fallback).
var (
	projectIDAdjectives = []string{"useful", "bright", "swift", "calm", "bold"}
	projectIDNouns      = []string{"fuze", "wave", "spark", "flow", "core"}
)

func generateProjectID() string {
	// Use uuid bytes for thread-safe random selection
	uuidBytes := []byte(uuid.NewString())
	adj := projectIDAdjectives[int(uuidBytes[0])%len(projectIDAdjectives)]
	noun := projectIDNouns[int(uuidBytes[1])%len(projectIDNouns)]
	randomPart := strings.ToLower(uuid.NewString())[:5]
	return adj + "-" + noun + "-" + randomPart
}

func modelName2Alias(modelName string) string {
	switch modelName {
	case "rev19-uic3-1p":
		return "gemini-2.5-computer-use-preview-10-2025"
	case "gemini-3-pro-image":
		return "gemini-3-pro-image-preview"
	case "gemini-3-pro-high":
		return "gemini-3-pro-preview"
	case "claude-sonnet-4-5":
		return "gemini-claude-sonnet-4-5"
	case "claude-sonnet-4-5-thinking":
		return "gemini-claude-sonnet-4-5-thinking"
	case "claude-opus-4-5-thinking":
		return "gemini-claude-opus-4-5-thinking"
	case "chat_20706", "chat_23310", "gemini-2.5-flash-thinking", "gemini-3-pro-low", "gemini-2.5-pro":
		return ""
	default:
		return modelName
	}
}

func alias2ModelName(modelName string) string {
	switch modelName {
	case "gemini-2.5-computer-use-preview-10-2025":
		return "rev19-uic3-1p"
	case "gemini-3-pro-image-preview":
		return "gemini-3-pro-image"
	case "gemini-3-pro-preview":
		return "gemini-3-pro-high"
	case "gemini-claude-sonnet-4-5", "claude-sonnet-4-5":
		return "claude-sonnet-4-5"
	case "gemini-claude-sonnet-4-5-thinking", "claude-sonnet-4-5-thinking":
		return "claude-sonnet-4-5-thinking"
	case "gemini-claude-opus-4-5-thinking", "claude-opus-4-5-thinking":
		return "claude-opus-4-5-thinking"
	default:
		return modelName
	}
}
