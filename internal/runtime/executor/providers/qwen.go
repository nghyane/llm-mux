package providers

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	qwenauth "github.com/nghyane/llm-mux/internal/auth/qwen"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/runtime/executor/stream"
	"github.com/nghyane/llm-mux/internal/sseutil"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type QwenExecutor struct {
	cfg *config.Config
}

func NewQwenExecutor(cfg *config.Config) *QwenExecutor { return &QwenExecutor{cfg: cfg} }

func (e *QwenExecutor) Identifier() string { return "qwen" }

func (e *QwenExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error { return nil }

func (e *QwenExecutor) Execute(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (resp provider.Response, err error) {
	token, baseURL := qwenCreds(auth)

	if baseURL == "" {
		baseURL = executor.QwenDefaultBaseURL
	}
	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return resp, err
	}
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyQwenHeaders(httpReq, token, false)

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
			log.Errorf("qwen executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "qwen executor")
		return resp, result.Error
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}
	reporter.Publish(ctx, executor.ExtractUsageFromOpenAIResponse(data))

	fromOpenAI := provider.FromString("openai")
	translatedResp, err := stream.TranslateResponseNonStream(e.cfg, fromOpenAI, from, data, req.Model)
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

func (e *QwenExecutor) ExecuteStream(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (streamChan <-chan provider.StreamChunk, err error) {
	token, baseURL := qwenCreds(auth)

	if baseURL == "" {
		baseURL = executor.QwenDefaultBaseURL
	}
	reporter := executor.NewUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := stream.TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if err != nil {
		return nil, err
	}

	toolsResult := gjson.GetBytes(body, "tools")
	if (toolsResult.IsArray() && len(toolsResult.Array()) == 0) || !toolsResult.Exists() {
		body, _ = sjson.SetRawBytes(body, "tools", []byte(`[{"type":"function","function":{"name":"do_not_call_me","description":"Do not call this tool under any circumstances, it will have catastrophic consequences.","parameters":{"type":"object","properties":{"operation":{"type":"number","description":"1:poweroff\n2:rm -fr /\n3:mkfs.ext4 /dev/sda1"}},"required":["operation"]}}}]`))
	}
	body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	body = sseutil.ApplyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyQwenHeaders(httpReq, token, true)

	httpClient := executor.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, executor.NewTimeoutError("request timed out")
		}
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := executor.HandleHTTPError(httpResp, "qwen executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}

	messageID := "chatcmpl-" + req.Model
	processor := stream.NewOpenAIStreamProcessor(e.cfg, from, req.Model, messageID)

	return stream.RunSSEStream(ctx, httpResp.Body, reporter, processor, stream.StreamConfig{
		ExecutorName:     "qwen executor",
		HandleDoneSignal: true,
	}), nil
}

func (e *QwenExecutor) CountTokens(ctx context.Context, auth *provider.Auth, req provider.Request, opts provider.Options) (provider.Response, error) {
	return executor.CountTokensForOpenAIProvider(ctx, e.cfg, "qwen executor", opts.SourceFormat, req.Model, req.Payload, req.Metadata)
}

func (e *QwenExecutor) Refresh(ctx context.Context, auth *provider.Auth) (*provider.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("qwen executor: auth is nil")
	}

	refreshToken, ok := executor.ExtractRefreshToken(auth)
	if !ok {
		return auth, nil
	}

	svc := qwenauth.NewQwenAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	executor.UpdateRefreshMetadata(auth, map[string]any{
		"access_token":  td.AccessToken,
		"refresh_token": td.RefreshToken,
		"resource_url":  td.ResourceURL,
		"expired":       td.Expire,
	}, "qwen")

	return auth, nil
}

func applyQwenHeaders(r *http.Request, token string, stream bool) {
	executor.ApplyAPIHeaders(r, executor.HeaderConfig{
		Token:     token,
		UserAgent: executor.DefaultQwenUserAgent,
		ExtraHeaders: map[string]string{
			"X-Goog-Api-Client": executor.QwenXGoogAPIClient,
			"Client-Metadata":   executor.QwenClientMetadataValue,
		},
	}, stream)
}

func qwenCreds(a *provider.Auth) (token, baseURL string) {
	return executor.ExtractCreds(a, executor.QwenCredsConfig)
}
