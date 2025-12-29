// Package executor contains provider executors. This file implements the Vertex AI
// Gemini executor that talks to Google Vertex AI endpoints using service account
// credentials imported by the CLI.
//
// GeminiVertexExecutor handles requests to Google Vertex AI Gemini endpoints.
//
// Authentication (Strategy Pattern):
//
//   - Service Account: via auth.Metadata["service_account"] with OAuth2 token exchange
//   - API Key: via auth.Attributes["api_key"] with x-goog-api-key header
//
// The executor uses VertexAuthStrategy interface to abstract authentication:
//
//   - serviceAccountStrategy: For project-based Vertex AI access
//   - apiKeyStrategy: For AI Studio / Generative Language API access
//
// Supported models: gemini-* models via Vertex AI
//
// Features:
//   - Streaming via SSE (streamGenerateContent)
//   - Token counting via countTokens endpoint
//   - Format translation (OpenAI/Claude -> Gemini)
//   - Thinking content transformation for reasoning models
package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"strings"

	vertexauth "github.com/nghyane/llm-mux/internal/auth/vertex"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
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
	// vertexAPIVersion aligns with current public Vertex Generative AI API.
	vertexAPIVersion = "v1"
)

// =============================================================================
// VertexAuthStrategy Interface and Implementations (Strategy Pattern)
// =============================================================================
//
// The Strategy pattern is used here to abstract authentication differences between:
//   - Service Account authentication (OAuth2 token exchange, Vertex AI endpoints)
//   - API Key authentication (static key header, Generative Language API endpoints)
//
// This allows the executor to support both authentication methods without
// conditional logic scattered throughout the code.

