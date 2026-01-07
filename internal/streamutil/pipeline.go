// Package streamutil provides optimized streaming utilities using Go standard patterns.
// It consolidates multiple channel wrappers into a single efficient pipeline.
package streamutil

import (
	"context"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"
)

// Chunk represents a single unit of streaming data.
type Chunk struct {
	Data []byte
	Err  error
}

// Pipeline manages a streaming pipeline with proper lifecycle control.
// Uses errgroup for goroutine management and single channel for data flow.
type Pipeline struct {
	ctx    context.Context
	cancel context.CancelFunc
	group  *errgroup.Group
	output chan Chunk

	// Callbacks for extension points (stats, logging, etc.)
	onChunk    func(Chunk)
	onComplete func(success bool, elapsed time.Duration)
	onError    func(error)

	startTime time.Time
	mu        sync.Mutex
	completed bool
	hasError  bool
}

// PipelineConfig holds configuration for creating a new pipeline.
type PipelineConfig struct {
	// BufferSize for the output channel (default: 128)
	BufferSize int

	// OnChunk is called for each chunk passing through (optional)
	OnChunk func(Chunk)

	// OnComplete is called when pipeline finishes (optional)
	OnComplete func(success bool, elapsed time.Duration)

	// OnError is called when an error occurs (optional)
	OnError func(error)
}

// DefaultPipelineConfig returns sensible defaults.
func DefaultPipelineConfig() PipelineConfig {
	return PipelineConfig{
		BufferSize: 128,
	}
}

// NewPipeline creates a new streaming pipeline with proper lifecycle management.
// Uses errgroup.WithContext for automatic cancellation on error.
func NewPipeline(parent context.Context, cfg PipelineConfig) *Pipeline {
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 128
	}

	ctx, cancel := context.WithCancel(parent)
	g, gctx := errgroup.WithContext(ctx)

	return &Pipeline{
		ctx:        gctx,
		cancel:     cancel,
		group:      g,
		output:     make(chan Chunk, cfg.BufferSize),
		onChunk:    cfg.OnChunk,
		onComplete: cfg.OnComplete,
		onError:    cfg.OnError,
		startTime:  time.Now(),
	}
}

// Context returns the pipeline's context.
func (p *Pipeline) Context() context.Context {
	return p.ctx
}

// Output returns the read-only output channel.
func (p *Pipeline) Output() <-chan Chunk {
	return p.output
}

// Go starts a new goroutine in the pipeline's errgroup.
// If f returns an error, all other goroutines in the group are cancelled.
func (p *Pipeline) Go(f func(ctx context.Context) error) {
	p.group.Go(func() error {
		return f(p.ctx)
	})
}

// Send sends a chunk to the output channel.
// Returns false if context is cancelled.
func (p *Pipeline) Send(chunk Chunk) bool {
	if chunk.Err != nil {
		p.mu.Lock()
		p.hasError = true
		p.mu.Unlock()
		if p.onError != nil {
			p.onError(chunk.Err)
		}
	}

	if p.onChunk != nil {
		p.onChunk(chunk)
	}

	select {
	case p.output <- chunk:
		return true
	case <-p.ctx.Done():
		return false
	}
}

// SendData is a convenience method to send data bytes.
func (p *Pipeline) SendData(data []byte) bool {
	return p.Send(Chunk{Data: data})
}

// SendError is a convenience method to send an error.
func (p *Pipeline) SendError(err error) bool {
	return p.Send(Chunk{Err: err})
}

// Close closes the pipeline and waits for all goroutines to finish.
// Should be called in a defer after creating the pipeline.
func (p *Pipeline) Close() error {
	p.mu.Lock()
	if p.completed {
		p.mu.Unlock()
		return nil
	}
	p.completed = true
	hasError := p.hasError
	p.mu.Unlock()

	// Wait for all goroutines in the group
	err := p.group.Wait()

	// Close output channel
	close(p.output)

	// Call completion callback
	if p.onComplete != nil {
		success := err == nil && !hasError
		p.onComplete(success, time.Since(p.startTime))
	}

	// Cancel context
	p.cancel()

	return err
}

// Cancel cancels the pipeline immediately.
func (p *Pipeline) Cancel() {
	p.cancel()
}

// Start launches a background goroutine that waits for all Go() goroutines
// to complete, then closes the output channel. This enables consumers to
// detect completion via channel close without explicit Close() calls.
// Use this when you want automatic cleanup after producers finish.
func (p *Pipeline) Start() {
	go func() {
		_ = p.Close()
	}()
}
