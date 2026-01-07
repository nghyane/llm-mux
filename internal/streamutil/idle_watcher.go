package streamutil

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// IdleWatcher provides shared idle detection for multiple streams.
// Instead of spawning 2 goroutines per stream, uses a single goroutine
// with a timer wheel pattern to monitor all active streams.
type IdleWatcher struct {
	mu       sync.RWMutex
	streams  map[uint64]*watchedStream
	nextID   atomic.Uint64
	interval time.Duration
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

type watchedStream struct {
	lastActivity atomic.Int64
	timeout      time.Duration
	onIdle       func()
	ctx          context.Context
	cancel       context.CancelFunc
	stopAfter    func() bool
}

// NewIdleWatcher creates a shared idle watcher.
// checkInterval determines how often streams are checked (recommended: 5-10s).
func NewIdleWatcher(checkInterval time.Duration) *IdleWatcher {
	if checkInterval <= 0 {
		checkInterval = 10 * time.Second
	}
	w := &IdleWatcher{
		streams:  make(map[uint64]*watchedStream),
		interval: checkInterval,
		stopCh:   make(chan struct{}),
	}
	w.wg.Add(1)
	go w.watchLoop()
	return w
}

// Register adds a stream to be watched for idle timeout.
// Returns:
//   - id: unique identifier for this stream
//   - touch: function to call on each read activity
//   - done: function to call when stream is complete
func (w *IdleWatcher) Register(ctx context.Context, timeout time.Duration, onIdle func()) (touch func(), done func()) {
	id := w.nextID.Add(1)

	streamCtx, cancel := context.WithCancel(ctx)

	stream := &watchedStream{
		timeout: timeout,
		onIdle:  onIdle,
		ctx:     streamCtx,
		cancel:  cancel,
	}
	stream.lastActivity.Store(time.Now().UnixNano())

	w.mu.Lock()
	w.streams[id] = stream
	w.mu.Unlock()

	touch = func() {
		stream.lastActivity.Store(time.Now().UnixNano())
	}

	var doneOnce sync.Once
	cleanup := func() {
		doneOnce.Do(func() {
			w.mu.Lock()
			delete(w.streams, id)
			w.mu.Unlock()
			cancel()
			if stream.stopAfter != nil {
				stream.stopAfter()
			}
		})
	}

	done = cleanup

	stream.stopAfter = context.AfterFunc(ctx, func() {
		if onIdle != nil {
			onIdle()
		}
		cleanup()
	})

	return touch, done
}

func (w *IdleWatcher) watchLoop() {
	defer w.wg.Done()
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	for {
		select {
		case <-w.stopCh:
			return
		case now := <-ticker.C:
			w.checkStreams(now)
		}
	}
}

func (w *IdleWatcher) checkStreams(now time.Time) {
	nowNano := now.UnixNano()

	w.mu.RLock()
	// Collect streams to check (avoid holding lock during callbacks)
	toCheck := make([]*watchedStream, 0, len(w.streams))
	for _, stream := range w.streams {
		toCheck = append(toCheck, stream)
	}
	w.mu.RUnlock()

	for _, stream := range toCheck {
		// Skip if context already cancelled
		select {
		case <-stream.ctx.Done():
			continue
		default:
		}

		lastActive := stream.lastActivity.Load()
		idle := time.Duration(nowNano - lastActive)

		if idle > stream.timeout {
			// Trigger idle callback
			if stream.onIdle != nil {
				stream.onIdle()
			}
			stream.cancel()
		}
	}
}

// Stop stops the idle watcher and cleans up.
func (w *IdleWatcher) Stop() {
	close(w.stopCh)
	w.wg.Wait()

	// Cancel all remaining streams
	w.mu.Lock()
	for _, stream := range w.streams {
		stream.cancel()
	}
	w.streams = nil
	w.mu.Unlock()
}

func (w *IdleWatcher) ActiveCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.streams)
}

var defaultWatcher = sync.OnceValue(func() *IdleWatcher {
	return NewIdleWatcher(10 * time.Second)
})

func DefaultIdleWatcher() *IdleWatcher {
	return defaultWatcher()
}
