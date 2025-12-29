package executor

import (
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/gjson"
)

// StreamContext holds all streaming state - single source of truth
// Replaces: GeminiCLIStreamState, StreamState
type StreamContext struct {
	// Claude format state (embed, don't duplicate)
	ClaudeState *from_ir.ClaudeStreamState

	// Tool call tracking
	ToolCallIndex int
	HasToolCalls  bool

	// Finish event tracking - prevents duplicates
	FinishSent bool

	// Reasoning token estimation
	ReasoningCharsAccum int

	// Tool schema for parameter normalization (Antigravity workaround)
	ToolSchemaCtx *ir.ToolSchemaContext

	// Estimated input tokens (for Claude format responses)
	EstimatedInputTokens int64

	// Gemini format buffering (for delay-1-chunk strategy)
	PendingGeminiChunk []byte
	PendingFinishEvent *ir.UnifiedEvent
}

func NewStreamContext() *StreamContext {
	return &StreamContext{
		ClaudeState: from_ir.NewClaudeStreamState(),
	}
}

func NewStreamContextWithTools(originalRequest []byte) *StreamContext {
	ctx := NewStreamContext()
	// Extract tool schemas from original request for parameter normalization
	// Antigravity has issue where Gemini ignores tool parameter schemas
	if len(originalRequest) > 0 {
		tools := gjson.GetBytes(originalRequest, "tools").Array()
		if len(tools) > 0 {
			ctx.ToolSchemaCtx = ir.NewToolSchemaContextFromGJSON(tools)
		}
	}
	return ctx
}

// MarkFinishSent marks finish as sent, returns false if already sent (idempotent)
func (s *StreamContext) MarkFinishSent() bool {
	if s.FinishSent {
		return false
	}
	s.FinishSent = true
	return true
}

// AccumulateReasoning adds reasoning characters for token estimation
func (s *StreamContext) AccumulateReasoning(text string) {
	s.ReasoningCharsAccum += len(text)
}

// EstimateReasoningTokens returns estimated reasoning tokens (chars/3)
func (s *StreamContext) EstimateReasoningTokens() int32 {
	return int32(s.ReasoningCharsAccum / 3)
}