// VertexAuthStrategy defines the authentication strategy for Vertex AI requests.
// Implementations provide different authentication mechanisms for accessing
// Google AI endpoints.
type VertexAuthStrategy interface {
	// GetToken returns the authentication token (API key or access token).
	GetToken(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (string, error)
	// BuildURL builds the request URL for the given action.
	BuildURL(model, action string, opts cliproxyexecutor.Options) string
	// ApplyAuth applies authentication headers to the request.
	ApplyAuth(req *http.Request, token string)
}

// serviceAccountStrategy implements VertexAuthStrategy using service account credentials.
type serviceAccountStrategy struct {
	projectID string
	location  string
	saJSON    []byte
}

func (s *serviceAccountStrategy) GetToken(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (string, error) {
	return vertexAccessToken(ctx, cfg, auth, s.saJSON)
}

func (s *serviceAccountStrategy) BuildURL(model, action string, opts cliproxyexecutor.Options) string {
	baseURL := vertexBaseURL(s.location)
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(150) // vertex URLs are longer
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(vertexAPIVersion)
	ub.WriteString("/projects/")
	ub.WriteString(s.projectID)
	ub.WriteString("/locations/")
	ub.WriteString(s.location)
	ub.WriteString("/publishers/google/models/")
	ub.WriteString(model)
	ub.WriteString(":")
	ub.WriteString(action)
	return ub.String()
}

func (s *serviceAccountStrategy) ApplyAuth(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// apiKeyStrategy implements VertexAuthStrategy using API key credentials.
type apiKeyStrategy struct {
	apiKey  string
	baseURL string
}

func (s *apiKeyStrategy) GetToken(_ context.Context, _ *config.Config, _ *cliproxyauth.Auth) (string, error) {
	return s.apiKey, nil
}

func (s *apiKeyStrategy) BuildURL(model, action string, _ cliproxyexecutor.Options) string {
	baseURL := s.baseURL
	if baseURL == "" {
		baseURL = "https://generativelanguage.googleapis.com"
	}
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(150) // vertex URLs are longer
	ub.WriteString(baseURL)
	ub.WriteString("/")
	ub.WriteString(vertexAPIVersion)
	ub.WriteString("/publishers/google/models/")
	ub.WriteString(model)
	ub.WriteString(":")
	ub.WriteString(action)
	return ub.String()
}

func (s *apiKeyStrategy) ApplyAuth(req *http.Request, token string) {
	if token != "" {
		req.Header.Set("x-goog-api-key", token)
	}
}

// =============================================================================
// GeminiVertexExecutor
// =============================================================================

// GeminiVertexExecutor sends requests to Vertex AI Gemini endpoints.
//
// It supports two authentication modes via the Strategy pattern:
//   - Service Account: Uses OAuth2 token exchange for project-scoped access
//   - API Key: Uses static API key for AI Studio access
//
// The executor translates requests from OpenAI/Claude format to Gemini format,
// handles streaming via SSE, and provides token counting capabilities.
type GeminiVertexExecutor struct {
	cfg *config.Config
}

// NewGeminiVertexExecutor constructs the Vertex executor.
func NewGeminiVertexExecutor(cfg *config.Config) *GeminiVertexExecutor {
	return &GeminiVertexExecutor{cfg: cfg}
}

// Identifier returns provider key for manager routing.
func (e *GeminiVertexExecutor) Identifier() string { return "vertex" }

// PrepareRequest is a no-op for Vertex.
func (e *GeminiVertexExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

// resolveStrategy determines the appropriate auth strategy based on auth credentials.
func (e *GeminiVertexExecutor) resolveStrategy(auth *cliproxyauth.Auth) (VertexAuthStrategy, error) {
	// Try API key authentication first
	apiKey, baseURL := vertexAPICreds(auth)
	if apiKey != "" {
		return &apiKeyStrategy{apiKey: apiKey, baseURL: baseURL}, nil
	}

	// Fall back to service account authentication
	projectID, location, saJSON, err := vertexCreds(auth)
	if err != nil {
		return nil, err
	}
	return &serviceAccountStrategy{projectID: projectID, location: location, saJSON: saJSON}, nil
}

// =============================================================================
// Execute (Non-Streaming)
// =============================================================================

// Execute handles non-streaming requests.
func (e *GeminiVertexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	strategy, err := e.resolveStrategy(auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.executeWithStrategy(ctx, auth, req, opts, strategy)
}

func (e *GeminiVertexExecutor) executeWithStrategy(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, strategy VertexAuthStrategy) (resp cliproxyexecutor.Response, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := TranslateToGemini(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return resp, err
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}

	url := strategy.BuildURL(req.Model, action, opts)
	if opts.Alt != "" && action != "countTokens" {
		url = url + "?$alt=" + opts.Alt
	}
	// Delete session_id for API key auth (apiKeyStrategy)
	if _, ok := strategy.(*apiKeyStrategy); ok {
		body, _ = sjson.DeleteBytes(body, "session_id")
	}

	httpReq, errNewReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNewReq != nil {
		return resp, errNewReq
	}
	httpReq.Header.Set("Content-Type", "application/json")

	token, errTok := strategy.GetToken(ctx, e.cfg, auth)
	if errTok != nil {
		log.Errorf("vertex executor: access token error: %v", errTok)
		return resp, NewStatusError(500, "internal server error", nil)
	}
	strategy.ApplyAuth(httpReq, token)
	applyGeminiHeaders(httpReq, auth)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		if errors.Is(errDo, context.DeadlineExceeded) {
			return resp, NewTimeoutError("request timed out")
		}
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("vertex executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "gemini-vertex executor")
		return resp, result.Error
	}
	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return resp, errRead
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

// =============================================================================
// ExecuteStream (Streaming)
// =============================================================================

// ExecuteStream handles SSE streaming for Vertex.
func (e *GeminiVertexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	strategy, err := e.resolveStrategy(auth)
	if err != nil {
		return nil, err
	}
	return e.executeStreamWithStrategy(ctx, auth, req, opts, strategy)
}

func (e *GeminiVertexExecutor) executeStreamWithStrategy(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, strategy VertexAuthStrategy) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	translation, err := TranslateToGeminiWithTokens(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if err != nil {
		return nil, err
	}
	body := translation.Payload
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)

	url := strategy.BuildURL(req.Model, "streamGenerateContent", opts)
	if opts.Alt == "" {
		url = url + "?alt=sse"
	} else {
		url = url + "?$alt=" + opts.Alt
	}
	body, _ = sjson.DeleteBytes(body, "session_id")

	httpReq, errNewReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errNewReq != nil {
		return nil, errNewReq
	}
	httpReq.Header.Set("Content-Type", "application/json")

	token, errTok := strategy.GetToken(ctx, e.cfg, auth)
	if errTok != nil {
		log.Errorf("vertex executor: access token error: %v", errTok)
		return nil, NewStatusError(500, "internal server error", nil)
	}
	strategy.ApplyAuth(httpReq, token)
	applyGeminiHeaders(httpReq, auth)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		if errors.Is(errDo, context.DeadlineExceeded) {
			return nil, NewTimeoutError("request timed out")
		}
		return nil, errDo
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "gemini-vertex executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	streamCtx := NewStreamContext()
	streamCtx.EstimatedInputTokens = translation.EstimatedInputTokens
	translator := NewStreamTranslator(e.cfg, from, from.String(), req.Model, "chatcmpl-"+req.Model, streamCtx)
	processor := &vertexStreamProcessor{
		translator: translator,
	}

	return RunSSEStream(ctx, httpResp.Body, reporter, processor, StreamConfig{
		ExecutorName:     "vertex executor",
		HandleDoneSignal: true,
	}), nil
}

// =============================================================================
// CountTokens
// =============================================================================

// CountTokens calls Vertex countTokens endpoint.
func (e *GeminiVertexExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	strategy, err := e.resolveStrategy(auth)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return e.countTokensWithStrategy(ctx, auth, req, opts, strategy)
}

func (e *GeminiVertexExecutor) countTokensWithStrategy(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, strategy VertexAuthStrategy) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	translatedReq, err := TranslateToGemini(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	translatedReq = util.StripThinkingConfigIfUnsupported(req.Model, translatedReq)
	respCtx := context.WithValue(ctx, altContextKey{}, opts.Alt)
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "tools")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "generationConfig")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "safetySettings")

	url := strategy.BuildURL(req.Model, "countTokens", opts)

	httpReq, errNewReq := http.NewRequestWithContext(respCtx, http.MethodPost, url, bytes.NewReader(translatedReq))
	if errNewReq != nil {
		return cliproxyexecutor.Response{}, errNewReq
	}
	httpReq.Header.Set("Content-Type", "application/json")

	token, errTok := strategy.GetToken(ctx, e.cfg, auth)
	if errTok != nil {
		log.Errorf("vertex executor: access token error: %v", errTok)
		return cliproxyexecutor.Response{}, NewStatusError(500, "internal server error", nil)
	}
	strategy.ApplyAuth(httpReq, token)
	applyGeminiHeaders(httpReq, auth)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		if errors.Is(errDo, context.DeadlineExceeded) {
			return cliproxyexecutor.Response{}, NewTimeoutError("request timed out")
		}
		return cliproxyexecutor.Response{}, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("vertex executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "gemini-vertex executor")
		return cliproxyexecutor.Response{}, result.Error
	}
	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		return cliproxyexecutor.Response{}, errRead
	}
	count := gjson.GetBytes(data, "totalTokens").Int()
	to := formatGemini
	out := sdktranslator.TranslateTokenCount(ctx, to, from, count, data)
	return cliproxyexecutor.Response{Payload: []byte(out)}, nil
}

// Refresh is a no-op for service account based credentials.
func (e *GeminiVertexExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// =============================================================================
// Stream Processor for Vertex
// =============================================================================

// vertexStreamProcessor implements StreamProcessor for Vertex AI streams.
type vertexStreamProcessor struct {
	translator *StreamTranslator
}

// ProcessLine implements StreamProcessor.ProcessLine.
func (p *vertexStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	// Parse Gemini chunk to IR events
	var events []ir.UnifiedEvent
	var err error
	if p.translator.ctx.ToolSchemaCtx != nil {
		events, err = to_ir.ParseGeminiChunkWithContext(line, p.translator.ctx.ToolSchemaCtx)
	} else {
		events, err = to_ir.ParseGeminiChunk(line)
	}
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

// ProcessDone implements StreamProcessor.ProcessDone.
func (p *vertexStreamProcessor) ProcessDone() ([][]byte, error) {
	return p.translator.Flush(), nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// vertexCreds extracts project, location and raw service account JSON from auth metadata.
func vertexCreds(a *cliproxyauth.Auth) (projectID, location string, serviceAccountJSON []byte, err error) {
	if a == nil || a.Metadata == nil {
		return "", "", nil, fmt.Errorf("vertex executor: missing auth metadata")
	}
	if v, ok := a.Metadata["project_id"].(string); ok {
		projectID = strings.TrimSpace(v)
	}
	if projectID == "" {
		// Some service accounts may use "project"; still prefer standard field
		if v, ok := a.Metadata["project"].(string); ok {
			projectID = strings.TrimSpace(v)
		}
	}
	if projectID == "" {
		return "", "", nil, fmt.Errorf("vertex executor: missing project_id in credentials")
	}
	if v, ok := a.Metadata["location"].(string); ok && strings.TrimSpace(v) != "" {
		location = strings.TrimSpace(v)
	} else {
		location = "us-central1"
	}
	var sa map[string]any
	if raw, ok := a.Metadata["service_account"].(map[string]any); ok {
		sa = raw
	}
	if sa == nil {
		return "", "", nil, fmt.Errorf("vertex executor: missing service_account in credentials")
	}
	normalized, errNorm := vertexauth.NormalizeServiceAccountMap(sa)
	if errNorm != nil {
		return "", "", nil, fmt.Errorf("vertex executor: %w", errNorm)
	}
	saJSON, errMarshal := json.Marshal(normalized)
	if errMarshal != nil {
		return "", "", nil, fmt.Errorf("vertex executor: marshal service_account failed: %w", errMarshal)
	}
	return projectID, location, saJSON, nil
}

// vertexAPICreds extracts API key and base URL from auth attributes following the claudeCreds pattern.
func vertexAPICreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func vertexBaseURL(location string) string {
	loc := strings.TrimSpace(location)
	if loc == "" {
		loc = "us-central1"
	}
	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(64)
	ub.WriteString("https://")
	ub.WriteString(loc)
	ub.WriteString("-aiplatform.googleapis.com")
	return ub.String()
}

func vertexAccessToken(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, saJSON []byte) (string, error) {
	if httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0); httpClient != nil {
		ctx = context.WithValue(ctx, oauth2.HTTPClient, httpClient)
	}
	// Use cloud-platform scope for Vertex AI.
	creds, errCreds := google.CredentialsFromJSON(ctx, saJSON, "https://www.googleapis.com/auth/cloud-platform")
	if errCreds != nil {
		return "", fmt.Errorf("vertex executor: parse service account json failed: %w", errCreds)
	}
	tok, errTok := creds.TokenSource.Token()
	if errTok != nil {
		return "", fmt.Errorf("vertex executor: get access token failed: %w", errTok)
	}
	return tok.AccessToken, nil
}

// =============================================================================
// Dynamic Model Fetching
// =============================================================================

// FetchVertexModels retrieves available models from Vertex AI.
// Supports both API key and service account authentication.
func FetchVertexModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	exec := &GeminiVertexExecutor{cfg: cfg}
	strategy, err := exec.resolveStrategy(auth)
	if err != nil {
		log.Errorf("vertex: failed to resolve auth strategy: %v", err)
		return nil
	}

	// For API key mode, use GL API endpoint
	if apiStrategy, ok := strategy.(*apiKeyStrategy); ok {
		httpClient := newProxyAwareHTTPClient(ctx, cfg, auth, 0)

		fetchCfg := GLAPIFetchConfig{
			BaseURL:      apiStrategy.baseURL,
			APIKey:       apiStrategy.apiKey,
			ProviderType: "vertex",
		}

		return FetchGLAPIModels(ctx, httpClient, fetchCfg)
	}

	// For service account mode, use Vertex AI endpoint
	// Note: Vertex AI list models endpoint is different and more complex
	// For now, fall back to static models for SA mode
	return nil
}
