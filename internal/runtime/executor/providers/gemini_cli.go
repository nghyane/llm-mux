package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/misc"
	"github.com/nghyane/llm-mux/internal/oauth"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/providers/cloudcode"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/runtime/geminicli"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/tidwall/sjson"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

const (
	codeAssistEndpoint = "https://cloudcode-pa.googleapis.com"
	codeAssistVersion  = "v1internal"
)

var geminiOauthScopes = []string{
	"https://www.googleapis.com/auth/cloud-platform",
	"https://www.googleapis.com/auth/userinfo.email",
	"https://www.googleapis.com/auth/userinfo.profile",
}

type GeminiCLIExecutor struct {
	executor.BaseExecutor
}

func NewGeminiCLIExecutor(cfg *config.Config) *GeminiCLIExecutor {
	return &GeminiCLIExecutor{BaseExecutor: executor.BaseExecutor{Cfg: cfg}}
}

func (e *GeminiCLIExecutor) Identifier() string { return "gemini-cli" }

func (e *GeminiCLIExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *GeminiCLIExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.Cfg, auth)
	if err != nil {
		return resp, err
	}
	reporter := e.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat

	var basePayload []byte
	if ir.IsClaudeModel(req.Model) {
		irReq, errIR := stream.ConvertRequestToIR(from, req.Model, req.Payload, req.Metadata)
		if errIR != nil {
			return resp, fmt.Errorf("failed to parse request: %w", errIR)
		}
		basePayload, err = from_ir.ToVertexClaudeRequest(irReq)
		if err != nil {
			return resp, fmt.Errorf("failed to translate request: %w", err)
		}
	} else {
		geminiPayload, errGemini := stream.TranslateToGemini(e.Cfg, from, req.Model, req.Payload, false, req.Metadata)
		if errGemini != nil {
			return resp, fmt.Errorf("failed to translate request: %w", errGemini)
		}
		basePayload = cloudcode.RequestEnvelope(geminiPayload)
	}

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}

	projectID := resolveGeminiProjectID(auth)
	models := []string{req.Model}

	httpClient := e.NewHTTPClient(ctx, auth, 0)

	var lastStatus int
	var lastBody []byte
	retrier := &executor.RateLimitRetrier{}

	for idx := 0; idx < len(models); idx++ {
		attemptModel := models[idx]
		payload := append([]byte(nil), basePayload...)
		if action == "countTokens" {
			payload = deleteJSONField(payload, "project")
			payload = deleteJSONField(payload, "model")
		} else {
			payload = setJSONField(payload, "project", projectID)
			payload = setJSONField(payload, "model", attemptModel)
		}

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return resp, wrapTokenError(errTok)
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		ub := executor.GetURLBuilder()
		defer ub.Release()
		ub.Grow(100)
		ub.WriteString(codeAssistEndpoint)
		ub.WriteString("/")
		ub.WriteString(codeAssistVersion)
		ub.WriteString(":")
		ub.WriteString(action)
		url := ub.String()
		if opts.Alt != "" && action != "countTokens" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			err = errReq
			return resp, err
		}
		executor.SetCommonHeaders(reqHTTP, "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "application/json")

		httpResp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			if errors.Is(errDo, context.DeadlineExceeded) {
				return resp, executor.NewTimeoutError("request timed out")
			}
			err = errDo
			return resp, err
		}

		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini cli executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			err = errRead
			return resp, err
		}
		if httpResp.StatusCode >= 200 && httpResp.StatusCode < 300 {
			reporter.Publish(ctx, executor.ExtractUsageFromGeminiResponse(data))

			// Unwrap envelope if present (Gemini CLI wraps response in {"response": ...})
			// This allows us to use the standard Gemini format translator.
			cleanData := cloudcode.ResponseUnwrap(data)

			translatedResp, err := stream.TranslateResponseNonStream(e.Cfg, provider.FormatGemini, from, cleanData, attemptModel)
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

		lastStatus = httpResp.StatusCode
		lastBody = append([]byte(nil), data...)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, executor.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		if httpResp.StatusCode == 429 {
			hasNextModel := idx+1 < len(models)
			if hasNextModel {
				log.Debugf("gemini cli executor: rate limited, retrying with next model: %s", models[idx+1])
			}
			action, ctxErr := retrier.HandleRateLimit(ctx, hasNextModel, data)
			if ctxErr != nil {
				err = ctxErr
				return resp, err
			}
			switch action {
			case executor.RateLimitActionContinue:
				continue
			case executor.RateLimitActionRetry:
				idx--
				continue
			}
		}

		err = newGeminiStatusErr(httpResp.StatusCode, data)
		return resp, err
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	err = newGeminiStatusErr(lastStatus, lastBody)
	return resp, err
}

