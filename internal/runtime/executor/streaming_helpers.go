// Package executor provides streaming utilities for SSE-based API responses.
//
// StreamingHelper abstracts the common SSE streaming pattern used across all
// executors, reducing ~50 lines of boilerplate per executor.
//
// Key components:
//   - StreamProcessor: Interface for translating SSE chunks (provider-specific)
//   - StreamConfig: Configuration for buffer sizes, preprocessing, done handling
//   - RunSSEStream: Main helper that handles goroutine, scanner, context cancellation
//   - GeminiPreprocessor: Pre-built preprocessor for Gemini-family APIs
//   - DataTagPreprocessor: Standard SSE "data:" prefix handler for OpenAI-style APIs
//
// Usage:
//
//	processor := &myStreamProcessor{...}
//	out := RunSSEStream(ctx, resp.Body, reporter, processor, StreamConfig{
//	    ExecutorName:     "my executor",
//	    Preprocessor:     GeminiPreprocessor(),
//	    HandleDoneSignal: true,
//	})
package executor

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"sync"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	cliproxyexecutor "github.com/nghyane/llm-mux/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// =============================================================================
// Buffer Pools for Performance
// =============================================================================

// scannerBufferPool pools scanner buffers to reduce allocations in hot path.
// Each streaming request reuses a 64KB buffer instead of allocating new ones.
var scannerBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, DefaultScannerBufferSize)
	},
}

// =============================================================================
// Pre-allocated Byte Slices (avoid repeated []byte("...") allocations)
// =============================================================================

var (
	doneMarker = []byte("[DONE]") // SSE done signal
	dataTag    = []byte("data:")  // SSE data prefix
)

// StreamProcessor defines the interface for processing SSE stream lines.
// Implementations handle provider-specific parsing and translation logic.
type StreamProcessor interface {
	// ProcessLine processes a single SSE line and returns translated chunks.
	// The line parameter contains raw bytes from the scanner (may include "data:" prefix).
	// Returns:
	//   - chunks: Zero or more translated output chunks to send to client
	//   - usage: Optional usage information extracted from this line
	//   - err: Processing error (terminates the stream if non-nil)
	ProcessLine(line []byte) (chunks [][]byte, usage *ir.Usage, err error)

	// ProcessDone handles the [DONE] signal (optional cleanup/final chunks).
	// Called when the stream encounters a done signal, if HandleDoneSignal is true.
	ProcessDone() (chunks [][]byte, err error)
}

// StreamPreprocessor is a function that pre-processes SSE lines before the main processor.
// It can transform or filter lines before they reach the StreamProcessor.
// Returns:
//   - payload: The preprocessed payload (may be modified or unchanged)
//   - skip: If true, skip this line entirely (don't pass to processor)
type StreamPreprocessor func(line []byte) (payload []byte, skip bool)

// StreamConfig configures the behavior of RunSSEStream.
type StreamConfig struct {
	// ExecutorName identifies the executor for logging purposes.
	ExecutorName string

	// MaxBufferSize is the maximum buffer size for the scanner (default: DefaultStreamBufferSize).
	MaxBufferSize int

	// Preprocessor is an optional function to pre-process lines before the main processor.
	Preprocessor StreamPreprocessor

	// SkipEmptyLines skips lines that are empty after preprocessing.
	SkipEmptyLines bool

	// PassthroughOnEmpty sends the raw line if processor returns no chunks.
	// Useful for passthrough scenarios where translation may not produce output.
	PassthroughOnEmpty bool

	// EnsurePublished calls reporter.ensurePublished at the end of successful streams.
	// Use this when the upstream may not always return usage information.
	EnsurePublished bool

	// HandleDoneSignal calls ProcessDone() when [DONE] is encountered.
	HandleDoneSignal bool

	// SkipDoneInData skips lines that contain "data: [DONE]" (OpenAI-style done signal).
	SkipDoneInData bool
}

// GeminiPreprocessor creates a preprocessor for Gemini/Antigravity streams.
// It applies FilterSSEUsageMetadata, extracts JSON payload, and validates JSON.
func GeminiPreprocessor() StreamPreprocessor {
	return func(line []byte) (payload []byte, skip bool) {
		// Filter usage metadata for non-terminal chunks
		filtered := FilterSSEUsageMetadata(line)

		// Extract JSON payload from SSE line (strips "data: " prefix)
		payload = jsonPayload(filtered)
		if payload == nil {
			return nil, true // Skip non-JSON lines (empty, comments, events, etc.)
		}

		// Validate JSON to handle malformed SSE data gracefully
		if !gjson.ValidBytes(payload) {
			log.Debugf("gemini preprocessor: skipping malformed SSE payload")
			return nil, true
		}

		return payload, false
	}
}

// DataTagPreprocessor creates a preprocessor that strips the "data: " prefix.
// It's suitable for standard SSE streams like OpenAI/Codex.
func DataTagPreprocessor() StreamPreprocessor {
	return func(line []byte) (payload []byte, skip bool) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			return nil, true
		}

		// Check for done signal (use pre-allocated doneMarker)
		if bytes.Equal(trimmed, doneMarker) {
			return trimmed, false // Let the main loop handle [DONE]
		}

		// Strip data: prefix if present
		if bytes.HasPrefix(trimmed, dataTag) {
			payload = bytes.TrimSpace(trimmed[len(dataTag):])
		} else {
			payload = trimmed
		}

		if len(payload) == 0 {
			return nil, true
		}

		return payload, false
	}
}

