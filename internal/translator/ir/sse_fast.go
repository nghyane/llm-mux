// Package ir provides optimized SSE event generation for streaming responses.
// This file contains zero-allocation or low-allocation implementations for hot paths.
package ir

import (
	"strconv"
	"sync"

	"github.com/nghyane/llm-mux/internal/json"
)

// -----------------------------------------------------------------------------
// OpenAI Chat Completion Chunk - Typed Structs for Fast Marshaling
// -----------------------------------------------------------------------------

// OpenAITextDelta represents a simple text delta event (most common case).
// Using typed struct instead of map[string]any reduces allocations by ~3x.
type OpenAITextDelta struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Model   string                  `json:"model"`
	Choices []OpenAITextDeltaChoice `json:"choices"`
}

type OpenAITextDeltaChoice struct {
	Index int                    `json:"index"`
	Delta OpenAITextDeltaContent `json:"delta"`
}

type OpenAITextDeltaContent struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// Pool for OpenAI text delta events
var openaiTextDeltaPool = sync.Pool{
	New: func() any {
		return &OpenAITextDelta{
			Object:  "chat.completion.chunk",
			Choices: make([]OpenAITextDeltaChoice, 1),
		}
	},
}

// GetOpenAITextDelta retrieves a pre-allocated OpenAI text delta from the pool.
func GetOpenAITextDelta() *OpenAITextDelta {
	return openaiTextDeltaPool.Get().(*OpenAITextDelta)
}

// PutOpenAITextDelta returns an OpenAI text delta to the pool.
func PutOpenAITextDelta(d *OpenAITextDelta) {
	// Reset fields
	d.ID = ""
	d.Model = ""
	d.Created = 0
	if len(d.Choices) > 0 {
		d.Choices[0].Index = 0
		d.Choices[0].Delta.Role = ""
		d.Choices[0].Delta.Content = ""
	}
	openaiTextDeltaPool.Put(d)
}

// BuildOpenAITextDeltaSSE builds an SSE chunk for a simple text delta.
// This is the HOT PATH - called for every token in streaming.
func BuildOpenAITextDeltaSSE(id, model string, created int64, content string) []byte {
	delta := GetOpenAITextDelta()
	defer PutOpenAITextDelta(delta)

	delta.ID = id
	delta.Model = model
	delta.Created = created
	delta.Choices[0].Delta.Role = "assistant"
	delta.Choices[0].Delta.Content = content

	jb, _ := json.Marshal(delta)
	return BuildSSEChunk(jb)
}

// -----------------------------------------------------------------------------
// OpenAI Reasoning Delta - Typed Struct
// -----------------------------------------------------------------------------

type OpenAIReasoningDelta struct {
	ID      string                       `json:"id"`
	Object  string                       `json:"object"`
	Created int64                        `json:"created"`
	Model   string                       `json:"model"`
	Choices []OpenAIReasoningDeltaChoice `json:"choices"`
}

type OpenAIReasoningDeltaChoice struct {
	Index int                         `json:"index"`
	Delta OpenAIReasoningDeltaContent `json:"delta"`
}

type OpenAIReasoningDeltaContent struct {
	Role      string                `json:"role,omitempty"`
	Reasoning *OpenAIReasoningBlock `json:"reasoning,omitempty"`
}

type OpenAIReasoningBlock struct {
	Content   string `json:"content,omitempty"`
	Signature string `json:"signature,omitempty"`
}

var openaiReasoningDeltaPool = sync.Pool{
	New: func() any {
		return &OpenAIReasoningDelta{
			Object:  "chat.completion.chunk",
			Choices: make([]OpenAIReasoningDeltaChoice, 1),
		}
	},
}

func GetOpenAIReasoningDelta() *OpenAIReasoningDelta {
	d := openaiReasoningDeltaPool.Get().(*OpenAIReasoningDelta)
	if d.Choices[0].Delta.Reasoning == nil {
		d.Choices[0].Delta.Reasoning = &OpenAIReasoningBlock{}
	}
	return d
}

func PutOpenAIReasoningDelta(d *OpenAIReasoningDelta) {
	d.ID = ""
	d.Model = ""
	d.Created = 0
	if len(d.Choices) > 0 {
		d.Choices[0].Index = 0
		d.Choices[0].Delta.Role = ""
		if d.Choices[0].Delta.Reasoning != nil {
			d.Choices[0].Delta.Reasoning.Content = ""
			d.Choices[0].Delta.Reasoning.Signature = ""
		}
	}
	openaiReasoningDeltaPool.Put(d)
}

func BuildOpenAIReasoningDeltaSSE(id, model string, created int64, reasoning, signature string) []byte {
	delta := GetOpenAIReasoningDelta()
	defer PutOpenAIReasoningDelta(delta)

	delta.ID = id
	delta.Model = model
	delta.Created = created
	delta.Choices[0].Delta.Role = "assistant"
	delta.Choices[0].Delta.Reasoning.Content = reasoning
	delta.Choices[0].Delta.Reasoning.Signature = signature

	jb, _ := json.Marshal(delta)
	return BuildSSEChunk(jb)
}

