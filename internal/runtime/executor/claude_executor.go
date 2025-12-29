package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/auth/claude"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/misc"
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

	"github.com/gin-gonic/gin"
)

type ClaudeExecutor struct {
	cfg *config.Config
}

type claudeStreamProcessor struct {
	translator *StreamTranslator
}

func (p *claudeStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	var parserState *ir.ClaudeStreamParserState
	if p.translator.ctx.ClaudeState != nil {
		parserState = p.translator.ctx.ClaudeState.ParserState
	}
	events, err := to_ir.ParseClaudeChunkWithState(line, parserState)
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

func (p *claudeStreamProcessor) ProcessDone() ([][]byte, error) {
	return p.translator.Flush(), nil
}

type claudePassthroughProcessor struct{}

func (p *claudePassthroughProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	events, err := to_ir.ParseClaudeChunk(line)
	if err != nil {
		return nil, nil, nil
	}
	usage := extractUsageFromEvents(events)
	return nil, usage, nil
}

func (p *claudePassthroughProcessor) ProcessDone() ([][]byte, error) {
	return nil, nil
}

func NewClaudeExecutor(cfg *config.Config) *ClaudeExecutor { return &ClaudeExecutor{cfg: cfg} }

func (e *ClaudeExecutor) Identifier() string { return "claude" }

func (e *ClaudeExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *ClaudeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, baseURL := claudeCreds(auth)

	if baseURL == "" {
		baseURL = ClaudeDefaultBaseURL
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)
	from := opts.SourceFormat
	stream := from.String() != "claude"
	body, err := TranslateToClaude(e.cfg, from, req.Model, req.Payload, stream, req.Metadata)
	if err != nil {
		return resp, err
	}
	modelForUpstream := req.Model
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		body, _ = sjson.SetBytes(body, "model", modelOverride)
		modelForUpstream = modelOverride
	}
	body = e.injectThinkingConfig(req.Model, body)

	if !strings.HasPrefix(modelForUpstream, "claude-3-5-haiku") {
		body = checkSystemInstructions(body)
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)

	body = ensureMaxTokensForThinking(req.Model, body)

	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)

	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(64)
	ub.WriteString(baseURL)
	ub.WriteString("/v1/messages?beta=true")
	url := ub.String()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return resp, NewTimeoutError("request timed out")
		}
		return resp, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = NewStatusError(httpResp.StatusCode, string(b), nil)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return resp, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		return resp, err
	}
	if stream {
		lines := bytes.Split(data, []byte("\n"))
		for _, line := range lines {
			if events, err := to_ir.ParseClaudeChunk(line); err == nil && len(events) > 0 {
				if u := extractUsageFromEvents(events); u != nil {
					reporter.publish(ctx, u)
				}
			}
		}
	} else {
		reporter.publish(ctx, extractUsageFromClaudeResponse(data))
	}

	claudeFrom := sdktranslator.FromString("claude")
	translatedResp, err := TranslateResponseNonStream(e.cfg, claudeFrom, from, data, req.Model)
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

func (e *ClaudeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiKey, baseURL := claudeCreds(auth)

	if baseURL == "" {
		baseURL = ClaudeDefaultBaseURL
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)
	from := opts.SourceFormat
	body, err := TranslateToClaude(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if err != nil {
		return nil, err
	}
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		body, _ = sjson.SetBytes(body, "model", modelOverride)
	}
	body = e.injectThinkingConfig(req.Model, body)
	body = checkSystemInstructions(body)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	body = ensureMaxTokensForThinking(req.Model, body)

	body, _ = sjson.SetBytes(body, "stream", true)

	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)

	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(64)
	ub.WriteString(baseURL)
	ub.WriteString("/v1/messages?beta=true")
	url := ub.String()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, true, extraBetas)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewTimeoutError("request timed out")
		}
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		err = NewStatusError(httpResp.StatusCode, string(b), nil)
		return nil, err
	}
	decodedBody, err := decodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return nil, err
	}

	if from.String() == "claude" {
		processor := &claudePassthroughProcessor{}
		return RunSSEStream(ctx, decodedBody, reporter, processor, StreamConfig{
			ExecutorName:       "claude",
			PassthroughOnEmpty: true,
		}), nil
	}

	streamCtx := NewStreamContext()
	translator := NewStreamTranslator(e.cfg, from, from.String(), req.Model, "msg-"+req.Model, streamCtx)
	processor := &claudeStreamProcessor{
		translator: translator,
	}
	return RunSSEStream(ctx, decodedBody, reporter, processor, StreamConfig{
		ExecutorName: "claude",
	}), nil
}