func (e *GeminiCLIExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.Cfg, auth)
	if err != nil {
		return nil, err
	}
	reporter := e.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat

	var translation *stream.TranslationResult
	var estimatedInputTokens int64
	if ir.IsClaudeModel(req.Model) {
		irReq, errIR := stream.ConvertRequestToIR(from, req.Model, req.Payload, req.Metadata)
		if errIR != nil {
			return nil, fmt.Errorf("failed to parse request: %w", errIR)
		}
		claudePayload, errClaude := from_ir.ToVertexClaudeRequest(irReq)
		if errClaude != nil {
			return nil, fmt.Errorf("failed to translate request: %w", errClaude)
		}
		translation = &stream.TranslationResult{
			Payload:              claudePayload,
			IR:                   irReq,
			EstimatedInputTokens: util.CountGeminiTokensFromIR(irReq),
		}
		estimatedInputTokens = translation.EstimatedInputTokens
	} else {
		var errGemini error
		translation, errGemini = stream.TranslateToGeminiWithTokens(e.Cfg, from, req.Model, req.Payload, true, req.Metadata)
		if errGemini != nil {
			return nil, fmt.Errorf("failed to translate request: %w", errGemini)
		}
		translation.Payload = cloudcode.RequestEnvelope(translation.Payload)
		estimatedInputTokens = translation.EstimatedInputTokens
	}
	basePayload := translation.Payload

	projectID := resolveGeminiProjectID(auth)
	models := []string{req.Model}

	httpClient := e.NewHTTPClient(ctx, auth, 0)

	var lastStatus int
	var lastBody []byte
	retrier := &executor.RateLimitRetrier{}

	for idx := 0; idx < len(models); idx++ {
		attemptModel := models[idx]
		payload := append([]byte(nil), basePayload...)
		payload = setJSONField(payload, "project", projectID)
		payload = setJSONField(payload, "model", attemptModel)

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return nil, wrapTokenError(errTok)
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		ub := executor.GetURLBuilder()
		defer ub.Release()
		ub.Grow(100)
		ub.WriteString(codeAssistEndpoint)
		ub.WriteString("/")
		ub.WriteString(codeAssistVersion)
		ub.WriteString(":")
		ub.WriteString("streamGenerateContent")
		url := ub.String()
		if opts.Alt == "" {
			url = url + "?alt=sse"
		} else {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			err = errReq
			return nil, err
		}
		executor.SetCommonHeaders(reqHTTP, "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "text/event-stream")

		httpResp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			if errors.Is(errDo, context.DeadlineExceeded) {
				return nil, executor.NewTimeoutError("request timed out")
			}
			err = errDo
			return nil, err
		}
		if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
			data, errRead := io.ReadAll(httpResp.Body)
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini cli executor: close response body error: %v", errClose)
			}
			if errRead != nil {
				err = errRead
				return nil, err
			}
			lastStatus = httpResp.StatusCode
			lastBody = append([]byte(nil), data...)
			log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, executor.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
			if httpResp.StatusCode == 429 {
				hasNextModel := idx+1 < len(models)
				if hasNextModel {
					log.Debugf("gemini cli executor: rate limited, retrying with next model: %s", models[idx+1])
				}
				action, ctxErr := retrier.HandleRateLimit(ctx, hasNextModel, data)
				if ctxErr != nil {
					err = ctxErr
					return nil, err
				}
				switch action {
				case executor.RateLimitActionContinue:
					continue
				case executor.RateLimitActionRetry:
					idx--
					continue
				}
			}
			err = newGeminiStatusErr(httpResp.StatusCode, data)
			return nil, err
		}

		streamCtx := stream.NewStreamContext()
		streamCtx.EstimatedInputTokens = estimatedInputTokens
		messageID := "chatcmpl-" + attemptModel

		processor := stream.NewGeminiStreamProcessor(e.Cfg, from, attemptModel, messageID, streamCtx)

		// Use GeminiPreprocessor with UnwrapEnvelope for envelope-wrapped responses
		geminiPreprocessFn := stream.GeminiPreprocessor()
		preprocessor := func(line []byte) ([]byte, bool) {
			payload, skip := geminiPreprocessFn(line)
			if skip || payload == nil {
				return nil, true
			}
			return cloudcode.ResponseUnwrap(payload), false
		}

		streamChan = stream.RunSSEStream(ctx, httpResp.Body, reporter, processor, stream.StreamConfig{
			ExecutorName:    "gemini-cli",
			Preprocessor:    preprocessor,
			EnsurePublished: true,
		})
		return streamChan, nil
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	err = newGeminiStatusErr(lastStatus, lastBody)
	return nil, err
}

