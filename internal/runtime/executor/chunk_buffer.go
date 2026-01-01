package executor

import (
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/sjson"
)

type ChunkBufferStrategy interface {
	Process(chunk []byte, finishEvent *ir.UnifiedEvent) [][]byte
	Flush() [][]byte
	IsFinished() bool
}

type MergeFinishFunc func(chunk []byte, finish *ir.UnifiedEvent) ([]byte, error)

type PassthroughBuffer struct {
	finishSent bool
}

type DelayOneBuffer struct {
	pending    []byte
	pendingFin *ir.UnifiedEvent
	finishSent bool
	mergeFn    MergeFinishFunc
}

func NewPassthroughBuffer() *PassthroughBuffer {
	return &PassthroughBuffer{}
}

func NewDelayOneBuffer(mergeFn MergeFinishFunc) *DelayOneBuffer {
	return &DelayOneBuffer{
		mergeFn: mergeFn,
	}
}

func NewGeminiDelayBuffer() *DelayOneBuffer {
	return NewDelayOneBuffer(mergeGeminiFinishChunk)
}

func (p *PassthroughBuffer) Process(chunk []byte, finishEvent *ir.UnifiedEvent) [][]byte {
	if p.finishSent {
		return nil
	}

	if finishEvent != nil {
		p.finishSent = true
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

func (p *PassthroughBuffer) Flush() [][]byte {
	return nil
}

func (p *PassthroughBuffer) IsFinished() bool {
	return p.finishSent
}

func (d *DelayOneBuffer) Process(chunk []byte, finishEvent *ir.UnifiedEvent) [][]byte {
	if d.finishSent {
		return nil
	}

	var chunks [][]byte

	if finishEvent != nil {
		d.pendingFin = finishEvent
		if len(d.pending) > 0 {
			merged, err := d.mergeFn(d.pending, finishEvent)
			if err == nil {
				chunks = append(chunks, merged)
			}
			d.pending = nil
			d.pendingFin = nil
			d.finishSent = true
		} else {
			d.finishSent = true
		}
		return chunks
	}

	if chunk != nil {
		if len(d.pending) > 0 {
			chunks = append(chunks, d.pending)
		}
		d.pending = chunk
	}

	return chunks
}

func (d *DelayOneBuffer) Flush() [][]byte {
	if d.finishSent || len(d.pending) == 0 {
		return nil
	}

	chunk := d.pending
	d.pending = nil

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

func (d *DelayOneBuffer) IsFinished() bool {
	return d.finishSent
}

func mergeGeminiFinishChunk(chunk []byte, finishEvent *ir.UnifiedEvent) ([]byte, error) {
	if len(chunk) > 0 && chunk[len(chunk)-1] == '\n' {
		chunk = chunk[:len(chunk)-1]
	}

	finishReason := mapFinishReasonToGemini(finishEvent.FinishReason)

	result, err := sjson.SetBytes(chunk, "candidates.0.finishReason", finishReason)
	if err != nil {
		return nil, err
	}

	if finishEvent.Usage != nil {
		usageMetadata := map[string]any{
			"promptTokenCount":     finishEvent.Usage.PromptTokens,
			"candidatesTokenCount": finishEvent.Usage.CompletionTokens,
			"totalTokenCount":      finishEvent.Usage.TotalTokens,
		}
		if finishEvent.Usage.ThoughtsTokenCount > 0 {
			usageMetadata["thoughtsTokenCount"] = finishEvent.Usage.ThoughtsTokenCount
		}
		if finishEvent.Usage.PromptTokensDetails != nil && finishEvent.Usage.PromptTokensDetails.CachedTokens > 0 {
			usageMetadata["cachedContentTokenCount"] = finishEvent.Usage.PromptTokensDetails.CachedTokens
		}
		result, err = sjson.SetBytes(result, "usageMetadata", usageMetadata)
		if err != nil {
			return nil, err
		}
	}

	return append(result, '\n'), nil
}

func mapFinishReasonToGemini(reason ir.FinishReason) string {
	switch reason {
	case ir.FinishReasonStop, ir.FinishReasonStopSequence:
		return "STOP"
	case ir.FinishReasonMaxTokens:
		return "MAX_TOKENS"
	case ir.FinishReasonToolCalls:
		return "STOP"
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