// -----------------------------------------------------------------------------
// OpenAI Responses API - Typed Structs for Fast Marshaling
// -----------------------------------------------------------------------------

// ResponsesTextDelta represents a text delta in Responses API format.
type ResponsesTextDelta struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Delta          string `json:"delta"`
}

var responsesTextDeltaPool = sync.Pool{
	New: func() any {
		return &ResponsesTextDelta{
			Type: "response.output_text.delta",
		}
	},
}

func GetResponsesTextDelta() *ResponsesTextDelta {
	return responsesTextDeltaPool.Get().(*ResponsesTextDelta)
}

func PutResponsesTextDelta(d *ResponsesTextDelta) {
	d.SequenceNumber = 0
	d.ItemID = ""
	d.OutputIndex = 0
	d.ContentIndex = 0
	d.Delta = ""
	responsesTextDeltaPool.Put(d)
}

// BuildResponsesTextDeltaSSE builds an SSE event for Responses API text delta.
// Returns []byte for consistency with other Build*SSE functions and zero-copy writes.
func BuildResponsesTextDeltaSSE(seqNum int, itemID string, delta string) []byte {
	d := GetResponsesTextDelta()
	defer PutResponsesTextDelta(d)

	d.SequenceNumber = seqNum
	d.ItemID = itemID
	d.Delta = delta

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.output_text.delta", jb)
}

// ResponsesReasoningDelta represents a reasoning delta in Responses API format.
type ResponsesReasoningDelta struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	ContentIndex   int    `json:"content_index"`
	Delta          string `json:"delta"`
}

var responsesReasoningDeltaPool = sync.Pool{
	New: func() any {
		return &ResponsesReasoningDelta{
			Type: "response.reasoning_summary_text.delta",
		}
	},
}

func GetResponsesReasoningDelta() *ResponsesReasoningDelta {
	return responsesReasoningDeltaPool.Get().(*ResponsesReasoningDelta)
}

func PutResponsesReasoningDelta(d *ResponsesReasoningDelta) {
	d.SequenceNumber = 0
	d.ItemID = ""
	d.OutputIndex = 0
	d.ContentIndex = 0
	d.Delta = ""
	responsesReasoningDeltaPool.Put(d)
}

// BuildResponsesReasoningDeltaSSE builds an SSE event for Responses API reasoning delta.
// Returns []byte for consistency with other Build*SSE functions and zero-copy writes.
func BuildResponsesReasoningDeltaSSE(seqNum int, itemID string, delta string) []byte {
	d := GetResponsesReasoningDelta()
	defer PutResponsesReasoningDelta(d)

	d.SequenceNumber = seqNum
	d.ItemID = itemID
	d.Delta = delta

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.reasoning_summary_text.delta", jb)
}

// -----------------------------------------------------------------------------
// Claude API - Typed Structs for Fast Marshaling
// -----------------------------------------------------------------------------

// ClaudeTextDelta represents a text delta in Claude format.
type ClaudeTextDelta struct {
	Type  string               `json:"type"`
	Index int                  `json:"index"`
	Delta ClaudeTextDeltaInner `json:"delta"`
}

type ClaudeTextDeltaInner struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var claudeTextDeltaPool = sync.Pool{
	New: func() any {
		return &ClaudeTextDelta{
			Type: ClaudeSSEContentBlockDelta,
			Delta: ClaudeTextDeltaInner{
				Type: "text_delta",
			},
		}
	},
}

func GetClaudeTextDelta() *ClaudeTextDelta {
	return claudeTextDeltaPool.Get().(*ClaudeTextDelta)
}

func PutClaudeTextDelta(d *ClaudeTextDelta) {
	d.Index = 0
	d.Delta.Text = ""
	claudeTextDeltaPool.Put(d)
}

// BuildClaudeTextDeltaSSE builds an SSE chunk for Claude text delta.
func BuildClaudeTextDeltaSSE(index int, text string) []byte {
	d := GetClaudeTextDelta()
	defer PutClaudeTextDelta(d)

	d.Index = index
	d.Delta.Text = text

	jb, _ := json.Marshal(d)
	return BuildSSEEvent(ClaudeSSEContentBlockDelta, jb)
}

// ClaudeThinkingDelta represents a thinking delta in Claude format.
type ClaudeThinkingDelta struct {
	Type  string                   `json:"type"`
	Index int                      `json:"index"`
	Delta ClaudeThinkingDeltaInner `json:"delta"`
}

type ClaudeThinkingDeltaInner struct {
	Type     string `json:"type"`
	Thinking string `json:"thinking,omitempty"`
}

