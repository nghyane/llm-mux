package executor

import (
	"context"
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