func (e *GeminiCLIExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	tokenSource, baseTokenData, err := prepareGeminiCLITokenSource(ctx, e.Cfg, auth)
	if err != nil {
		return provider.Response{}, err
	}

	from := opts.SourceFormat
	models := []string{req.Model}

	httpClient := e.NewHTTPClient(ctx, auth, 0)

	var lastStatus int
	var lastBody []byte
	retrier := &executor.RateLimitRetrier{}

	for idx := 0; idx < len(models); idx++ {
		attemptModel := models[idx]
		var payload []byte
		if ir.IsClaudeModel(attemptModel) {
			irReq, errIR := stream.ConvertRequestToIR(from, attemptModel, req.Payload, req.Metadata)
			if errIR != nil {
				return provider.Response{}, fmt.Errorf("failed to parse request: %w", errIR)
			}
			var errClaude error
			payload, errClaude = from_ir.ToVertexClaudeRequest(irReq)
			if errClaude != nil {
				return provider.Response{}, fmt.Errorf("failed to translate request: %w", errClaude)
			}
		} else {
			geminiPayload, errGemini := stream.TranslateToGemini(e.Cfg, from, attemptModel, req.Payload, false, req.Metadata)
			if errGemini != nil {
				return provider.Response{}, fmt.Errorf("failed to translate request: %w", errGemini)
			}
			payload = cloudcode.RequestEnvelope(geminiPayload)
		}

		payload = deleteJSONField(payload, "project")
		payload = deleteJSONField(payload, "model")
		payload = deleteJSONField(payload, "request.safetySettings")

		tok, errTok := tokenSource.Token()
		if errTok != nil {
			return provider.Response{}, wrapTokenError(errTok)
		}
		updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

		ub := executor.GetURLBuilder()
		defer ub.Release()
		ub.Grow(100)
		ub.WriteString(codeAssistEndpoint)
		ub.WriteString("/")
		ub.WriteString(codeAssistVersion)
		ub.WriteString(":")
		ub.WriteString("countTokens")
		url := ub.String()
		if opts.Alt != "" {
			url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
		}

		reqHTTP, errReq := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if errReq != nil {
			return provider.Response{}, errReq
		}
		executor.SetCommonHeaders(reqHTTP, "application/json")
		reqHTTP.Header.Set("Authorization", "Bearer "+tok.AccessToken)
		applyGeminiCLIHeaders(reqHTTP)
		reqHTTP.Header.Set("Accept", "application/json")

		resp, errDo := httpClient.Do(reqHTTP)
		if errDo != nil {
			if errors.Is(errDo, context.DeadlineExceeded) {
				return provider.Response{}, executor.NewTimeoutError("request timed out")
			}
			return provider.Response{}, errDo
		}
		data, errRead := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if errRead != nil {
			return provider.Response{}, errRead
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return provider.Response{Payload: data}, nil
		}
		lastStatus = resp.StatusCode
		lastBody = append([]byte(nil), data...)
		if resp.StatusCode == 429 {
			hasNextModel := idx+1 < len(models)
			if hasNextModel {
				log.Debugf("gemini cli executor: rate limited, retrying with next model")
			}
			action, ctxErr := retrier.HandleRateLimit(ctx, hasNextModel, data)
			if ctxErr != nil {
				return provider.Response{}, ctxErr
			}
			switch action {
			case executor.RateLimitActionContinue:
				continue
			case executor.RateLimitActionRetry:
				idx--
				continue
			}
		}
		break
	}

	if lastStatus == 0 {
		lastStatus = 429
	}
	return provider.Response{}, newGeminiStatusErr(lastStatus, lastBody)
}