var claudeThinkingDeltaPool = sync.Pool{
	New: func() any {
		return &ClaudeThinkingDelta{
			Type: ClaudeSSEContentBlockDelta,
			Delta: ClaudeThinkingDeltaInner{
				Type: "thinking_delta",
			},
		}
	},
}

func GetClaudeThinkingDelta() *ClaudeThinkingDelta {
	return claudeThinkingDeltaPool.Get().(*ClaudeThinkingDelta)
}

func PutClaudeThinkingDelta(d *ClaudeThinkingDelta) {
	d.Index = 0
	d.Delta.Thinking = ""
	claudeThinkingDeltaPool.Put(d)
}

func BuildClaudeThinkingDeltaSSE(index int, thinking string) []byte {
	d := GetClaudeThinkingDelta()
	defer PutClaudeThinkingDelta(d)

	d.Index = index
	d.Delta.Thinking = thinking

	jb, _ := json.Marshal(d)
	return BuildSSEEvent(ClaudeSSEContentBlockDelta, jb)
}

// ClaudeContentBlockStop represents a content block stop event.
type ClaudeContentBlockStop struct {
	Type  string `json:"type"`
	Index int    `json:"index"`
}

var claudeContentBlockStopPool = sync.Pool{
	New: func() any {
		return &ClaudeContentBlockStop{
			Type: ClaudeSSEContentBlockStop,
		}
	},
}

func GetClaudeContentBlockStop() *ClaudeContentBlockStop {
	return claudeContentBlockStopPool.Get().(*ClaudeContentBlockStop)
}

func PutClaudeContentBlockStop(d *ClaudeContentBlockStop) {
	d.Index = 0
	claudeContentBlockStopPool.Put(d)
}

func BuildClaudeContentBlockStopSSE(index int) []byte {
	d := GetClaudeContentBlockStop()
	defer PutClaudeContentBlockStop(d)

	d.Index = index

	jb, _ := json.Marshal(d)
	return BuildSSEEvent(ClaudeSSEContentBlockStop, jb)
}

// -----------------------------------------------------------------------------
// Zero-Marshal Template Builders (Ultimate Fast Path)
// -----------------------------------------------------------------------------

// BuildOpenAITextDeltaSSETemplate builds an SSE chunk using string concatenation
// instead of JSON marshaling. ~5x faster for simple text deltas.
// Note: content must be pre-escaped JSON string.
func BuildOpenAITextDeltaSSETemplate(id, model string, created int64, contentJSON []byte) []byte {
	buf := GetBuffer()
	defer PutBuffer(buf)

	// Pre-calculate approximate size
	buf.Grow(150 + len(id) + len(model) + len(contentJSON))

	buf.WriteString(`data: {"id":"`)
	buf.WriteString(id)
	buf.WriteString(`","object":"chat.completion.chunk","created":`)
	buf.WriteString(strconv.FormatInt(created, 10))
	buf.WriteString(`,"model":"`)
	buf.WriteString(model)
	buf.WriteString(`","choices":[{"index":0,"delta":{"role":"assistant","content":`)
	buf.Write(contentJSON)
	buf.WriteString(`}}]}`)
	buf.WriteString("\n\n")

	// Clone the result since buffer will be reused
	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result
}

// BuildClaudeTextDeltaSSETemplate builds Claude text delta using template.
// Note: text must be pre-escaped JSON string.
func BuildClaudeTextDeltaSSETemplate(index int, textJSON []byte) []byte {
	buf := GetBuffer()
	defer PutBuffer(buf)

	buf.Grow(120 + len(textJSON))

	buf.WriteString(`event: content_block_delta`)
	buf.WriteString("\n")
	buf.WriteString(`data: {"type":"content_block_delta","index":`)
	buf.WriteString(strconv.Itoa(index))
	buf.WriteString(`,"delta":{"type":"text_delta","text":`)
	buf.Write(textJSON)
	buf.WriteString(`}}`)
	buf.WriteString("\n\n")

	result := make([]byte, buf.Len())
	copy(result, buf.Bytes())
	return result
}

// -----------------------------------------------------------------------------
// Helper Functions
// -----------------------------------------------------------------------------

// formatResponsesSSEBytes builds an SSE event returning []byte for zero-copy writes.
// This is the preferred version for hot paths.
func formatResponsesSSEBytes(eventType string, data []byte) []byte {
	size := 7 + len(eventType) + 7 + len(data) + 2
	buf := GetSSEChunkBuf()
	if cap(buf) < size {
		buf = make([]byte, 0, size)
	}
	buf = append(buf, "event: "...)
	buf = append(buf, eventType...)
	buf = append(buf, "\ndata: "...)
	buf = append(buf, data...)
	buf = append(buf, "\n\n"...)
	return buf
}