// sendChunk sends a chunk to the output channel with context cancellation support.
// Returns true if the chunk was sent, false if context was cancelled.
func sendChunk(ctx context.Context, out chan<- cliproxyexecutor.StreamChunk, chunk cliproxyexecutor.StreamChunk) bool {
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

// isDoneLine checks if a line represents an SSE done signal.
// Uses pre-allocated byte slices to avoid allocations in hot path.
func isDoneLine(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if bytes.Equal(trimmed, doneMarker) {
		return true
	}
	// Also check for "data: [DONE]" format
	if bytes.HasPrefix(trimmed, dataTag) {
		data := bytes.TrimSpace(trimmed[len(dataTag):])
		return bytes.Equal(data, doneMarker)
	}
	return false
}

// RunSSEStream processes an SSE stream from the given body using the provided processor.
// It handles buffering, context cancellation, error reporting, and usage tracking.
//
// Parameters:
//   - ctx: Context for cancellation
//   - body: The HTTP response body to read from (will be closed when done)
//   - reporter: Usage reporter for tracking token usage (may be nil)
//   - processor: The StreamProcessor implementation for this provider
//   - cfg: Configuration options for stream processing
//
// Returns a channel that emits StreamChunk values. The channel is closed when
// the stream ends or an error occurs.
func RunSSEStream(
	ctx context.Context,
	body io.ReadCloser,
	reporter *usageReporter,
	processor StreamProcessor,
	cfg StreamConfig,
) <-chan cliproxyexecutor.StreamChunk {
	out := make(chan cliproxyexecutor.StreamChunk)

	go func() {
		defer close(out)
		defer func() {
			if errClose := body.Close(); errClose != nil {
				log.Errorf("%s: close response body error: %v", cfg.ExecutorName, errClose)
			}
		}()

		// Get buffer from pool (reduces allocations in hot path)
		buf := scannerBufferPool.Get().([]byte)
		defer scannerBufferPool.Put(buf)

		// Configure scanner with pooled buffer
		scanner := bufio.NewScanner(body)
		maxBufferSize := cfg.MaxBufferSize
		if maxBufferSize == 0 {
			maxBufferSize = DefaultStreamBufferSize
		}
		scanner.Buffer(buf, maxBufferSize)

		for scanner.Scan() {
			// Check context cancellation before processing each line
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Bytes()

			// Check for done signal
			if isDoneLine(line) {
				if cfg.SkipDoneInData {
					continue
				}
				if cfg.HandleDoneSignal && processor != nil {
					doneChunks, doneErr := processor.ProcessDone()
					if doneErr != nil {
						if reporter != nil {
							reporter.publishFailure(ctx)
						}
						sendChunk(ctx, out, cliproxyexecutor.StreamChunk{Err: doneErr})
						return
					}
					for _, chunk := range doneChunks {
						if !sendChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: chunk}) {
							return
						}
					}
				}
				continue
			}

			// Apply preprocessor if configured
			payload := line
			if cfg.Preprocessor != nil {
				var skip bool
				payload, skip = cfg.Preprocessor(line)
				if skip {
					continue
				}
			}

			// Skip empty lines if configured
			if cfg.SkipEmptyLines && len(bytes.TrimSpace(payload)) == 0 {
				continue
			}

			// Process the line through the StreamProcessor
			chunks, usage, err := processor.ProcessLine(payload)
			if err != nil {
				if reporter != nil {
					reporter.publishFailure(ctx)
				}
				sendChunk(ctx, out, cliproxyexecutor.StreamChunk{Err: err})
				return
			}

			// Publish usage if available
			if usage != nil && reporter != nil {
				reporter.publish(ctx, usage)
			}

			// Send chunks to output
			if len(chunks) > 0 {
				for _, chunk := range chunks {
					if !sendChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: chunk}) {
						return
					}
				}
			} else if cfg.PassthroughOnEmpty {
				// Send raw line as passthrough (use append to avoid make+copy)
				if !sendChunk(ctx, out, cliproxyexecutor.StreamChunk{Payload: append(line, '\n')}) {
					return
				}
			}
		}

		// Handle scanner errors
		if errScan := scanner.Err(); errScan != nil {
			if reporter != nil {
				reporter.publishFailure(ctx)
			}
			sendChunk(ctx, out, cliproxyexecutor.StreamChunk{Err: errScan})
			return
		}

		// Ensure usage is published for successful streams
		if cfg.EnsurePublished && reporter != nil {
			reporter.ensurePublished(ctx)
		}
	}()

	return out
}

// SimpleStreamProcessor is a convenience wrapper for simple processing functions.
// It implements StreamProcessor for cases where ProcessDone is not needed.
type SimpleStreamProcessor struct {
	// ProcessFunc processes a single line and returns chunks and usage.
	ProcessFunc func(line []byte) (chunks [][]byte, usage *ir.Usage, err error)
}

func (p *SimpleStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	if p.ProcessFunc == nil {
		return nil, nil, nil
	}
	return p.ProcessFunc(line)
}

func (p *SimpleStreamProcessor) ProcessDone() ([][]byte, error) {
	return nil, nil
}

// NewSimpleStreamProcessor creates a SimpleStreamProcessor from a processing function.
func NewSimpleStreamProcessor(fn func(line []byte) (chunks [][]byte, usage *ir.Usage, err error)) *SimpleStreamProcessor {
	return &SimpleStreamProcessor{ProcessFunc: fn}
}
