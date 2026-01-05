package executor

import (
	"testing"

	"github.com/nghyane/llm-mux/internal/translator/ir"
)

func TestPassthroughEventBuffer_Process(t *testing.T) {
	buf := NewPassthroughEventBuffer()

	events := []*ir.UnifiedEvent{
		{Type: ir.EventTypeToken, Content: "Hello"},
		{Type: ir.EventTypeToken, Content: " world"},
		{Type: ir.EventTypeFinish, FinishReason: ir.FinishReasonStop},
	}

	var emitted []*ir.UnifiedEvent
	for _, ev := range events {
		emitted = append(emitted, buf.Process(ev)...)
	}
	emitted = append(emitted, buf.Flush()...)

	if len(emitted) != 3 {
		t.Errorf("expected 3 events, got %d", len(emitted))
	}
	for i, ev := range emitted {
		if ev != events[i] {
			t.Errorf("event %d mismatch", i)
		}
	}
}

func TestPassthroughEventBuffer_FlushReturnsNil(t *testing.T) {
	buf := NewPassthroughEventBuffer()

	buf.Process(&ir.UnifiedEvent{Type: ir.EventTypeToken, Content: "test"})

	flushed := buf.Flush()
	if flushed != nil {
		t.Error("passthrough buffer flush should return nil")
	}
}
