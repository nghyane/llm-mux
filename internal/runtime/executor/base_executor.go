package executor

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/sseutil"
)

// Context keys for executor-specific values (merged from context_keys.go)
type altContextKey struct{}

// AltContextKey is an exported version of altContextKey for use in sub-packages.
type AltContextKey = altContextKey

type BaseExecutor struct {
	Cfg *config.Config
}

func (b *BaseExecutor) Config() *config.Config {
	return b.Cfg
}

func (b *BaseExecutor) PrepareRequest(_ *http.Request, _ *provider.Auth) error {
	return nil
}

func (b *BaseExecutor) NewHTTPClient(ctx context.Context, auth *provider.Auth, timeout time.Duration) *http.Client {
	return NewProxyAwareHTTPClient(ctx, b.Cfg, auth, timeout)
}

func (b *BaseExecutor) NewUsageReporter(ctx context.Context, prov, model string, auth *provider.Auth) *usageReporter {
	return NewUsageReporter(ctx, prov, model, auth)
}

func (b *BaseExecutor) ApplyPayloadConfig(model string, payload []byte) []byte {
	return sseutil.ApplyPayloadConfig(b.Cfg, model, payload)
}

func (b *BaseExecutor) RefreshNoOp(_ context.Context, auth *provider.Auth) (*provider.Auth, error) {
	return auth, nil
}

func (b *BaseExecutor) CountTokensNotSupported(prov string) (provider.Response, error) {
	return provider.Response{}, NewNotImplementedError("count tokens not supported for " + prov)
}

func SetCommonHeaders(req *http.Request, contentType string) {
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Accept-Encoding", "gzip, deflate, br, zstd")
}

// HTTPRequestConfig holds configuration for making HTTP requests.
type HTTPRequestConfig struct {
	Method      string
	URL         string
	Body        []byte
	Headers     map[string]string
	Auth        *provider.Auth
	Timeout     time.Duration
	ContentType string
}

// HTTPResult holds the result of an HTTP request.
type HTTPResult struct {
	StatusCode int
	Body       []byte
	Headers    http.Header
	Error      error
}

// DoRequest executes an HTTP request with standard error handling.
// It handles request creation, common headers, timeout errors, status code checks,
// response body decoding (gzip, brotli, zstd, deflate), and body reading.
func (b *BaseExecutor) DoRequest(ctx context.Context, cfg HTTPRequestConfig) (*HTTPResult, error) {
	httpReq, err := http.NewRequestWithContext(ctx, cfg.Method, cfg.URL, bytes.NewReader(cfg.Body))
	if err != nil {
		return nil, err
	}

	SetCommonHeaders(httpReq, cfg.ContentType)
	for k, v := range cfg.Headers {
		httpReq.Header.Set(k, v)
	}

	httpClient := b.NewHTTPClient(ctx, cfg.Auth, cfg.Timeout)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, NewTimeoutError("request timed out")
		}
		return nil, err
	}
	defer func() {
		_ = httpResp.Body.Close()
	}()

	decodedBody, err := DecodeResponseBody(httpResp.Body, httpResp.Header.Get("Content-Encoding"))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = decodedBody.Close()
	}()

	data, err := io.ReadAll(decodedBody)
	if err != nil {
		return nil, err
	}

	result := &HTTPResult{
		StatusCode: httpResp.StatusCode,
		Body:       data,
		Headers:    httpResp.Header,
	}

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		result.Error = NewStatusError(httpResp.StatusCode, string(data), nil)
	}

	return result, nil
}