func (e *ClaudeExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey, baseURL := claudeCreds(auth)

	if baseURL == "" {
		baseURL = ClaudeDefaultBaseURL
	}

	from := opts.SourceFormat
	stream := from.String() != "claude"
	body, err := TranslateToClaude(e.cfg, from, req.Model, req.Payload, stream, req.Metadata)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	modelForUpstream := req.Model
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		body, _ = sjson.SetBytes(body, "model", modelOverride)
		modelForUpstream = modelOverride
	}

	if !strings.HasPrefix(modelForUpstream, "claude-3-5-haiku") {
		body = checkSystemInstructions(body)
	}

	var extraBetas []string
	extraBetas, body = extractAndRemoveBetas(body)

	ub := GetURLBuilder()
	defer ub.Release()
	ub.Grow(64)
	ub.WriteString(baseURL)
	ub.WriteString("/v1/messages/count_tokens?beta=true")
	url := ub.String()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	applyClaudeHeaders(httpReq, auth, apiKey, false, extraBetas)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return cliproxyexecutor.Response{}, NewTimeoutError("request timed out")
		}
		return cliproxyexecutor.Response{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return cliproxyexecutor.Response{}, NewStatusError(resp.StatusCode, string(b), nil)
	}
	decodedBody, err := decodeResponseBody(resp.Body, resp.Header.Get("Content-Encoding"))
	if err != nil {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := decodedBody.Close(); errClose != nil {
			log.Errorf("response body close error: %v", errClose)
		}
	}()
	data, err := io.ReadAll(decodedBody)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	return cliproxyexecutor.Response{Payload: data}, nil
}

func (e *ClaudeExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("claude executor: auth is nil")
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := claude.NewClaudeAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	auth.Metadata["email"] = td.Email
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "claude"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func extractAndRemoveBetas(body []byte) ([]string, []byte) {
	betasResult := gjson.GetBytes(body, "betas")
	if !betasResult.Exists() {
		return nil, body
	}
	var betas []string
	if betasResult.IsArray() {
		for _, item := range betasResult.Array() {
			if s := strings.TrimSpace(item.String()); s != "" {
				betas = append(betas, s)
			}
		}
	} else if s := strings.TrimSpace(betasResult.String()); s != "" {
		betas = append(betas, s)
	}
	body, _ = sjson.DeleteBytes(body, "betas")
	return betas, body
}

func (e *ClaudeExecutor) injectThinkingConfig(modelName string, body []byte) []byte {
	if gjson.GetBytes(body, "thinking").Exists() {
		return body
	}

	var budgetTokens int
	switch {
	case strings.HasSuffix(modelName, "-thinking-low"):
		budgetTokens = 1024
	case strings.HasSuffix(modelName, "-thinking-medium"):
		budgetTokens = 8192
	case strings.HasSuffix(modelName, "-thinking-high"):
		budgetTokens = 24576
	case strings.HasSuffix(modelName, "-thinking"):
		budgetTokens = 8192
	default:
		return body
	}

	body, _ = sjson.SetBytes(body, "thinking.type", "enabled")
	body, _ = sjson.SetBytes(body, "thinking.budget_tokens", budgetTokens)
	return body
}

func ensureMaxTokensForThinking(modelName string, body []byte) []byte {
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType != "enabled" {
		return body
	}

	budgetTokens := gjson.GetBytes(body, "thinking.budget_tokens").Int()
	if budgetTokens <= 0 {
		return body
	}

	maxTokens := gjson.GetBytes(body, "max_tokens").Int()

	maxCompletionTokens := 0
	if modelInfo := registry.GetGlobalRegistry().GetModelInfo(modelName); modelInfo != nil {
		maxCompletionTokens = modelInfo.MaxCompletionTokens
	}

	const fallbackBuffer = 4000
	requiredMaxTokens := budgetTokens + fallbackBuffer
	if maxCompletionTokens > 0 {
		requiredMaxTokens = int64(maxCompletionTokens)
	}

	if maxTokens < requiredMaxTokens {
		body, _ = sjson.SetBytes(body, "max_tokens", requiredMaxTokens)
	}
	return body
}

func (e *ClaudeExecutor) resolveUpstreamModel(alias string, auth *cliproxyauth.Auth) string {
	if alias == "" {
		return ""
	}
	switch alias {
	case "claude-opus-4-5-thinking", "claude-opus-4-5-thinking-low", "claude-opus-4-5-thinking-medium", "claude-opus-4-5-thinking-high":
		return "claude-opus-4-5-20251101"
	case "claude-sonnet-4-5-thinking":
		return "claude-sonnet-4-5-20250929"
	}
	entry := e.resolveClaudeConfig(auth)
	if entry == nil {
		return ""
	}
	for i := range entry.Models {
		model := entry.Models[i]
		name := strings.TrimSpace(model.Name)
		modelAlias := strings.TrimSpace(model.Alias)
		if modelAlias != "" {
			if strings.EqualFold(modelAlias, alias) {
				if name != "" {
					return name
				}
				return alias
			}
			continue
		}
		if name != "" && strings.EqualFold(name, alias) {
			return name
		}
	}
	return ""
}

func (e *ClaudeExecutor) resolveClaudeConfig(auth *cliproxyauth.Auth) *config.Provider {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.Providers {
		p := &e.cfg.Providers[i]
		if p.Type != config.ProviderTypeAnthropic {
			continue
		}
		cfgBase := strings.TrimSpace(p.BaseURL)
		apiKeys := p.GetAPIKeys()
		for _, apiKeyEntry := range apiKeys {
			cfgKey := strings.TrimSpace(apiKeyEntry.Key)
			if attrKey != "" && attrBase != "" {
				if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
					return p
				}
				continue
			}
			if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
				if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
					return p
				}
			}
			if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
				return p
			}
		}
	}
	if attrKey != "" {
		for i := range e.cfg.Providers {
			p := &e.cfg.Providers[i]
			if p.Type != config.ProviderTypeAnthropic {
				continue
			}
			apiKeys := p.GetAPIKeys()
			for _, apiKeyEntry := range apiKeys {
				if strings.EqualFold(strings.TrimSpace(apiKeyEntry.Key), attrKey) {
					return p
				}
			}
		}
	}
	return nil
}

