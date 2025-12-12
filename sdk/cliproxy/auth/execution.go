package auth

import (
	"context"
	"errors"
	"time"

	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	"github.com/nghyane/llm-mux/internal/registry"
)

// ExecuteWithProvider handles non-streaming execution for a single provider, attempting
// multiple auth candidates until one succeeds or all are exhausted.
func (m *Manager) executeWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if provider == "" {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}

	// Translate canonical model ID to provider-specific ID using registry
	req.Model = registry.GetGlobalRegistry().GetModelIDForProvider(req.Model, provider)

	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		resp, errExec := executor.Execute(execCtx, auth, req, opts)
		result := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: errExec == nil}
		if errExec != nil {
			result.Error = &Error{Message: errExec.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errExec, &se) && se != nil {
				result.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errExec); ra != nil {
				result.RetryAfter = ra
			}
			m.MarkResult(execCtx, result)
			lastErr = errExec
			continue
		}
		m.MarkResult(execCtx, result)
		return resp, nil
	}
}

// ExecuteCountWithProvider handles token counting for a single provider, attempting
// multiple auth candidates until one succeeds or all are exhausted.
func (m *Manager) executeCountWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if provider == "" {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}

	// Translate canonical model ID to provider-specific ID using registry
	req.Model = registry.GetGlobalRegistry().GetModelIDForProvider(req.Model, provider)

	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return cliproxyexecutor.Response{}, lastErr
			}
			return cliproxyexecutor.Response{}, errPick
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		resp, errExec := executor.CountTokens(execCtx, auth, req, opts)
		result := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: errExec == nil}
		if errExec != nil {
			result.Error = &Error{Message: errExec.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errExec, &se) && se != nil {
				result.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errExec); ra != nil {
				result.RetryAfter = ra
			}
			m.MarkResult(execCtx, result)
			lastErr = errExec
			continue
		}
		m.MarkResult(execCtx, result)
		return resp, nil
	}
}

// ExecuteStreamWithProvider handles streaming execution for a single provider, attempting
// multiple auth candidates until one succeeds or all are exhausted.
func (m *Manager) executeStreamWithProvider(ctx context.Context, provider string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (<-chan cliproxyexecutor.StreamChunk, error) {
	if provider == "" {
		return nil, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}

	// Translate canonical model ID to provider-specific ID using registry
	req.Model = registry.GetGlobalRegistry().GetModelIDForProvider(req.Model, provider)

	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errPick
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
			execCtx = context.WithValue(execCtx, "cliproxy.roundtripper", rt)
		}
		chunks, errStream := executor.ExecuteStream(execCtx, auth, req, opts)
		if errStream != nil {
			rerr := &Error{Message: errStream.Error()}
			var se cliproxyexecutor.StatusError
			if errors.As(errStream, &se) && se != nil {
				rerr.HTTPStatus = se.StatusCode()
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(errStream)
			m.MarkResult(execCtx, result)
			lastErr = errStream
			continue
		}
		out := make(chan cliproxyexecutor.StreamChunk, 1)
		go func(streamCtx context.Context, streamAuth *Auth, streamProvider string, streamChunks <-chan cliproxyexecutor.StreamChunk) {
			defer close(out)
			var failed bool
			for {
				select {
				case <-streamCtx.Done():
					// Context cancelled by client - don't count as provider failure
					// This prevents penalizing providers when users disconnect
					return
				case chunk, ok := <-streamChunks:
					if !ok {
						// Input channel closed - stream complete
						if !failed {
							m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamProvider, Model: req.Model, Success: true})
						}
						return
					}
					if chunk.Err != nil && !failed {
						failed = true
						rerr := &Error{Message: chunk.Err.Error()}
						var se cliproxyexecutor.StatusError
						if errors.As(chunk.Err, &se) && se != nil {
							rerr.HTTPStatus = se.StatusCode()
						}
						m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamProvider, Model: req.Model, Success: false, Error: rerr})
					}
					// Non-blocking send with context check
					select {
					case out <- chunk:
					case <-streamCtx.Done():
						return
					}
				}
			}
		}(execCtx, auth.Clone(), provider, chunks)
		return out, nil
	}
}

// executeProvidersOnce attempts execution across multiple providers in sequence,
// returning the first successful response.
func (m *Manager) executeProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (cliproxyexecutor.Response, error)) (cliproxyexecutor.Response, error) {
	if len(providers) == 0 {
		return cliproxyexecutor.Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	var lastErr error
	for _, provider := range providers {
		resp, errExec := fn(ctx, provider)
		if errExec == nil {
			return resp, nil
		}
		lastErr = errExec
	}
	if lastErr != nil {
		return cliproxyexecutor.Response{}, lastErr
	}
	return cliproxyexecutor.Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// executeStreamProvidersOnce attempts streaming execution across multiple providers in sequence,
// returning the first successful stream.
func (m *Manager) executeStreamProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (<-chan cliproxyexecutor.StreamChunk, error)) (<-chan cliproxyexecutor.StreamChunk, error) {
	if len(providers) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	var lastErr error
	for _, provider := range providers {
		chunks, errExec := fn(ctx, provider)
		if errExec == nil {
			return chunks, nil
		}
		lastErr = errExec
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// wrapStreamForStats wraps a stream channel to record stats when complete.
// Uses a buffered channel and non-blocking sends to prevent goroutine leaks
// when consumers stop reading.
// Context cancellation (user disconnect) is not counted as provider failure.
func (m *Manager) wrapStreamForStats(ctx context.Context, in <-chan cliproxyexecutor.StreamChunk, provider, model string, start time.Time) <-chan cliproxyexecutor.StreamChunk {
	out := make(chan cliproxyexecutor.StreamChunk, 1)
	go func() {
		defer close(out)
		hasError := false
		for {
			select {
			case <-ctx.Done():
				// Context cancelled by client - don't count as provider failure
				return
			case chunk, ok := <-in:
				if !ok {
					// Input channel closed - stream complete
					m.recordProviderResult(provider, model, !hasError, time.Since(start))
					return
				}
				if chunk.Err != nil {
					hasError = true
				}
				// Non-blocking send with context check
				select {
				case out <- chunk:
				case <-ctx.Done():
					// Context cancelled by client - don't count as provider failure
					return
				}
			}
		}
	}()
	return out
}
