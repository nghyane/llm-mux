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
