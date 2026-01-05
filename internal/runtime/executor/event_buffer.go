package executor

import "github.com/nghyane/llm-mux/internal/translator/ir"

type EventBufferStrategy interface {
	Process(event *ir.UnifiedEvent) []*ir.UnifiedEvent
	Flush() []*ir.UnifiedEvent
}

type PassthroughEventBuffer struct{}

func NewPassthroughEventBuffer() *PassthroughEventBuffer {
	return &PassthroughEventBuffer{}
}

func (b *PassthroughEventBuffer) Process(event *ir.UnifiedEvent) []*ir.UnifiedEvent {
	return []*ir.UnifiedEvent{event}
}

func (b *PassthroughEventBuffer) Flush() []*ir.UnifiedEvent {
	return nil
}
