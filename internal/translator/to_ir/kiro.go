package to_ir

import (
	"fmt"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/gjson"
)

func ParseKiroResponse(rawJSON []byte) ([]ir.Message, *ir.Usage, error) {
	parsed, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, nil, err
	}

	// Try finding assistant response in various paths
	var resp gjson.Result
	if r := parsed.Get("conversationState.currentMessage.assistantResponseMessage"); r.Exists() {
		resp = r
	} else if r := parsed.Get("assistantResponseMessage"); r.Exists() {
		resp = r
	} else {
		return nil, nil, nil
	}

	msg := &ir.Message{Role: ir.RoleAssistant}
	if content := resp.Get("content").String(); content != "" {
		msg.Content = append(msg.Content, ir.ContentPart{Type: ir.ContentTypeText, Text: content})
	}

	for _, tool := range resp.Get("toolUsages").Array() {
		msg.ToolCalls = append(msg.ToolCalls, ir.ToolCall{
			ID:   convertToolID(tool.Get("toolUseId").String()),
			Name: tool.Get("name").String(),
			Args: tool.Get("input").String(),
		})
	}

	if len(msg.Content) == 0 && len(msg.ToolCalls) == 0 {
		return nil, nil, nil
	}

	// Parse usage if present
	var usage *ir.Usage
	if u := parsed.Get("usage"); u.Exists() {
		usage = &ir.Usage{
			PromptTokens:     u.Get("inputTokens").Int(),
			CompletionTokens: u.Get("outputTokens").Int(),
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	}

	// Alternative field names (top-level inputTokens/outputTokens)
	if usage == nil {
		if inputTokens := parsed.Get("inputTokens"); inputTokens.Exists() {
			usage = &ir.Usage{
				PromptTokens:     inputTokens.Int(),
				CompletionTokens: parsed.Get("outputTokens").Int(),
			}
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
		}
	}

	return []ir.Message{*msg}, usage, nil
}

type KiroStreamState struct {
	AccumulatedContent string
	ToolCalls          []ir.ToolCall
	CurrentTool        *ir.ToolCall
	CurrentToolInput   string
}

func NewKiroStreamState() *KiroStreamState {
	return &KiroStreamState{ToolCalls: make([]ir.ToolCall, 0)}
}

func (s *KiroStreamState) ProcessChunk(rawJSON []byte) ([]ir.UnifiedEvent, error) {
	if len(rawJSON) == 0 {
		return nil, nil
	}
	parsed, err := ir.ParseAndValidateJSON(rawJSON)
	if err != nil {
		return nil, nil // Ignore invalid chunks in streaming
	}

	// Check for error in response
	if errMsg := parsed.Get("error"); errMsg.Exists() {
		return []ir.UnifiedEvent{{
			Type:  ir.EventTypeError,
			Error: fmt.Errorf("%s", errMsg.Get("message").String()),
		}}, nil
	}

	// Check for error type event
	if parsed.Get("type").String() == "error" {
		return []ir.UnifiedEvent{{
			Type:  ir.EventTypeError,
			Error: fmt.Errorf("%s", parsed.Get("message").String()),
		}}, nil
	}

	// Check for stream finish with done flag or stopReason
	if parsed.Get("done").Bool() || parsed.Get("stopReason").Exists() {
		usage := &ir.Usage{}
		if inputTokens := parsed.Get("inputTokens"); inputTokens.Exists() {
			usage.PromptTokens = inputTokens.Int()
		}
		if outputTokens := parsed.Get("outputTokens"); outputTokens.Exists() {
			usage.CompletionTokens = outputTokens.Int()
		}
		usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens

		return []ir.UnifiedEvent{{
			Type:         ir.EventTypeFinish,
			FinishReason: s.DetermineFinishReason(),
			Usage:        usage,
		}}, nil
	}

	// Handle structured tool call event (incremental)
	if parsed.Get("toolUseId").Exists() && parsed.Get("name").Exists() {
		return s.processToolEvent(parsed), nil
	}

	// Handle regular events (content or completed tool usages)
	return s.processRegularEvents(parsed), nil
}

func (s *KiroStreamState) processToolEvent(parsed gjson.Result) []ir.UnifiedEvent {
	id := convertToolID(parsed.Get("toolUseId").String())
	if s.CurrentTool == nil || s.CurrentTool.ID != id {
		s.CurrentTool = &ir.ToolCall{ID: id, Name: parsed.Get("name").String()}
		s.CurrentToolInput = ""
	}

	s.CurrentToolInput += parsed.Get("input").String()

	if parsed.Get("stop").Bool() {
		s.CurrentTool.Args = s.CurrentToolInput
		if s.CurrentTool.Args == "" {
			s.CurrentTool.Args = "{}"
		}
		s.ToolCalls = append(s.ToolCalls, *s.CurrentTool)
		event := ir.UnifiedEvent{Type: ir.EventTypeToolCall, ToolCall: s.CurrentTool}
		s.CurrentTool = nil
		s.CurrentToolInput = ""
		return []ir.UnifiedEvent{event}
	}
	return nil
}

func (s *KiroStreamState) processRegularEvents(parsed gjson.Result) []ir.UnifiedEvent {
	var events []ir.UnifiedEvent
	// Unwrap if needed
	data := parsed
	if r := parsed.Get("assistantResponseEvent"); r.Exists() {
		data = r
	}

	if content := data.Get("content").String(); content != "" {
		s.AccumulatedContent += content
		events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToken, Content: content})
	}

	// Handle completed tool usages in array
	for _, tool := range data.Get("toolUsages").Array() {
		tc := ir.ToolCall{
			ID:   convertToolID(tool.Get("toolUseId").String()),
			Name: tool.Get("name").String(),
			Args: tool.Get("input").String(),
		}
		if !s.hasToolCall(tc.ID) {
			s.ToolCalls = append(s.ToolCalls, tc)
			events = append(events, ir.UnifiedEvent{Type: ir.EventTypeToolCall, ToolCall: &tc})
		}
	}
	return events
}

func (s *KiroStreamState) hasToolCall(id string) bool {
	for _, tc := range s.ToolCalls {
		if tc.ID == id {
			return true
		}
	}
	return false
}

func (s *KiroStreamState) DetermineFinishReason() ir.FinishReason {
	if len(s.ToolCalls) > 0 {
		return ir.FinishReasonToolCalls
	}
	return ir.FinishReasonStop
}

func (s *KiroStreamState) Finalize() *ir.Message {
	msg := &ir.Message{Role: ir.RoleAssistant}

	if s.AccumulatedContent != "" {
		msg.Content = append(msg.Content, ir.ContentPart{
			Type: ir.ContentTypeText,
			Text: s.AccumulatedContent,
		})
	}

	msg.ToolCalls = s.ToolCalls
	return msg
}

func convertToolID(id string) string {
	return ir.FromKiroToolID(id)
}