// -----------------------------------------------------------------------------
// OpenAI Tool Call Delta - Typed Struct for Fast Marshaling (HOT PATH)
// Tool calls are frequent in agentic workflows - every tool invocation triggers this.
// -----------------------------------------------------------------------------

// OpenAIToolCallDelta represents a tool call delta event.
// Using typed struct instead of map[string]any reduces allocations by ~3x.
type OpenAIToolCallDelta struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Model   string                      `json:"model"`
	Choices []OpenAIToolCallDeltaChoice `json:"choices"`
}

type OpenAIToolCallDeltaChoice struct {
	Index int                        `json:"index"`
	Delta OpenAIToolCallDeltaContent `json:"delta"`
}

type OpenAIToolCallDeltaContent struct {
	Role      string                `json:"role,omitempty"`
	ToolCalls []OpenAIToolCallEntry `json:"tool_calls,omitempty"`
}

type OpenAIToolCallEntry struct {
	Index        int                         `json:"index"`
	ID           string                      `json:"id,omitempty"`
	Type         string                      `json:"type,omitempty"`
	Function     *OpenAIToolCallFunction     `json:"function,omitempty"`
	ExtraContent *OpenAIToolCallExtraContent `json:"extra_content,omitempty"`
}

type OpenAIToolCallFunction struct {
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
}

type OpenAIToolCallExtraContent struct {
	Google *OpenAIToolCallGoogle `json:"google,omitempty"`
}

type OpenAIToolCallGoogle struct {
	ThoughtSignature string `json:"thought_signature,omitempty"`
}

// Pool for OpenAI tool call delta events
var openaiToolCallDeltaPool = sync.Pool{
	New: func() any {
		return &OpenAIToolCallDelta{
			Object:  "chat.completion.chunk",
			Choices: make([]OpenAIToolCallDeltaChoice, 1),
		}
	},
}

// GetOpenAIToolCallDelta retrieves a pre-allocated OpenAI tool call delta from the pool.
func GetOpenAIToolCallDelta() *OpenAIToolCallDelta {
	d := openaiToolCallDeltaPool.Get().(*OpenAIToolCallDelta)
	// Ensure Choices[0].Delta.ToolCalls has capacity
	if cap(d.Choices[0].Delta.ToolCalls) == 0 {
		d.Choices[0].Delta.ToolCalls = make([]OpenAIToolCallEntry, 0, 1)
	}
	return d
}

// PutOpenAIToolCallDelta returns an OpenAI tool call delta to the pool.
func PutOpenAIToolCallDelta(d *OpenAIToolCallDelta) {
	// Reset fields
	d.ID = ""
	d.Model = ""
	d.Created = 0
	if len(d.Choices) > 0 {
		d.Choices[0].Index = 0
		d.Choices[0].Delta.Role = ""
		d.Choices[0].Delta.ToolCalls = d.Choices[0].Delta.ToolCalls[:0] // Reset slice but keep capacity
	}
	openaiToolCallDeltaPool.Put(d)
}

// BuildOpenAIToolCallDeltaSSE builds an SSE chunk for a tool call delta.
// This is a HOT PATH for agentic workflows - called for every tool call in streaming.
func BuildOpenAIToolCallDeltaSSE(id, model string, created int64, toolCallIndex int, toolCallID, funcName, funcArgs string, thoughtSig []byte) []byte {
	delta := GetOpenAIToolCallDelta()
	defer PutOpenAIToolCallDelta(delta)

	delta.ID = id
	delta.Model = model
	delta.Created = created
	delta.Choices[0].Delta.Role = "assistant"

	// Build tool call entry
	entry := OpenAIToolCallEntry{
		Index: toolCallIndex,
		ID:    toolCallID,
		Type:  "function",
		Function: &OpenAIToolCallFunction{
			Name:      funcName,
			Arguments: funcArgs,
		},
	}

	// Add thought signature if present (Gemini 3 compatibility)
	if len(thoughtSig) > 0 {
		entry.ExtraContent = &OpenAIToolCallExtraContent{
			Google: &OpenAIToolCallGoogle{
				ThoughtSignature: string(thoughtSig),
			},
		}
	}

	delta.Choices[0].Delta.ToolCalls = append(delta.Choices[0].Delta.ToolCalls, entry)

	jb, _ := json.Marshal(delta)
	return BuildSSEChunk(jb)
}

// BuildOpenAIToolCallArgsDeltaSSE builds an SSE chunk for tool call arguments delta (streaming args).
// Used when only arguments are being streamed (no ID/name).
func BuildOpenAIToolCallArgsDeltaSSE(id, model string, created int64, toolCallIndex int, funcArgs string) []byte {
	delta := GetOpenAIToolCallDelta()
	defer PutOpenAIToolCallDelta(delta)

	delta.ID = id
	delta.Model = model
	delta.Created = created

	entry := OpenAIToolCallEntry{
		Index: toolCallIndex,
		Function: &OpenAIToolCallFunction{
			Arguments: funcArgs,
		},
	}

	delta.Choices[0].Delta.ToolCalls = append(delta.Choices[0].Delta.ToolCalls, entry)

	jb, _ := json.Marshal(delta)
	return BuildSSEChunk(jb)
}