func (e *GeminiCLIExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	_ = ctx
	return auth, nil
}

func prepareGeminiCLITokenSource(ctx context.Context, cfg *config.Config, auth *provider.Auth) (oauth2.TokenSource, map[string]any, error) {
	metadata := geminiOAuthMetadata(auth)
	if auth == nil || metadata == nil {
		return nil, nil, fmt.Errorf("gemini-cli auth metadata missing")
	}

	var base map[string]any
	if tokenRaw, ok := metadata["token"].(map[string]any); ok && tokenRaw != nil {
		base = executor.CloneMap(tokenRaw)
	} else {
		base = make(map[string]any)
	}

	var token oauth2.Token
	if len(base) > 0 {
		if raw, err := json.Marshal(base); err == nil {
			_ = json.Unmarshal(raw, &token)
		}
	}

	if token.AccessToken == "" {
		token.AccessToken = stringValue(metadata, "access_token")
	}
	if token.RefreshToken == "" {
		token.RefreshToken = stringValue(metadata, "refresh_token")
	}
	if token.TokenType == "" {
		token.TokenType = stringValue(metadata, "token_type")
	}
	if token.Expiry.IsZero() {
		if expiry := stringValue(metadata, "expiry"); expiry != "" {
			if ts, err := time.Parse(time.RFC3339, expiry); err == nil {
				token.Expiry = ts
			}
		}
	}

	conf := &oauth2.Config{
		ClientID:     oauth.GeminiClientID,
		ClientSecret: oauth.GeminiClientSecret,
		Scopes:       geminiOauthScopes,
		Endpoint:     google.Endpoint,
	}

	ctxToken := ctx
	if httpClient := executor.NewProxyAwareHTTPClient(ctx, cfg, auth, 0); httpClient != nil {
		ctxToken = context.WithValue(ctxToken, oauth2.HTTPClient, httpClient)
	}

	src := conf.TokenSource(ctxToken, &token)
	currentToken, err := src.Token()
	if err != nil {
		return nil, nil, wrapTokenError(err)
	}
	updateGeminiCLITokenMetadata(auth, base, currentToken)
	return oauth2.ReuseTokenSource(currentToken, src), base, nil
}

