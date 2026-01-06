package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/sseutil"
	"github.com/nghyane/llm-mux/internal/util"
	"github.com/tidwall/sjson"
)

type OpenAICompatExecutor struct {
	cfg      *config.Config
	provider string
}

func NewOpenAICompatExecutor(providerName string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{cfg: cfg, provider: providerName}
}

func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

func (e *OpenAICompatExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = executor.NewStatusError(http.StatusUnauthorized, "missing provider baseURL", nil)
		return
	}

	from := opts.SourceFormat
	translated, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, opts.Stream, nil)
	if err != nil {
		return resp, err
	}
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		translated = e.overrideModel(translated, modelOverride)
	}
	translated = sseutil.ApplyPayloadConfigWithRoot(e.cfg, req.Model, "openai", "", translated)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	executor.SetCommonHeaders(httpReq, "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)

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
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "openai-compat executor")
		return resp, result.Error
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}
	reporter.Publish(ctx, executor.ExtractUsageFromOpenAIResponse(body))
	reporter.EnsurePublished(ctx)

	fromOpenAI := provider.FromString("openai")
	translatedResp, err := stream.TranslateResponseNonStream(e.cfg, fromOpenAI, from, body, req.Model)
	if err != nil {
		return resp, err
	}
	if translatedResp != nil {
		resp = provider.Response{Payload: translatedResp}
	} else {
		resp = provider.Response{Payload: body}
	}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = executor.NewStatusError(http.StatusUnauthorized, "missing provider baseURL", nil)
		return nil, err
	}
	from := opts.SourceFormat
	translated, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, nil)
	if err != nil {
		return nil, err
	}
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		translated = e.overrideModel(translated, modelOverride)
	}
	translated = sseutil.ApplyPayloadConfigWithRoot(e.cfg, req.Model, "openai", "", translated)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	executor.SetCommonHeaders(httpReq, "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, executor.NewTimeoutError("request timed out")
		}
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "openai-compat executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	messageID := "chatcmpl-" + req.Model
	processor := stream.NewOpenAIStreamProcessor(e.cfg, from, req.Model, messageID)
	return stream.RunSSEStream(ctx, httpResp.Body, reporter, processor, stream.StreamConfig{
		ExecutorName:     "openai-compat",
		Preprocessor:     stream.DataTagPreprocessor(),
		HandleDoneSignal: true,
		EnsurePublished:  true,
	}), nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	from := opts.SourceFormat
	translated, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, nil)
	if err != nil {
		return provider.Response{}, err
	}

	modelForCounting := req.Model
	if modelOverride := e.resolveUpstreamModel(req.Model, auth); modelOverride != "" {
		translated = e.overrideModel(translated, modelOverride)
		modelForCounting = modelOverride
	}

	enc, err := executor.TokenizerForModel(modelForCounting)
	if err != nil {
		return provider.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := executor.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return provider.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := executor.BuildOpenAIUsageJSON(count)
	return provider.Response{Payload: usageJSON}, nil
}

func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	_ = ctx
	return auth, nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *provider.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	baseURL = executor.AttrStringValue(auth.Attributes, "base_url")
	apiKey = executor.AttrStringValue(auth.Attributes, "api_key")
	return
}

func (e *OpenAICompatExecutor) resolveUpstreamModel(alias string, auth *provider.Auth) string {
	if alias == "" || auth == nil || e.cfg == nil {
		return ""
	}
	compat := e.resolveCompatConfig(auth)
	if compat == nil {
		return ""
	}
	for i := range compat.Models {
		model := compat.Models[i]
		if model.Alias != "" {
			if strings.EqualFold(model.Alias, alias) {
				if model.Name != "" {
					return model.Name
				}
				return alias
			}
			continue
		}
		if strings.EqualFold(model.Name, alias) {
			return model.Name
		}
	}
	return ""
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *provider.Auth) *config.Provider {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if v := executor.AttrStringValue(auth.Attributes, "compat_name"); v != "" {
		candidates = append(candidates, v)
	}
	if v := executor.AttrStringValue(auth.Attributes, "provider_key"); v != "" {
		candidates = append(candidates, v)
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.Providers {
		prov := &e.cfg.Providers[i]
		if prov.Type != config.ProviderTypeOpenAI {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), prov.Name) {
				return prov
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}
