package executor

import (
	"context"
	"io"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
)

// StreamReader wraps an io.ReadCloser with context-aware cancellation and idle detection.
//
// Design principles:
// 1. Data integrity: Never lose data due to arbitrary timeouts
// 2. Context-aware: Immediately respond to context cancellation
// 3. Idle detection: Safety net for stalled upstream connections
// 4. High concurrency: Minimal lock contention, uses atomics
//
// How it works:
// - When context is cancelled, body is closed immediately, unblocking any pending Read()
// - Idle watchdog runs periodically (not per-read) to detect truly stalled connections
// - Activity is tracked on every successful read
type StreamReader struct {
	body         io.ReadCloser
	ctx          context.Context
	closed       atomic.Bool
	closeOnce    sync.Once
	closeErr     error
	lastActivity atomic.Int64 // UnixNano timestamp of last read activity
	idleTimeout  time.Duration
	stopWatchdog chan struct{}
	executorName string
}

// NewStreamReader creates a new context-aware stream reader.
//
// Parameters:
//   - ctx: When cancelled, body is closed to unblock reads immediately
//   - body: The underlying HTTP response body
//   - idleTimeout: Safety timeout for stalled connections (0 = disabled, recommended: 3-5 minutes)
//   - executorName: For logging purposes
func NewStreamReader(ctx context.Context, body io.ReadCloser, idleTimeout time.Duration, executorName string) *StreamReader {
	sr := &StreamReader{
		body:         body,
		ctx:          ctx,
		idleTimeout:  idleTimeout,
		stopWatchdog: make(chan struct{}),
		executorName: executorName,
	}
	sr.touch() // Initialize last activity

	// Start background monitors
	go sr.watchContext()
	if idleTimeout > 0 {
		go sr.watchIdle()
	}

	return sr
}

// touch updates the last activity timestamp (called on every successful read).
func (sr *StreamReader) touch() {
	sr.lastActivity.Store(time.Now().UnixNano())
}

// watchContext closes body when context is cancelled, immediately unblocking any pending Read().
func (sr *StreamReader) watchContext() {
	select {
	case <-sr.ctx.Done():
		sr.closeWithReason("context cancelled")
	case <-sr.stopWatchdog:
		// Normal completion
	}
}

// watchIdle periodically checks for stalled connections.
// This is a safety net - it only triggers if upstream completely stops sending data.
func (sr *StreamReader) watchIdle() {
	// Check interval: 1/4 of timeout, clamped between 10s and 30s
	checkInterval := sr.idleTimeout / 4
	if checkInterval < 10*time.Second {
		checkInterval = 10 * time.Second
	}
	if checkInterval > 30*time.Second {
		checkInterval = 30 * time.Second
	}

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-sr.ctx.Done():
			return
		case <-sr.stopWatchdog:
			return
		case <-ticker.C:
			if sr.closed.Load() {
				return
			}
			lastActive := time.Unix(0, sr.lastActivity.Load())
			idleTime := time.Since(lastActive)
			if idleTime > sr.idleTimeout {
				log.Warnf("%s: stream stalled for %v (limit: %v), closing connection",
					sr.executorName, idleTime.Round(time.Second), sr.idleTimeout)
				sr.closeWithReason("idle timeout - upstream stalled")
				return
			}
		}
	}
}

// Read implements io.Reader.
// Updates activity timestamp on successful reads to reset idle timer.
func (sr *StreamReader) Read(p []byte) (int, error) {
	if sr.closed.Load() {
		return 0, io.EOF
	}

	n, err := sr.body.Read(p)
	if n > 0 {
		sr.touch()
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
	// Signal watchdog goroutines to stop
	select {
	case <-sr.stopWatchdog:
	default:
		close(sr.stopWatchdog)
	}
	return sr.closeErr
}