// -----------------------------------------------------------------------------
// Claude Tool Call SSE - Typed Structs for Fast Marshaling (HOT PATH)
// -----------------------------------------------------------------------------

type ClaudeToolCallBlockStart struct {
	Type         string                     `json:"type"`
	Index        int                        `json:"index"`
	ContentBlock ClaudeToolCallContentBlock `json:"content_block"`
}

type ClaudeToolCallContentBlock struct {
	Type  string         `json:"type"`
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

type ClaudeToolCallInputDelta struct {
	Type  string                        `json:"type"`
	Index int                           `json:"index"`
	Delta ClaudeToolCallInputDeltaInner `json:"delta"`
}

type ClaudeToolCallInputDeltaInner struct {
	Type        string `json:"type"`
	PartialJSON string `json:"partial_json"`
}

var emptyInputMap = map[string]any{}

var claudeToolCallBlockStartPool = sync.Pool{
	New: func() any {
		return &ClaudeToolCallBlockStart{
			Type: ClaudeSSEContentBlockStart,
			ContentBlock: ClaudeToolCallContentBlock{
				Type:  ClaudeBlockToolUse,
				Input: emptyInputMap,
			},
		}
	},
}

func GetClaudeToolCallBlockStart() *ClaudeToolCallBlockStart {
	return claudeToolCallBlockStartPool.Get().(*ClaudeToolCallBlockStart)
}

func PutClaudeToolCallBlockStart(d *ClaudeToolCallBlockStart) {
	d.Index = 0
	d.ContentBlock.ID = ""
	d.ContentBlock.Name = ""
	claudeToolCallBlockStartPool.Put(d)
}

var claudeToolCallInputDeltaPool = sync.Pool{
	New: func() any {
		return &ClaudeToolCallInputDelta{
			Type: ClaudeSSEContentBlockDelta,
			Delta: ClaudeToolCallInputDeltaInner{
				Type: "input_json_delta",
			},
		}
	},
}

func GetClaudeToolCallInputDelta() *ClaudeToolCallInputDelta {
	return claudeToolCallInputDeltaPool.Get().(*ClaudeToolCallInputDelta)
}

func PutClaudeToolCallInputDelta(d *ClaudeToolCallInputDelta) {
	d.Index = 0
	d.Delta.PartialJSON = ""
	claudeToolCallInputDeltaPool.Put(d)
}

func BuildClaudeToolCallBlockStartSSE(index int, toolID, name string) []byte {
	d := GetClaudeToolCallBlockStart()
	defer PutClaudeToolCallBlockStart(d)

	d.Index = index
	d.ContentBlock.ID = toolID
	d.ContentBlock.Name = name

	jb, _ := json.Marshal(d)
	return BuildSSEEvent(ClaudeSSEContentBlockStart, jb)
}

func BuildClaudeToolCallInputDeltaSSE(index int, partialJSON string) []byte {
	d := GetClaudeToolCallInputDelta()
	defer PutClaudeToolCallInputDelta(d)

	d.Index = index
	d.Delta.PartialJSON = partialJSON

	jb, _ := json.Marshal(d)
	return BuildSSEEvent(ClaudeSSEContentBlockDelta, jb)
}

// -----------------------------------------------------------------------------
// OpenAI Responses API - Additional Event Types for ToResponsesAPIChunk
// -----------------------------------------------------------------------------

// ResponsesResponseEvent is used for response.created, response.in_progress events.
type ResponsesResponseEvent struct {
	Type           string                      `json:"type"`
	SequenceNumber int                         `json:"sequence_number"`
	Response       ResponsesResponseEventInner `json:"response"`
}

type ResponsesResponseEventInner struct {
	ID        string `json:"id"`
	Object    string `json:"object"`
	CreatedAt int64  `json:"created_at"`
	Status    string `json:"status"`
}

var responsesResponseEventPool = sync.Pool{
	New: func() any {
		return &ResponsesResponseEvent{
			Response: ResponsesResponseEventInner{
				Object: "response",
			},
		}
	},
}

func GetResponsesResponseEvent() *ResponsesResponseEvent {
	return responsesResponseEventPool.Get().(*ResponsesResponseEvent)
}

func PutResponsesResponseEvent(d *ResponsesResponseEvent) {
	d.Type = ""
	d.SequenceNumber = 0
	d.Response.ID = ""
	d.Response.CreatedAt = 0
	d.Response.Status = ""
	responsesResponseEventPool.Put(d)
}

// BuildResponsesResponseEventSSE builds SSE events for response.created, response.in_progress.
func BuildResponsesResponseEventSSE(eventType string, seqNum int, respID string, createdAt int64, status string) []byte {
	d := GetResponsesResponseEvent()
	defer PutResponsesResponseEvent(d)

	d.Type = eventType
	d.SequenceNumber = seqNum
	d.Response.ID = respID
	d.Response.CreatedAt = createdAt
	d.Response.Status = status

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes(eventType, jb)
}

// ResponsesOutputItemAddedEvent is used for response.output_item.added event.
type ResponsesOutputItemAddedEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	OutputIndex    int    `json:"output_index"`
	Item           any    `json:"item"` // flexible: message, reasoning, or function_call
}

