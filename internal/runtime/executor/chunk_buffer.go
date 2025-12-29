package executor

import (
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/sjson"
)

// ChunkBufferStrategy defines buffering behavior for SSE chunks
// Implementations control when chunks are emitted vs held
type ChunkBufferStrategy interface {
	// Process receives a chunk and finish event, returns chunks to emit now
	// chunk: the converted SSE chunk (nil if finish-only)
	// finishEvent: the finish event if this is a finish (nil otherwise)
	Process(chunk []byte, finishEvent *ir.UnifiedEvent) [][]byte

	// Flush returns any buffered chunks on stream end (EOF or [DONE])
	Flush() [][]byte

	// IsFinished returns true if finish was already emitted
	IsFinished() bool
}

// MergeFinishFunc merges finish info into a content chunk
type MergeFinishFunc func(chunk []byte, finish *ir.UnifiedEvent) ([]byte, error)

// PassthroughBuffer emits chunks immediately without buffering
// Used for: OpenAI, Claude, Ollama formats
type PassthroughBuffer struct {
	finishSent bool
}

// DelayOneBuffer holds 1 chunk to merge finish info into content
// Used for: Gemini format with Claude models (SDK rejects finish-only chunks)
// Strategy:
//   - Hold chunk N
//   - When chunk N+1 arrives, emit N, hold N+1
//   - When finish arrives, merge into held chunk and emit
type DelayOneBuffer struct {
	pending    []byte
	pendingFin *ir.UnifiedEvent
	finishSent bool
	mergeFn    MergeFinishFunc
}

// NewPassthroughBuffer creates a buffer that emits chunks immediately
func NewPassthroughBuffer() *PassthroughBuffer {
	return &PassthroughBuffer{}
}

// NewDelayOneBuffer creates a buffer that holds one chunk to merge finish info
func NewDelayOneBuffer(mergeFn MergeFinishFunc) *DelayOneBuffer {
	return &DelayOneBuffer{
		mergeFn: mergeFn,
	}
}

// NewGeminiDelayBuffer creates a DelayOneBuffer configured for Gemini format
func NewGeminiDelayBuffer() *DelayOneBuffer {
	return NewDelayOneBuffer(mergeGeminiFinishChunk)
}

// Process implements ChunkBufferStrategy for PassthroughBuffer
func (p *PassthroughBuffer) Process(chunk []byte, finishEvent *ir.UnifiedEvent) [][]byte {
	if p.finishSent {
		return nil
	}

	if finishEvent != nil {
		p.finishSent = true
		// For passthrough, emit finish-only chunks as-is if they exist
		if chunk != nil {
			return [][]byte{chunk}
		}
		return nil
	}

	if chunk != nil {
		return [][]byte{chunk}
	}

	return nil
}

// Flush implements ChunkBufferStrategy for PassthroughBuffer
func (p *PassthroughBuffer) Flush() [][]byte {
	return nil
}

// IsFinished implements ChunkBufferStrategy for PassthroughBuffer
func (p *PassthroughBuffer) IsFinished() bool {
	return p.finishSent
}

// Process implements ChunkBufferStrategy for DelayOneBuffer
func (d *DelayOneBuffer) Process(chunk []byte, finishEvent *ir.UnifiedEvent) [][]byte {
	if d.finishSent {
		return nil
	}

	var chunks [][]byte

	// Handle finish event
	if finishEvent != nil {
		d.pendingFin = finishEvent
		// If we have a pending chunk, merge finish into it
		if len(d.pending) > 0 {
			merged, err := d.mergeFn(d.pending, finishEvent)
			if err == nil {
				chunks = append(chunks, merged)
			}
			d.pending = nil
			d.pendingFin = nil
			d.finishSent = true
		} else {
			// No pending content, finish-only chunk - mark as sent
			d.finishSent = true
		}
		return chunks
	}

	// Handle content chunk
	if chunk != nil {
		// If we have a pending chunk, emit it now
		if len(d.pending) > 0 {
			chunks = append(chunks, d.pending)
		}
		// Hold current chunk as pending
		d.pending = chunk
	}

	return chunks
}

// Flush implements ChunkBufferStrategy for DelayOneBuffer
func (d *DelayOneBuffer) Flush() [][]byte {
	if d.finishSent || len(d.pending) == 0 {
		return nil
	}

	chunk := d.pending
	d.pending = nil

	// If we have a pending finish event, merge it
	if d.pendingFin != nil {
		merged, err := d.mergeFn(chunk, d.pendingFin)
		d.pendingFin = nil
		d.finishSent = true
		if err == nil {
			return [][]byte{merged}
		}
	}

	return [][]byte{chunk}
}

// IsFinished implements ChunkBufferStrategy for DelayOneBuffer
func (d *DelayOneBuffer) IsFinished() bool {
	return d.finishSent
}

// mergeGeminiFinishChunk adds finishReason and usage to an existing Gemini chunk.
// Copied from translator_wrapper.go for the buffering strategy implementation.
func mergeGeminiFinishChunk(chunk []byte, finishEvent *ir.UnifiedEvent) ([]byte, error) {
	// Remove trailing newline if present
	if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		chunk = chunk[:len(chunk)-1]
	}

	// Map IR finish reason to Gemini format
	finishReason := mapFinishReasonToGemini(finishEvent.FinishReason)

	// Add finishReason to candidate
	result, err := sjson.SetBytes(chunk, "candidates.0.finishReason", finishReason)
	if err != nil {
		return nil, err
	}

	// Add usage metadata if present
	if finishEvent.Usage != nil {
		usageMetadata := map[string]any{
			"promptTokenCount":     finishEvent.Usage.PromptTokens,
			"candidatesTokenCount": finishEvent.Usage.CompletionTokens,
			"totalTokenCount":      finishEvent.Usage.TotalTokens,
		}
		if finishEvent.Usage.ThoughtsTokenCount > 0 {
			usageMetadata["thoughtsTokenCount"] = finishEvent.Usage.ThoughtsTokenCount
		}
		result, err = sjson.SetBytes(result, "usageMetadata", usageMetadata)
		if err != nil {
			return nil, err
		}
	}

	// Add trailing newline back
	return append(result, '\n'), nil
}

// mapFinishReasonToGemini converts IR finish reason to Gemini API format.
func mapFinishReasonToGemini(reason ir.FinishReason) string {
	switch reason {
	case ir.FinishReasonStop, ir.FinishReasonStopSequence:
		return "STOP"
	case ir.FinishReasonMaxTokens:
		return "MAX_TOKENS"
	case ir.FinishReasonToolCalls:
		return "STOP" // Gemini uses STOP for tool calls too
	case ir.FinishReasonContentFilter:
		return "SAFETY"
	case ir.FinishReasonRecitation:
		return "RECITATION"
	case ir.FinishReasonBlocklist:
		return "BLOCKLIST"
	case ir.FinishReasonProhibitedContent:
		return "PROHIBITED_CONTENT"
	case ir.FinishReasonSPII:
		return "SPII"
	default:
		return "STOP"
	}
}