func applyClaudeHeaders(r *http.Request, auth *cliproxyauth.Auth, apiKey string, stream bool, extraBetas []string) {
	r.Header.Set("Authorization", "Bearer "+apiKey)
	r.Header.Set("Content-Type", "application/json")

	var ginHeaders http.Header
	if ginCtx, ok := r.Context().Value("gin").(*gin.Context); ok && ginCtx != nil && ginCtx.Request != nil {
		ginHeaders = ginCtx.Request.Header
	}

	baseBetas := "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14,fine-grained-tool-streaming-2025-05-14"
	if val := strings.TrimSpace(ginHeaders.Get("Anthropic-Beta")); val != "" {
		baseBetas = val
		if !strings.Contains(val, "oauth") {
			baseBetas += ",oauth-2025-04-20"
		}
	}

	if len(extraBetas) > 0 {
		existingSet := make(map[string]bool)
		for _, b := range strings.Split(baseBetas, ",") {
			existingSet[strings.TrimSpace(b)] = true
		}
		for _, beta := range extraBetas {
			beta = strings.TrimSpace(beta)
			if beta != "" && !existingSet[beta] {
				baseBetas += "," + beta
				existingSet[beta] = true
			}
		}
	}
	r.Header.Set("Anthropic-Beta", baseBetas)

	misc.EnsureHeader(r.Header, ginHeaders, "Anthropic-Version", "2023-06-01")
	misc.EnsureHeader(r.Header, ginHeaders, "Anthropic-Dangerous-Direct-Browser-Access", "true")
	misc.EnsureHeader(r.Header, ginHeaders, "X-App", "cli")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Helper-Method", "stream")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Retry-Count", "0")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Runtime-Version", "v24.3.0")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Package-Version", "0.55.1")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Runtime", "node")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Lang", "js")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Arch", "arm64")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Os", "MacOS")
	misc.EnsureHeader(r.Header, ginHeaders, "X-Stainless-Timeout", "60")
	misc.EnsureHeader(r.Header, ginHeaders, "User-Agent", DefaultClaudeUserAgent)
	r.Header.Set("Connection", "keep-alive")
	r.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
	if stream {
		r.Header.Set("Accept", "text/event-stream")
	} else {
		r.Header.Set("Accept", "application/json")
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(r, attrs)
}

func claudeCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	return ExtractCreds(a, ClaudeCredsConfig)
}

func checkSystemInstructions(payload []byte) []byte {
	system := gjson.GetBytes(payload, "system")
	claudeCodeInstructions := `[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}]`
	if system.IsArray() {
		if gjson.GetBytes(payload, "system.0.text").String() != "You are Claude Code, Anthropic's official CLI for Claude." {
			system.ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "text" {
					claudeCodeInstructions, _ = sjson.SetRaw(claudeCodeInstructions, "-1", part.Raw)
				}
				return true
			})
			payload, _ = sjson.SetRawBytes(payload, "system", []byte(claudeCodeInstructions))
		}
	} else {
		payload, _ = sjson.SetRawBytes(payload, "system", []byte(claudeCodeInstructions))
	}
	return payload
}
