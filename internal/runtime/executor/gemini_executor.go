// Package executor provides runtime execution capabilities for various AI service providers.
// It includes stateless executors that handle API requests, streaming responses,
// token counting, and authentication refresh for different AI service providers.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/util"
	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	// glEndpoint is the base URL for the Google Generative Language API.
	glEndpoint = "https://generativelanguage.googleapis.com"

	// glAPIVersion is the API version used for Gemini requests.
	glAPIVersion = "v1beta"
)

// GeminiExecutor is a stateless executor for the official Gemini API using API keys.
// It handles both API key and OAuth bearer token authentication, supporting both
// regular and streaming requests to the Google Generative Language API.
type GeminiExecutor struct {
	// cfg holds the application configuration.
	cfg *config.Config
}

// NewGeminiExecutor creates a new Gemini executor instance.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *GeminiExecutor: A new Gemini executor instance
func NewGeminiExecutor(cfg *config.Config) *GeminiExecutor { return &GeminiExecutor{cfg: cfg} }

// Identifier returns the executor identifier for Gemini.
func (e *GeminiExecutor) Identifier() string { return "gemini" }

// PrepareRequest prepares the HTTP request for execution (no-op for Gemini).
func (e *GeminiExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

// Execute performs a non-streaming request to the Gemini API.
// It translates the request to Gemini format, sends it to the API, and translates
// the response back to the requested format.
//
// Parameters:
//   - ctx: The context for the request
//   - auth: The authentication information
//   - req: The request to execute
//   - opts: Additional execution options
//
// Returns:
//   - cliproxyexecutor.Response: The response from the API
//   - error: An error if the request fails
func (e *GeminiExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, bearer := geminiCreds(auth)

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	// Official Gemini API via API key or OAuth bearer
	from := opts.SourceFormat
	body, err := TranslateToGemini(e.cfg, from, req.Model, bytes.Clone(req.Payload), false, req.Metadata)
	if err != nil {
		return resp, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
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
	url := fmt.Sprintf("%s/%s/models/%s:%s", baseURL, glAPIVersion, req.Model, action)
	if opts.Alt != "" && action != "countTokens" {
		url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
	}

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
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}
	reporter.publish(ctx, extractUsageFromGeminiResponse(data))

	translatedResp, err := TranslateGeminiResponseNonStream(e.cfg, from, data, req.Model)
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

	// Translate request and count tokens in one operation (uses shared IR)
	translation, err := TranslateToGeminiWithTokens(e.cfg, from, req.Model, bytes.Clone(req.Payload), true, req.Metadata)
	if err != nil {
		return nil, fmt.Errorf("translate request: %w", err)
	}

	body := translation.Payload
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		body = util.ApplyGeminiThinkingConfig(body, budgetOverride, includeOverride)
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/models/%s:%s", baseURL, glAPIVersion, req.Model, "streamGenerateContent")
	if opts.Alt == "" {
		url = url + "?alt=sse"
	} else {
		url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
	}

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
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out

	// Use pre-calculated input tokens from translation
	estimatedInputTokens := translation.EstimatedInputTokens

	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 64*1024), DefaultStreamBufferSize)
		streamState := &GeminiCLIStreamState{
			ClaudeState: from_ir.NewClaudeStreamState(),
		}
		// Set pre-calculated input tokens for message_start
		streamState.ClaudeState.EstimatedInputTokens = estimatedInputTokens

		for scanner.Scan() {
			// Check context cancellation before processing each line
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

			messageID := "chatcmpl-" + req.Model
			result, err := TranslateGeminiResponseStreamWithUsage(e.cfg, from, bytes.Clone(payload), req.Model, messageID, streamState)
			if err != nil {
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: err}:
				case <-ctx.Done():
				}
				return
			}
			if result.Usage != nil {
				reporter.publish(ctx, result.Usage)
			}
			for _, chunk := range result.Chunks {
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
	translatedReq, err := TranslateToGemini(e.cfg, from, req.Model, bytes.Clone(req.Payload), false, req.Metadata)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("translate request: %w", err)
	}
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		translatedReq = util.ApplyGeminiThinkingConfig(translatedReq, budgetOverride, includeOverride)
	}
	translatedReq = util.StripThinkingConfigIfUnsupported(req.Model, translatedReq)
	respCtx := context.WithValue(ctx, altContextKey{}, opts.Alt)
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "tools")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "generationConfig")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "safetySettings")

	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/models/%s:%s", baseURL, glAPIVersion, req.Model, "countTokens")

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
		return cliproxyexecutor.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("request error, error status: %d, error body: %s", resp.StatusCode, summarizeErrorBody(resp.Header.Get("Content-Type"), data))
		return cliproxyexecutor.Response{}, statusErr{code: resp.StatusCode, msg: string(data)}
	}

	count := gjson.GetBytes(data, "totalTokens").Int()
	formatGemini := sdktranslator.FromString("gemini")
	translated := sdktranslator.TranslateTokenCount(respCtx, formatGemini, from, count, data)
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

func (e *GeminiExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("gemini executor: refresh called")
	// OAuth bearer token refresh for official Gemini API.
	if auth == nil {
		return nil, fmt.Errorf("gemini executor: auth is nil")
	}
	if auth.Metadata == nil {
		return auth, nil
	}
	// Token data is typically nested under "token" map in Gemini files.
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
		// Fallback to top-level keys if present
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
		// Nothing to do for API key or cookie based entries
		return auth, nil
	}

	// Prepare oauth2 config; default to Google endpoints
	endpoint := google.Endpoint
	if tokenURI != "" {
		endpoint.TokenURL = tokenURI
	}
	conf := &oauth2.Config{ClientID: clientID, ClientSecret: clientSecret, Endpoint: endpoint}

	// Ensure proxy-aware HTTP client for token refresh
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

	// Persist back to metadata; prefer nested token map if present
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

	// Also mirror top-level access_token for compatibility if previously present
	if _, ok := auth.Metadata["access_token"]; ok {
		auth.Metadata["access_token"] = newTok.AccessToken
	}
	return auth, nil
}

// geminiCreds extracts credentials for Gemini API.
// Returns (apiKey, bearer) for compatibility with existing Gemini executor logic.
// Delegates to the common ExtractCreds function with Gemini configuration.
func geminiCreds(a *cliproxyauth.Auth) (apiKey, bearer string) {
	token, _ := ExtractCreds(a, GeminiCredsConfig)
	// For Gemini, the extracted token becomes the bearer, and apiKey comes from attributes
	if a != nil && a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
	}
	bearer = token
	return
}

func resolveGeminiBaseURL(auth *cliproxyauth.Auth) string {
	base := glEndpoint
	if auth != nil && auth.Attributes != nil {
		if custom := strings.TrimSpace(auth.Attributes["base_url"]); custom != "" {
			base = strings.TrimRight(custom, "/")
		}
	}
	if base == "" {
		return glEndpoint
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
