package provider

import (
	"context"
	"errors"
	"time"

	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/telemetry"
	"github.com/sony/gobreaker"
)

// ExecuteWithProvider handles non-streaming execution for a single provider, attempting
// multiple auth candidates until one succeeds or all are exhausted.
func (m *Manager) executeWithProvider(ctx context.Context, provider string, req Request, opts Options) (Response, error) {
	if provider == "" {
		return Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}

	start := time.Now()
	ctx, span := telemetry.StartProviderSpan(ctx, provider, req.Model)
	defer func() {
		telemetry.RecordLatency(span, start)
		span.End()
	}()

	breaker := m.getOrCreateBreaker(provider)
	if breaker.State() == gobreaker.StateOpen {
		return Response{}, &Error{Code: "circuit_open", Message: "provider circuit breaker is open"}
	}

	req.Model = registry.GetGlobalRegistry().GetModelIDForProvider(req.Model, provider)

	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			telemetry.RecordError(span, errPick)
			if lastErr != nil {
				return Response{}, lastErr
			}
			return Response{}, errPick
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		}

		authCopy := auth
		reqCopy := req
		result, errBreaker := breaker.Execute(func() (any, error) {
			return executor.Execute(execCtx, authCopy, reqCopy, opts)
		})

		if errBreaker != nil {
			telemetry.RecordError(span, errBreaker)
			markResult := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: false}
			markResult.Error = &Error{Message: errBreaker.Error()}
			var se StatusCodeError
			if errors.As(errBreaker, &se) && se != nil {
				markResult.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errBreaker); ra != nil {
				markResult.RetryAfter = ra
			}
			m.MarkResult(execCtx, markResult)
			lastErr = errBreaker
			continue
		}

		resp := result.(Response)
		m.MarkResult(execCtx, Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: true})
		return resp, nil
	}
}

// ExecuteCountWithProvider handles token counting for a single provider, attempting
// multiple auth candidates until one succeeds or all are exhausted.
func (m *Manager) executeCountWithProvider(ctx context.Context, provider string, req Request, opts Options) (Response, error) {
	if provider == "" {
		return Response{}, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}

	breaker := m.getOrCreateBreaker(provider)
	if breaker.State() == gobreaker.StateOpen {
		return Response{}, &Error{Code: "circuit_open", Message: "provider circuit breaker is open"}
	}

	req.Model = registry.GetGlobalRegistry().GetModelIDForProvider(req.Model, provider)

	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			if lastErr != nil {
				return Response{}, lastErr
			}
			return Response{}, errPick
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		}

		authCopy := auth
		reqCopy := req
		result, errBreaker := breaker.Execute(func() (any, error) {
			return executor.CountTokens(execCtx, authCopy, reqCopy, opts)
		})

		if errBreaker != nil {
			markResult := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: false}
			markResult.Error = &Error{Message: errBreaker.Error()}
			var se StatusCodeError
			if errors.As(errBreaker, &se) && se != nil {
				markResult.Error.HTTPStatus = se.StatusCode()
			}
			if ra := retryAfterFromError(errBreaker); ra != nil {
				markResult.RetryAfter = ra
			}
			m.MarkResult(execCtx, markResult)
			lastErr = errBreaker
			continue
		}

		resp := result.(Response)
		m.MarkResult(execCtx, Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: true})
		return resp, nil
	}
}

// ExecuteStreamWithProvider handles streaming execution for a single provider, attempting
// multiple auth candidates until one succeeds or all are exhausted.
func (m *Manager) executeStreamWithProvider(ctx context.Context, provider string, req Request, opts Options) (<-chan StreamChunk, error) {
	if provider == "" {
		return nil, &Error{Code: "provider_not_found", Message: "provider identifier is empty"}
	}

	breaker := m.getOrCreateStreamingBreaker(provider)
	done, err := breaker.Allow()
	if err != nil {
		return nil, &Error{Code: "circuit_open", Message: "provider circuit breaker is open"}
	}

	req.Model = registry.GetGlobalRegistry().GetModelIDForProvider(req.Model, provider)

	tried := make(map[string]struct{})
	var lastErr error
	for {
		auth, executor, errPick := m.pickNext(ctx, provider, req.Model, opts, tried)
		if errPick != nil {
			done(false)
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, errPick
		}

		tried[auth.ID] = struct{}{}
		execCtx := ctx
		if rt := m.roundTripperFor(auth); rt != nil {
			execCtx = context.WithValue(execCtx, roundTripperContextKey{}, rt)
		}
		chunks, errStream := executor.ExecuteStream(execCtx, auth, req, opts)
		if errStream != nil {
			rerr := &Error{Message: errStream.Error()}
			var se StatusCodeError
			if errors.As(errStream, &se) && se != nil {
				rerr.HTTPStatus = se.StatusCode()
			}
			result := Result{AuthID: auth.ID, Provider: provider, Model: req.Model, Success: false, Error: rerr}
			result.RetryAfter = retryAfterFromError(errStream)
			m.MarkResult(execCtx, result)
			lastErr = errStream
			continue
		}
		out := make(chan StreamChunk, 64) // Increased buffer for high throughput streaming
		go func(streamCtx context.Context, streamAuth *Auth, streamProvider string, streamChunks <-chan StreamChunk, cbDone func(bool)) {
			defer close(out)
			var failed bool
			for {
				select {
				case <-streamCtx.Done():
					cbDone(!failed)
					return
				case chunk, ok := <-streamChunks:
					if !ok {
						if !failed {
							m.MarkResult(streamCtx, Result{AuthID: streamAuth.ID, Provider: streamProvider, Model: req.Model, Success: true})
						}
						cbDone(!failed)
						return
					}
					if chunk.Err != nil && !failed {
						failed = true
						rerr := &Error{Message: chunk.Err.Error()}
						var se StatusCodeError
						if errors.As(chunk.Err, &se) && se != nil {
							rerr.HTTPStatus = se.StatusCode()
						}
						result := Result{AuthID: streamAuth.ID, Provider: streamProvider, Model: req.Model, Success: false, Error: rerr}
						result.RetryAfter = retryAfterFromError(chunk.Err)
						m.MarkResult(streamCtx, result)
					}
					select {
					case out <- chunk:
					case <-streamCtx.Done():
						cbDone(!failed)
						return
					}
				}
			}
		}(execCtx, auth.Clone(), provider, chunks, done)
		return out, nil
	}
}

// executeProvidersOnce attempts execution across multiple providers in sequence,
// returning the first successful response.
func (m *Manager) executeProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (Response, error)) (Response, error) {
	if len(providers) == 0 {
		return Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
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
		return Response{}, lastErr
	}
	return Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// executeStreamProvidersOnce attempts streaming execution across multiple providers in sequence,
// returning the first successful stream.
func (m *Manager) executeStreamProvidersOnce(ctx context.Context, providers []string, fn func(context.Context, string) (<-chan StreamChunk, error)) (<-chan StreamChunk, error) {
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
func (m *Manager) wrapStreamForStats(ctx context.Context, in <-chan StreamChunk, provider, model string, start time.Time) <-chan StreamChunk {
	out := make(chan StreamChunk, 64) // Increased buffer for high throughput streaming
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