// ResponsesMessageItem represents a message item in output_item.added.
type ResponsesMessageItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Role    string `json:"role,omitempty"`
	Content []any  `json:"content"`
}

// ResponsesReasoningItem represents a reasoning item in output_item.added.
type ResponsesReasoningItem struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Summary []any  `json:"summary"`
}

// ResponsesFunctionCallItem represents a function_call item in output_item.added/done.
type ResponsesFunctionCallItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Status    string `json:"status"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

var responsesOutputItemAddedEventPool = sync.Pool{
	New: func() any {
		return &ResponsesOutputItemAddedEvent{
			Type: "response.output_item.added",
		}
	},
}

func GetResponsesOutputItemAddedEvent() *ResponsesOutputItemAddedEvent {
	return responsesOutputItemAddedEventPool.Get().(*ResponsesOutputItemAddedEvent)
}

func PutResponsesOutputItemAddedEvent(d *ResponsesOutputItemAddedEvent) {
	d.SequenceNumber = 0
	d.OutputIndex = 0
	d.Item = nil
	responsesOutputItemAddedEventPool.Put(d)
}

// BuildResponsesOutputItemAddedSSE builds SSE for response.output_item.added (message).
func BuildResponsesOutputItemAddedMessageSSE(seqNum, outputIndex int, itemID, status string) []byte {
	d := GetResponsesOutputItemAddedEvent()
	defer PutResponsesOutputItemAddedEvent(d)

	d.SequenceNumber = seqNum
	d.OutputIndex = outputIndex
	d.Item = ResponsesMessageItem{
		ID:      itemID,
		Type:    "message",
		Status:  status,
		Role:    "assistant",
		Content: []any{},
	}

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.output_item.added", jb)
}

// BuildResponsesOutputItemAddedReasoningSSE builds SSE for response.output_item.added (reasoning).
func BuildResponsesOutputItemAddedReasoningSSE(seqNum, outputIndex int, itemID, status string) []byte {
	d := GetResponsesOutputItemAddedEvent()
	defer PutResponsesOutputItemAddedEvent(d)

	d.SequenceNumber = seqNum
	d.OutputIndex = outputIndex
	d.Item = ResponsesReasoningItem{
		ID:      itemID,
		Type:    "reasoning",
		Status:  status,
		Summary: []any{},
	}

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.output_item.added", jb)
}

// BuildResponsesOutputItemAddedFunctionCallSSE builds SSE for response.output_item.added (function_call).
func BuildResponsesOutputItemAddedFunctionCallSSE(seqNum, outputIndex int, itemID, callID, name, status string) []byte {
	d := GetResponsesOutputItemAddedEvent()
	defer PutResponsesOutputItemAddedEvent(d)

	d.SequenceNumber = seqNum
	d.OutputIndex = outputIndex
	d.Item = ResponsesFunctionCallItem{
		ID:        itemID,
		Type:      "function_call",
		Status:    status,
		CallID:    callID,
		Name:      name,
		Arguments: "",
	}

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.output_item.added", jb)
}

// ResponsesContentPartAddedEvent is used for response.content_part.added event.
type ResponsesContentPartAddedEvent struct {
	Type           string                 `json:"type"`
	SequenceNumber int                    `json:"sequence_number"`
	ItemID         string                 `json:"item_id"`
	OutputIndex    int                    `json:"output_index"`
	ContentIndex   int                    `json:"content_index"`
	Part           ResponsesOutputTextRef `json:"part"`
}

type ResponsesOutputTextRef struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

var responsesContentPartAddedEventPool = sync.Pool{
	New: func() any {
		return &ResponsesContentPartAddedEvent{
			Type: "response.content_part.added",
			Part: ResponsesOutputTextRef{
				Type: "output_text",
			},
		}
	},
}

func GetResponsesContentPartAddedEvent() *ResponsesContentPartAddedEvent {
	return responsesContentPartAddedEventPool.Get().(*ResponsesContentPartAddedEvent)
}

func PutResponsesContentPartAddedEvent(d *ResponsesContentPartAddedEvent) {
	d.SequenceNumber = 0
	d.ItemID = ""
	d.OutputIndex = 0
	d.ContentIndex = 0
	d.Part.Text = ""
	responsesContentPartAddedEventPool.Put(d)
}

// BuildResponsesContentPartAddedSSE builds SSE for response.content_part.added.
func BuildResponsesContentPartAddedSSE(seqNum int, itemID string, outputIndex, contentIndex int) []byte {
	d := GetResponsesContentPartAddedEvent()
	defer PutResponsesContentPartAddedEvent(d)

	d.SequenceNumber = seqNum
	d.ItemID = itemID
	d.OutputIndex = outputIndex
	d.ContentIndex = contentIndex
	d.Part.Text = ""

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.content_part.added", jb)
}

// ResponsesFunctionCallArgsDeltaEvent is used for response.function_call_arguments.delta.
type ResponsesFunctionCallArgsDeltaEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id"`
	OutputIndex    int    `json:"output_index"`
	Delta          string `json:"delta"`
}

