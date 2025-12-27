package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	qwenauth "github.com/nghyane/llm-mux/internal/auth/qwen"
	"github.com/nghyane/llm-mux/internal/config"
	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	sdktranslator "github.com/nghyane/llm-mux/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// QwenExecutor is a stateless executor for Qwen Code using OpenAI-compatible chat completions.
// If access token is unavailable, it falls back to legacy via ClientAdapter.
type QwenExecutor struct {
	cfg *config.Config
}

func NewQwenExecutor(cfg *config.Config) *QwenExecutor { return &QwenExecutor{cfg: cfg} }

func (e *QwenExecutor) Identifier() string { return "qwen" }

func (e *QwenExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *QwenExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, baseURL := qwenCreds(auth)

	if baseURL == "" {
		baseURL = QwenDefaultBaseURL
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return resp, err
	}
	body = applyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyQwenHeaders(httpReq, token, false)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("qwen executor: close response body error: %v", errClose)
		}
	}()
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "qwen executor")
		return resp, result.Error
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return resp, err
	}
	reporter.publish(ctx, extractUsageFromOpenAIResponse(data))

	translatedResp, err := TranslateOpenAIResponseNonStream(e.cfg, from, data, req.Model)
	if err != nil {
		return resp, err
	}
	if translatedResp != nil {
		resp = cliproxyexecutor.Response{Payload: translatedResp}
	} else {
		// Passthrough if translator returns nil
		resp = cliproxyexecutor.Response{Payload: data}
	}
	return resp, nil
}

func (e *QwenExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	token, baseURL := qwenCreds(auth)

	if baseURL == "" {
		baseURL = QwenDefaultBaseURL
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	body, err := TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, true, req.Metadata)
	if err != nil {
		return nil, err
	}

	toolsResult := gjson.GetBytes(body, "tools")
	// I'm addressing the Qwen3 "poisoning" issue, which is caused by the model needing a tool to be defined. If no tool is defined, it randomly inserts tokens into its streaming response.
	// This will have no real consequences. It's just to scare Qwen3.
	if (toolsResult.IsArray() && len(toolsResult.Array()) == 0) || !toolsResult.Exists() {
		body, _ = sjson.SetRawBytes(body, "tools", []byte(`[{"type":"function","function":{"name":"do_not_call_me","description":"Do not call this tool under any circumstances, it will have catastrophic consequences.","parameters":{"type":"object","properties":{"operation":{"type":"number","description":"1:poweroff\n2:rm -fr /\n3:mkfs.ext4 /dev/sda1"}},"required":["operation"]}}}]`))
	}
	body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyQwenHeaders(httpReq, token, true)

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		return nil, err
	}
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result := HandleHTTPError(httpResp, "qwen executor")
		_ = httpResp.Body.Close()
		return nil, result.Error
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	messageID := "chatcmpl-" + req.Model
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("qwen executor: close response body error: %v", errClose)
			}
		}()
		buf := scannerBufferPool.Get().([]byte)
		defer scannerBufferPool.Put(buf)
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(buf, DefaultStreamBufferSize)
		var streamState *OpenAIStreamState
		for scanner.Scan() {
			// Check context cancellation before processing each line
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Bytes()
			result, err := TranslateOpenAIResponseStreamWithUsage(e.cfg, from, bytes.Clone(line), req.Model, messageID, streamState)
			if err != nil {
				reporter.publishFailure(ctx)
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
		// Handle [DONE] signal
		doneResult, _ := TranslateOpenAIResponseStreamWithUsage(e.cfg, from, []byte("[DONE]"), req.Model, messageID, streamState)
		for _, chunk := range doneResult.Chunks {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
			case <-ctx.Done():
				return
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

func (e *QwenExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	body, err := TranslateToOpenAI(e.cfg, from, req.Model, req.Payload, false, req.Metadata)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	modelName := gjson.GetBytes(body, "model").String()
	if strings.TrimSpace(modelName) == "" {
		modelName = req.Model
	}

	enc, err := tokenizerForModel(modelName)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("qwen executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("qwen executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	to := formatOpenAI
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

func (e *QwenExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if auth == nil {
		return nil, fmt.Errorf("qwen executor: auth is nil")
	}

	refreshToken, ok := ExtractRefreshToken(auth)
	if !ok {
		return auth, nil
	}

	svc := qwenauth.NewQwenAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}

	UpdateRefreshMetadata(auth, map[string]any{
		"access_token":  td.AccessToken,
		"refresh_token": td.RefreshToken,
		"resource_url":  td.ResourceURL,
		"expired":       td.Expire,
	}, "qwen")

	return auth, nil
}

func applyQwenHeaders(r *http.Request, token string, stream bool) {
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("User-Agent", DefaultQwenUserAgent)
	r.Header.Set("X-Goog-Api-Client", QwenXGoogAPIClient)
	r.Header.Set("Client-Metadata", QwenClientMetadataValue)
	if stream {
		r.Header.Set("Accept", "text/event-stream")
		return
	}
	r.Header.Set("Accept", "application/json")
}

// qwenCreds extracts credentials for Qwen API.
// Delegates to the common ExtractCreds function with Qwen configuration.
func qwenCreds(a *cliproxyauth.Auth) (token, baseURL string) {
	return ExtractCreds(a, QwenCredsConfig)
}
