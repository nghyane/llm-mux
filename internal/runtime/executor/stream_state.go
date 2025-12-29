package executor

import (
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/gjson"
)

type StreamContext struct {
	ClaudeState          *from_ir.ClaudeStreamState
	ToolCallIndex        int
	HasToolCalls         bool
	FinishSent           bool
	ReasoningCharsAccum  int
	ToolSchemaCtx        *ir.ToolSchemaContext
	EstimatedInputTokens int64
}

func NewStreamContext() *StreamContext {
	return &StreamContext{
		ClaudeState: from_ir.NewClaudeStreamState(),
	}
}

func NewStreamContextWithTools(originalRequest []byte) *StreamContext {
	ctx := NewStreamContext()
	if len(originalRequest) > 0 {
		tools := gjson.GetBytes(originalRequest, "tools").Array()
		if len(tools) > 0 {
			ctx.ToolSchemaCtx = ir.NewToolSchemaContextFromGJSON(tools)
		}
	}
	return ctx
}

func (s *StreamContext) MarkFinishSent() bool {
	if s.FinishSent {
		return false
	}
	s.FinishSent = true
	return true
}

func (s *StreamContext) AccumulateReasoning(text string) {
	s.ReasoningCharsAccum += len(text)
}

func (s *StreamContext) EstimateReasoningTokens() int32 {
	return int32(s.ReasoningCharsAccum / 3)
}