var responsesFunctionCallArgsDeltaEventPool = sync.Pool{
	New: func() any {
		return &ResponsesFunctionCallArgsDeltaEvent{
			Type: "response.function_call_arguments.delta",
		}
	},
}

func GetResponsesFunctionCallArgsDeltaEvent() *ResponsesFunctionCallArgsDeltaEvent {
	return responsesFunctionCallArgsDeltaEventPool.Get().(*ResponsesFunctionCallArgsDeltaEvent)
}

func PutResponsesFunctionCallArgsDeltaEvent(d *ResponsesFunctionCallArgsDeltaEvent) {
	d.SequenceNumber = 0
	d.ItemID = ""
	d.OutputIndex = 0
	d.Delta = ""
	responsesFunctionCallArgsDeltaEventPool.Put(d)
}

// BuildResponsesFunctionCallArgsDeltaSSE builds SSE for response.function_call_arguments.delta.
func BuildResponsesFunctionCallArgsDeltaSSE(seqNum int, itemID string, outputIndex int, delta string) []byte {
	d := GetResponsesFunctionCallArgsDeltaEvent()
	defer PutResponsesFunctionCallArgsDeltaEvent(d)

	d.SequenceNumber = seqNum
	d.ItemID = itemID
	d.OutputIndex = outputIndex
	d.Delta = delta

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.function_call_arguments.delta", jb)
}

// ResponsesOutputItemDoneEvent is used for response.output_item.done.
type ResponsesOutputItemDoneEvent struct {
	Type           string `json:"type"`
	SequenceNumber int    `json:"sequence_number"`
	ItemID         string `json:"item_id,omitempty"`
	OutputIndex    int    `json:"output_index"`
	Item           any    `json:"item"`
}

var responsesOutputItemDoneEventPool = sync.Pool{
	New: func() any {
		return &ResponsesOutputItemDoneEvent{
			Type: "response.output_item.done",
		}
	},
}

func GetResponsesOutputItemDoneEvent() *ResponsesOutputItemDoneEvent {
	return responsesOutputItemDoneEventPool.Get().(*ResponsesOutputItemDoneEvent)
}

func PutResponsesOutputItemDoneEvent(d *ResponsesOutputItemDoneEvent) {
	d.SequenceNumber = 0
	d.ItemID = ""
	d.OutputIndex = 0
	d.Item = nil
	responsesOutputItemDoneEventPool.Put(d)
}

// ResponsesMessageItemDone represents a completed message item.
type ResponsesMessageItemDone struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Role    string `json:"role"`
	Content []any  `json:"content"`
}

// ResponsesReasoningItemDone represents a completed reasoning item.
type ResponsesReasoningItemDone struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Summary []any  `json:"summary"`
}

// ResponsesSummaryText for summary array items.
type ResponsesSummaryText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// BuildResponsesOutputItemDoneFunctionCallSSE builds SSE for function_call completion.
func BuildResponsesOutputItemDoneFunctionCallSSE(seqNum int, itemID string, outputIndex int, callID, name, args string) []byte {
	d := GetResponsesOutputItemDoneEvent()
	defer PutResponsesOutputItemDoneEvent(d)

	d.SequenceNumber = seqNum
	d.ItemID = itemID
	d.OutputIndex = outputIndex
	d.Item = ResponsesFunctionCallItem{
		ID:        itemID,
		Type:      "function_call",
		Status:    "completed",
		CallID:    callID,
		Name:      name,
		Arguments: args,
	}

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.output_item.done", jb)
}

// BuildResponsesOutputItemDoneMessageSSE builds SSE for message completion.
func BuildResponsesOutputItemDoneMessageSSE(seqNum, outputIndex int, itemID, text string) []byte {
	d := GetResponsesOutputItemDoneEvent()
	defer PutResponsesOutputItemDoneEvent(d)

	d.SequenceNumber = seqNum
	d.OutputIndex = outputIndex
	d.Item = ResponsesMessageItemDone{
		ID:     itemID,
		Type:   "message",
		Status: "completed",
		Role:   "assistant",
		Content: []any{
			ResponsesOutputTextRef{Type: "output_text", Text: text},
		},
	}

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.output_item.done", jb)
}