func updateGeminiCLITokenMetadata(auth *provider.Auth, base map[string]any, tok *oauth2.Token) {
	if auth == nil || tok == nil {
		return
	}
	merged := buildGeminiTokenMap(base, tok)
	fields := buildGeminiTokenFields(tok, merged)
	shared := geminicli.ResolveSharedCredential(auth.Runtime)
	if shared != nil {
		snapshot := shared.MergeMetadata(fields)
		if !geminicli.IsVirtual(auth.Runtime) {
			auth.Metadata = snapshot
		}
		return
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	for k, v := range fields {
		auth.Metadata[k] = v
	}
}

func buildGeminiTokenMap(base map[string]any, tok *oauth2.Token) map[string]any {
	merged := executor.CloneMap(base)
	if merged == nil {
		merged = make(map[string]any)
	}
	if raw, err := json.Marshal(tok); err == nil {
		var tokenMap map[string]any
		if err = json.Unmarshal(raw, &tokenMap); err == nil {
			for k, v := range tokenMap {
				merged[k] = v
			}
		}
	}
	return merged
}

func buildGeminiTokenFields(tok *oauth2.Token, merged map[string]any) map[string]any {
	fields := make(map[string]any, 5)
	if tok.AccessToken != "" {
		fields["access_token"] = tok.AccessToken
	}
	if tok.TokenType != "" {
		fields["token_type"] = tok.TokenType
	}
	if tok.RefreshToken != "" {
		fields["refresh_token"] = tok.RefreshToken
	}
	if !tok.Expiry.IsZero() {
		fields["expiry"] = tok.Expiry.Format(time.RFC3339)
	}
	if len(merged) > 0 {
		fields["token"] = executor.CloneMap(merged)
	}
	return fields
}

func resolveGeminiProjectID(auth *provider.Auth) string {
	if auth == nil {
		return ""
	}
	if geminicli.IsVirtual(auth.Runtime) {
		if virtual, ok := auth.Runtime.(*geminicli.VirtualCredential); ok && virtual != nil {
			return strings.TrimSpace(virtual.ProjectID)
		}
		if rd, ok := auth.Runtime.(*provider.AuthRuntimeData); ok && rd != nil {
			if virtual, ok := rd.ProviderData.(*geminicli.VirtualCredential); ok && virtual != nil {
				return strings.TrimSpace(virtual.ProjectID)
			}
		}
	}
	return strings.TrimSpace(stringValue(auth.Metadata, "project_id"))
}

func geminiOAuthMetadata(auth *provider.Auth) map[string]any {
	if auth == nil {
		return nil
	}
	if shared := geminicli.ResolveSharedCredential(auth.Runtime); shared != nil {
		if snapshot := shared.MetadataSnapshot(); len(snapshot) > 0 {
			return snapshot
		}
	}
	return auth.Metadata
}

func stringValue(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	if v, ok := m[key]; ok {
		switch typed := v.(type) {
		case string:
			return typed
		case fmt.Stringer:
			return typed.String()
		}
	}
	return ""
}

func applyGeminiCLIHeaders(r *http.Request) {
	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	misc.EnsureHeader(r.Header, ginHeaders, "User-Agent", "google-api-nodejs-client/9.15.1")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Goog-Api-Client", "gl-node/22.17.0")
	misc.EnsureHeader(r.Header, ginHeaders, "Client-Metadata", geminiCLIClientMetadata())
}

func geminiCLIClientMetadata() string {
	return "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
}

func setJSONField(body []byte, key, value string) []byte {
	if key == "" {
		return body
	}
	updated, err := sjson.SetBytes(body, key, value)
	if err != nil {
		return body
	}
	return updated
}

func deleteJSONField(body []byte, key string) []byte {
	if key == "" || len(body) == 0 {
		return body
	}
	updated, err := sjson.DeleteBytes(body, key)
	if err != nil {
		return body
	}
	return updated
}

func newGeminiStatusErr(statusCode int, body []byte) error {
	var retryAfter *time.Duration
	if statusCode == http.StatusTooManyRequests {
		if parsed, parseErr := executor.ParseRetryDelay(body); parseErr == nil && parsed != nil {
			retryAfter = parsed
		}
	}
	return executor.NewStatusError(statusCode, string(body), retryAfter)
}

func wrapTokenError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	return executor.NewStatusError(http.StatusUnauthorized, msg, nil)
}

func FetchGeminiCLIModels(ctx context.Context, auth *provider.Auth, cfg *config.Config) []*registry.ModelInfo {
	tokenSource, _, err := prepareGeminiCLITokenSource(ctx, cfg, auth)
	if err != nil {
		log.Errorf("gemini-cli: failed to prepare token source: %v", err)
		return nil
	}

	tok, err := tokenSource.Token()
	if err != nil {
		log.Errorf("gemini-cli: failed to get token: %v", err)
		return nil
	}

	httpClient := executor.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)

	fetchCfg := CloudCodeFetchConfig{
		BaseURLs:     []string{codeAssistEndpoint},
		Token:        tok.AccessToken,
		ProviderType: "gemini-cli",
		AliasFunc:    func(name string) string { return registry.GeminiUpstreamToID(name, nil) },
	}

	return FetchCloudCodeModels(ctx, httpClient, fetchCfg)
}
