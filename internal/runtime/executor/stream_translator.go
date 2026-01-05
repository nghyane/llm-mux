package executor

import (
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// StreamTranslator handles format conversion with integrated buffering
type StreamTranslator struct {
	cfg            *config.Config
	from           provider.Format
	to             string
	model          string
	messageID      string
	ctx            *StreamContext
	eventBuffer    EventBufferStrategy
	chunkBuffer    ChunkBufferStrategy
	streamMetaSent bool
}

func NewStreamTranslator(cfg *config.Config, from provider.Format, to, model, messageID string, ctx *StreamContext) *StreamTranslator {
	st := &StreamTranslator{
		cfg:       cfg,
		from:      from,
		to:        to,
		model:     model,
		messageID: messageID,
		ctx:       ctx,
	}

	if to == "gemini" || to == "gemini-cli" {
		st.eventBuffer = NewPassthroughEventBuffer()
		st.chunkBuffer = NewGeminiDelayBuffer()
	} else {
		st.eventBuffer = NewPassthroughEventBuffer()
		st.chunkBuffer = NewPassthroughBuffer()
	}

	return st
}

// Translate converts IR events to target format with buffering
func (t *StreamTranslator) Translate(events []ir.UnifiedEvent) (*StreamTranslationResult, error) {
	var allChunks [][]byte

	if !t.streamMetaSent && len(events) > 0 {
		t.streamMetaSent = true
		metaEvent := ir.UnifiedEvent{
			Type: ir.EventTypeStreamMeta,
			StreamMeta: &ir.StreamMeta{
				MessageID:            t.messageID,
				Model:                t.model,
				EstimatedInputTokens: t.ctx.EstimatedInputTokens,
			},
		}
		if chunk, err := t.convertEvent(&metaEvent); err != nil {
			return nil, err
		} else if chunk != nil {
			allChunks = append(allChunks, chunk)
		}
	}

	for i := range events {
		event := &events[i]

		if t.preprocess(event) {
			continue
		}

		bufferedEvents := t.eventBuffer.Process(event)
		for _, ev := range bufferedEvents {
			chunks, err := t.convertAndBuffer(ev)
			if err != nil {
				return nil, err
			}
			allChunks = append(allChunks, chunks...)
		}
	}

	usage := extractUsageFromEvents(events)

	return &StreamTranslationResult{
		Chunks: allChunks,
		Usage:  usage,
	}, nil
}

func (t *StreamTranslator) convertAndBuffer(event *ir.UnifiedEvent) ([][]byte, error) {
	chunk, err := t.convertEvent(event)
	if err != nil {
		return nil, err
	}

	if chunk != nil || event.Type == ir.EventTypeFinish {
		var finishEvent *ir.UnifiedEvent
		if event.Type == ir.EventTypeFinish {
			finishEvent = event
		}
		return t.chunkBuffer.Process(chunk, finishEvent), nil
	}

	return nil, nil
}

func (t *StreamTranslator) Flush() ([][]byte, error) {
	var allChunks [][]byte

	// Finalize Claude parser state (embedded in ClaudeState)
	if t.ctx.ClaudeState != nil && t.ctx.ClaudeState.ParserState != nil {
		if finalEvent := t.ctx.ClaudeState.ParserState.Finalize(); finalEvent != nil {
			chunks, err := t.convertAndBuffer(finalEvent)
			if err != nil {
				return nil, err
			}
			allChunks = append(allChunks, chunks...)
		}
	}

	if t.ctx.GeminiState != nil {
		if finalEvent := t.ctx.GeminiState.Finalize(); finalEvent != nil {
			chunks, err := t.convertAndBuffer(finalEvent)
			if err != nil {
				return nil, err
			}
			allChunks = append(allChunks, chunks...)
		}
	}

	flushedEvents := t.eventBuffer.Flush()
	for _, ev := range flushedEvents {
		chunks, err := t.convertAndBuffer(ev)
		if err != nil {
			return nil, err
		}
		allChunks = append(allChunks, chunks...)
	}

	allChunks = append(allChunks, t.chunkBuffer.Flush()...)
	return allChunks, nil
}

// preprocess handles state tracking (tool calls, reasoning, finish dedup)
func (t *StreamTranslator) preprocess(event *ir.UnifiedEvent) bool {
	// Track tool calls - mark HasToolCalls but don't increment index yet
	// Index increment happens in convertEvent to maintain correct 0-based indexing
	if event.Type == ir.EventTypeToolCall {
		t.ctx.HasToolCalls = true
	}

	// Track reasoning content for token estimation
	if event.Type == ir.EventTypeReasoning && event.Reasoning != "" {
		t.ctx.AccumulateReasoning(event.Reasoning)
	}
	if event.Type == ir.EventTypeReasoningSummary && event.ReasoningSummary != "" {
		t.ctx.AccumulateReasoning(event.ReasoningSummary)
	}

	// Handle finish event with deduplication and token estimation
	if event.Type == ir.EventTypeFinish {
		if !t.ctx.MarkFinishSent() {
			return true // skip duplicate finish
		}

		// Override finish_reason if tool calls were seen
		if t.ctx.HasToolCalls {
			event.FinishReason = ir.FinishReasonToolCalls
		}

		// Estimate reasoning tokens if provider didn't provide them
		if t.ctx.ReasoningCharsAccum > 0 {
			if event.Usage == nil {
				event.Usage = &ir.Usage{}
			}
			if event.Usage.ThoughtsTokenCount == 0 {
				event.Usage.ThoughtsTokenCount = t.ctx.EstimateReasoningTokens()
			}
		}
	}

	return false // don't skip
}

// convertEvent converts single event to target format
func (t *StreamTranslator) convertEvent(event *ir.UnifiedEvent) ([]byte, error) {
	switch t.to {
	case "openai", "cline":
		idx := 0
		if event.Type == ir.EventTypeToolCall {
			idx = t.ctx.ToolCallIndex
			t.ctx.ToolCallIndex++ // Increment AFTER getting current index
		} else if event.Type == ir.EventTypeToolCallDelta {
			// For deltas, use PREVIOUS index (the tool call we're continuing)
			if t.ctx.ToolCallIndex > 0 {
				idx = t.ctx.ToolCallIndex - 1
			}
		}
		return from_ir.ToOpenAIChunk(*event, t.model, t.messageID, idx)
	case "claude":
		return from_ir.ToClaudeSSE(*event, t.ctx.ClaudeState)
	case "gemini", "gemini-cli":
		return from_ir.ToGeminiChunk(*event, t.model)
	case "ollama":
		return from_ir.ToOllamaChatChunk(*event, t.model)
	default:
		return nil, nil // unsupported format
	}
}