// BuildResponsesOutputItemDoneReasoningSSE builds SSE for reasoning completion.
func BuildResponsesOutputItemDoneReasoningSSE(seqNum, outputIndex int, itemID, text string) []byte {
	d := GetResponsesOutputItemDoneEvent()
	defer PutResponsesOutputItemDoneEvent(d)

	d.SequenceNumber = seqNum
	d.OutputIndex = outputIndex
	d.Item = ResponsesReasoningItemDone{
		ID:     itemID,
		Type:   "reasoning",
		Status: "completed",
		Summary: []any{
			ResponsesSummaryText{Type: "summary_text", Text: text},
		},
	}

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.output_item.done", jb)
}

// ResponsesContentPartDoneEvent is used for response.content_part.done.
type ResponsesContentPartDoneEvent struct {
	Type           string                 `json:"type"`
	SequenceNumber int                    `json:"sequence_number"`
	ItemID         string                 `json:"item_id"`
	OutputIndex    int                    `json:"output_index"`
	ContentIndex   int                    `json:"content_index"`
	Part           ResponsesOutputTextRef `json:"part"`
}

var responsesContentPartDoneEventPool = sync.Pool{
	New: func() any {
		return &ResponsesContentPartDoneEvent{
			Type: "response.content_part.done",
			Part: ResponsesOutputTextRef{
				Type: "output_text",
			},
		}
	},
}

func GetResponsesContentPartDoneEvent() *ResponsesContentPartDoneEvent {
	return responsesContentPartDoneEventPool.Get().(*ResponsesContentPartDoneEvent)
}

func PutResponsesContentPartDoneEvent(d *ResponsesContentPartDoneEvent) {
	d.SequenceNumber = 0
	d.ItemID = ""
	d.OutputIndex = 0
	d.ContentIndex = 0
	d.Part.Text = ""
	responsesContentPartDoneEventPool.Put(d)
}

// BuildResponsesContentPartDoneSSE builds SSE for response.content_part.done.
func BuildResponsesContentPartDoneSSE(seqNum int, itemID string, outputIndex, contentIndex int, text string) []byte {
	d := GetResponsesContentPartDoneEvent()
	defer PutResponsesContentPartDoneEvent(d)

	d.SequenceNumber = seqNum
	d.ItemID = itemID
	d.OutputIndex = outputIndex
	d.ContentIndex = contentIndex
	d.Part.Text = text

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.content_part.done", jb)
}

// ResponsesDoneEvent is used for response.done.
type ResponsesDoneEvent struct {
	Type           string                  `json:"type"`
	SequenceNumber int                     `json:"sequence_number"`
	Response       ResponsesDoneEventInner `json:"response"`
}

type ResponsesDoneEventInner struct {
	ID        string              `json:"id"`
	Object    string              `json:"object"`
	CreatedAt int64               `json:"created_at"`
	Status    string              `json:"status"`
	Usage     *ResponsesDoneUsage `json:"usage,omitempty"`
}

type ResponsesDoneUsage struct {
	InputTokens         int64                         `json:"input_tokens"`
	OutputTokens        int64                         `json:"output_tokens"`
	TotalTokens         int64                         `json:"total_tokens"`
	InputTokensDetails  *ResponsesTokensDetails       `json:"input_tokens_details,omitempty"`
	OutputTokensDetails *ResponsesOutputTokensDetails `json:"output_tokens_details,omitempty"`
}

type ResponsesTokensDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

type ResponsesOutputTokensDetails struct {
	ReasoningTokens int64 `json:"reasoning_tokens"`
}

var responsesDoneEventPool = sync.Pool{
	New: func() any {
		return &ResponsesDoneEvent{
			Type: "response.done",
			Response: ResponsesDoneEventInner{
				Object: "response",
				Status: "completed",
			},
		}
	},
}

func GetResponsesDoneEvent() *ResponsesDoneEvent {
	return responsesDoneEventPool.Get().(*ResponsesDoneEvent)
}

func PutResponsesDoneEvent(d *ResponsesDoneEvent) {
	d.SequenceNumber = 0
	d.Response.ID = ""
	d.Response.CreatedAt = 0
	d.Response.Usage = nil
	responsesDoneEventPool.Put(d)
}

// BuildResponsesDoneSSE builds SSE for response.done.
func BuildResponsesDoneSSE(seqNum int, respID string, createdAt int64, usage *ResponsesDoneUsage) []byte {
	d := GetResponsesDoneEvent()
	defer PutResponsesDoneEvent(d)

	d.SequenceNumber = seqNum
	d.Response.ID = respID
	d.Response.CreatedAt = createdAt
	d.Response.Usage = usage

	jb, _ := json.Marshal(d)
	return formatResponsesSSEBytes("response.done", jb)
}
