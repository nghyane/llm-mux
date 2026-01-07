package stream

import (
	"context"
	"io"
	"sync"
	"sync/atomic"

	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/streamutil"
	"time"
)

// StreamReader wraps an io.ReadCloser with context-aware cancellation and idle detection.
//
// Design principles:
// 1. Data integrity: Never lose data due to arbitrary timeouts
// 2. Context-aware: Immediately respond to context cancellation
// 3. Idle detection: Safety net for stalled upstream connections
// 4. High concurrency: Uses shared IdleWatcher to minimize goroutine overhead
//
// How it works:
// - Uses shared IdleWatcher (1 goroutine for all streams) instead of per-stream watchers
// - When context is cancelled, body is closed immediately via shared watcher
// - Activity is tracked on every successful read
type StreamReader struct {
	body         io.ReadCloser
	ctx          context.Context
	closed       atomic.Bool
	closeOnce    sync.Once
	closeErr     error
	touch        func()
	done         func()
	executorName string
}

// NewStreamReader creates a new context-aware stream reader.
//
// Parameters:
//   - ctx: When cancelled, body is closed to unblock reads immediately
//   - body: The underlying HTTP response body
//   - idleTimeout: Safety timeout for stalled connections (0 = disabled, recommended: 3-5 minutes)
//   - executorName: For logging purposes
//
// Uses shared IdleWatcher to minimize goroutine overhead (1 watcher goroutine for all streams).
const noIdleTimeoutSentinel = 24 * time.Hour

func NewStreamReader(ctx context.Context, body io.ReadCloser, idleTimeout time.Duration, executorName string) *StreamReader {
	sr := &StreamReader{
		body:         body,
		ctx:          ctx,
		executorName: executorName,
	}

	if idleTimeout <= 0 {
		idleTimeout = noIdleTimeoutSentinel
	}

	sr.touch, sr.done = streamutil.DefaultIdleWatcher().Register(ctx, idleTimeout, func() {
		if idleTimeout != noIdleTimeoutSentinel {
			log.Warnf("%s: stream stalled (idle timeout), closing connection", executorName)
		}
		sr.closeWithReason("idle timeout")
	})

	return sr
}

// Read implements io.Reader.
// Updates activity timestamp on successful reads to reset idle timer.
func (sr *StreamReader) Read(p []byte) (int, error) {
	if sr.closed.Load() {
		return 0, io.EOF
	}

	n, err := sr.body.Read(p)
	if n > 0 {
		sr.touch() // Update activity in shared watcher
	}
	return n, err
}

// closeWithReason closes the body and logs the reason.
func (sr *StreamReader) closeWithReason(reason string) {
	sr.closeOnce.Do(func() {
		sr.closed.Store(true)
		sr.closeErr = sr.body.Close()
		log.Debugf("%s: stream closed: %s", sr.executorName, reason)
	})
}

// Close implements io.Closer. Safe to call multiple times.
func (sr *StreamReader) Close() error {
	sr.closeWithReason("explicit close")
	// Unregister from shared watcher
	if sr.done != nil {
		sr.done()
	}
	return sr.closeErr
}
