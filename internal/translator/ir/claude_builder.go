package ir

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	ClaudeRoleUser              = "user"
	ClaudeRoleAssistant         = "assistant"
	ClaudeBlockText             = "text"
	ClaudeBlockThinking         = "thinking"
	ClaudeBlockImage            = "image"
	ClaudeBlockDocument         = "document"
	ClaudeBlockToolUse          = "tool_use"
	ClaudeBlockToolResult       = "tool_result"
	ClaudeBlockRedactedThinking = "redacted_thinking"
	ClaudeDeltaRedactedThinking = "redacted_thinking_delta"
	ClaudeSourceBase64          = "base64"
	ClaudeStopEndTurn           = "end_turn"
	ClaudeStopToolUse           = "tool_use"
	ClaudeStopMaxTokens         = "max_tokens"
	ClaudeSSEMessageStart       = "message_start"
	ClaudeSSEContentBlockStart  = "content_block_start"
	ClaudeSSEContentBlockDelta  = "content_block_delta"
	ClaudeSSEContentBlockStop   = "content_block_stop"
	ClaudeSSEMessageDelta       = "message_delta"
	ClaudeSSEMessageStop        = "message_stop"
	ClaudeSSEError              = "error"
	ClaudeDeltaText             = "text_delta"
	ClaudeDeltaThinking         = "thinking_delta"
	ClaudeDeltaInputJSON        = "input_json_delta"
	ClaudeDefaultMaxTokens      = 32000
)

// ClaudeStreamParserState tracks state for parsing Claude SSE stream with tool calls.
type ClaudeStreamParserState struct {
	ToolUseNames             map[int]string
	ToolUseIDs               map[int]string
	ToolUseArgs              map[int]*strings.Builder
	CurrentThinkingSignature string
	BlockTypes               map[int]string
	PendingThinkingEvent     *UnifiedEvent
}

// NewClaudeStreamParserState creates a new parser state with pre-allocated maps.
// Pre-allocation reduces memory allocations during streaming with large context.
func NewClaudeStreamParserState() *ClaudeStreamParserState {
	return &ClaudeStreamParserState{
		ToolUseNames: make(map[int]string, 4),
		ToolUseIDs:   make(map[int]string, 4),
		ToolUseArgs:  make(map[int]*strings.Builder, 4),
		BlockTypes:   make(map[int]string, 8),
	}
}

func (s *ClaudeStreamParserState) BufferThinkingEvent(event *UnifiedEvent) *UnifiedEvent {
	prev := s.PendingThinkingEvent
	s.PendingThinkingEvent = event
	return prev
}

func (s *ClaudeStreamParserState) AttachSignature(sig []byte) *UnifiedEvent {
	if s.PendingThinkingEvent == nil {
		return nil
	}
	event := s.PendingThinkingEvent
	event.ThoughtSignature = sig
	s.PendingThinkingEvent = nil
	return event
}

func (s *ClaudeStreamParserState) HasPendingEvent() bool {
	return s.PendingThinkingEvent != nil
}

func (s *ClaudeStreamParserState) FlushPending() *UnifiedEvent {
	event := s.PendingThinkingEvent
	s.PendingThinkingEvent = nil
	return event
}

func (s *ClaudeStreamParserState) Finalize() *UnifiedEvent {
	return s.FlushPending()
}

// ParseClaudeUsage parses Claude usage object into IR Usage.
func ParseClaudeUsage(usage gjson.Result) *Usage {
	if !usage.Exists() {
		return nil
	}
	input, output := usage.Get("input_tokens").Int(), usage.Get("output_tokens").Int()
	u := &Usage{PromptTokens: input, CompletionTokens: output, TotalTokens: input + output}

	// Parse Claude-specific cache tokens
	if v := usage.Get("cache_creation_input_tokens"); v.Exists() {
		u.CacheCreationInputTokens = v.Int()
	}
	if v := usage.Get("cache_read_input_tokens"); v.Exists() {
		u.CacheReadInputTokens = v.Int()
	}

	// Map cache tokens to PromptTokensDetails for OpenAI compatibility
	// Use cache_read_input_tokens as the primary "cached" count (tokens read from cache)
	// cache_creation_input_tokens represents tokens written to cache (not cached hits)
	if u.CacheReadInputTokens > 0 {
		if u.PromptTokensDetails == nil {
			u.PromptTokensDetails = &PromptTokensDetails{}
		}
		u.PromptTokensDetails.CachedTokens = u.CacheReadInputTokens
	}

	return u
}

// ParseClaudeContentBlock parses a Claude content block into IR Message parts.
func ParseClaudeContentBlock(block gjson.Result, msg *Message) {
	switch block.Get("type").String() {
	case ClaudeBlockText:
		if text := block.Get("text").String(); text != "" {
			part := ContentPart{Type: ContentTypeText, Text: text}
			if citations := block.Get("citations"); citations.Exists() && citations.IsArray() {
				for _, c := range citations.Array() {
					citation := &TextCitation{
						Type:           c.Get("type").String(),
						DocumentIndex:  int(c.Get("document_index").Int()),
						StartCharIndex: int(c.Get("start_char_index").Int()),
						EndCharIndex:   int(c.Get("end_char_index").Int()),
						URL:            c.Get("url").String(),
						Title:          c.Get("title").String(),
						// Extended fields for full Claude citation support
						FileID:            c.Get("file_id").String(),
						CitedText:         c.Get("cited_text").String(),
						DocumentTitle:     c.Get("document_title").String(),
						StartPageNumber:   int(c.Get("start_page_number").Int()),
						EndPageNumber:     int(c.Get("end_page_number").Int()),
						StartBlockIndex:   int(c.Get("start_block_index").Int()),
						EndBlockIndex:     int(c.Get("end_block_index").Int()),
						EncryptedIndex:    c.Get("encrypted_index").String(),
						SearchResultIndex: int(c.Get("search_result_index").Int()),
						Source:            c.Get("source").String(),
					}
					part.Citations = append(part.Citations, citation)
				}
			}
			msg.Content = append(msg.Content, part)
		}
	case ClaudeBlockThinking:
		if thinking := block.Get("thinking").String(); thinking != "" {
			part := ContentPart{Type: ContentTypeReasoning, Reasoning: thinking}
			if sig := block.Get("signature").String(); sig != "" {
				part.ThoughtSignature = []byte(sig)
			}
			msg.Content = append(msg.Content, part)
		}
	case ClaudeBlockImage:
		if source := block.Get("source"); source.Exists() && source.Get("type").String() == ClaudeSourceBase64 {
			msg.Content = append(msg.Content, ContentPart{
				Type:  ContentTypeImage,
				Image: &ImagePart{MimeType: source.Get("media_type").String(), Data: source.Get("data").String()},
			})
		}
	case ClaudeBlockToolUse:
		args := block.Get("input").Raw
		if args == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID: block.Get("id").String(), Name: block.Get("name").String(), Args: args,
		})
	case ClaudeBlockToolResult:
		content := block.Get("content")
		var result string
		if content.Type == gjson.String {
			result = content.String()
		} else if content.IsArray() {
			var parts []string
			for _, part := range content.Array() {
				if part.Get("type").String() == ClaudeBlockText {
					parts = append(parts, part.Get("text").String())
				}
			}
			result = strings.Join(parts, "\n")
		} else {
			result = content.Raw
		}
		msg.Content = append(msg.Content, ContentPart{
			Type: ContentTypeToolResult,
			ToolResult: &ToolResultPart{
				ToolCallID: block.Get("tool_use_id").String(),
				Result:     result,
				IsError:    block.Get("is_error").Bool(),
			},
		})
	case ClaudeBlockRedactedThinking:
		// Preserve encrypted redacted thinking data for protocol compliance (round-trip)
		if data := block.Get("data").String(); data != "" {
			msg.Content = append(msg.Content, ContentPart{
				Type:         ContentTypeRedactedThinking,
				RedactedData: data,
			})
		}
	case "mcp_tool_use":
		args := block.Get("input").Raw
		if args == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID: block.Get("id").String(), Name: block.Get("name").String(), Args: args,
		})
	case "mcp_tool_result":
		content := block.Get("content")
		var result string
		if content.Type == gjson.String {
			result = content.String()
		} else if content.IsArray() {
			var parts []string
			for _, part := range content.Array() {
				if part.Get("type").String() == ClaudeBlockText {
					parts = append(parts, part.Get("text").String())
				}
			}
			result = strings.Join(parts, "\n")
		} else {
			result = content.Raw
		}
		msg.Content = append(msg.Content, ContentPart{
			Type: ContentTypeToolResult,
			ToolResult: &ToolResultPart{
				ToolCallID: block.Get("tool_use_id").String(),
				Result:     result,
				IsError:    block.Get("is_error").Bool(),
			},
		})
	case "server_tool_use":
		args := block.Get("input").Raw
		if args == "" {
			args = "{}"
		}
		msg.ToolCalls = append(msg.ToolCalls, ToolCall{
			ID: block.Get("id").String(), Name: block.Get("name").String(), Args: args,
		})
	case "web_search_tool_result":
		content := block.Get("content")
		var result string
		if content.Type == gjson.String {
			result = content.String()
		} else if content.IsArray() {
			var parts []string
			for _, part := range content.Array() {
				if part.Get("type").String() == ClaudeBlockText {
					parts = append(parts, part.Get("text").String())
				}
			}
			result = strings.Join(parts, "\n")
		} else {
			result = content.Raw
		}
		msg.Content = append(msg.Content, ContentPart{
			Type: ContentTypeToolResult,
			ToolResult: &ToolResultPart{
				ToolCallID: block.Get("tool_use_id").String(),
				Result:     result,
				IsError:    block.Get("is_error").Bool(),
			},
		})
	}
}

// ExtractSSEData strips "data: " prefix from SSE line.
// Optimized to avoid string conversions where possible.
func ExtractSSEData(raw []byte) []byte {
	// Fast path: check for "data: " or "data:" prefix
	if len(raw) > 6 && raw[0] == 'd' && raw[1] == 'a' && raw[2] == 't' && raw[3] == 'a' {
		if raw[4] == ':' && raw[5] == ' ' {
			return bytes.TrimSpace(raw[6:])
		}
		if raw[4] == ':' {
			return bytes.TrimSpace(raw[5:])
		}
	}
	return bytes.TrimSpace(raw)
}

// ParseClaudeStreamDelta parses Claude content_block_delta into IR events.
func ParseClaudeStreamDelta(parsed gjson.Result) []UnifiedEvent {
	return ParseClaudeStreamDeltaWithState(parsed, nil)
}

// ParseClaudeStreamDeltaWithState parses content_block_delta with state tracking for tool calls.
func ParseClaudeStreamDeltaWithState(parsed gjson.Result, state *ClaudeStreamParserState) []UnifiedEvent {
	delta := parsed.Get("delta")
	switch delta.Get("type").String() {
	case ClaudeDeltaText:
		if text := delta.Get("text").String(); text != "" {
			if state != nil {
				if pending := state.FlushPending(); pending != nil {
					return []UnifiedEvent{*pending, {Type: EventTypeToken, Content: text}}
				}
			}
			return []UnifiedEvent{{Type: EventTypeToken, Content: text}}
		}
	case ClaudeDeltaThinking:
		if thinking := delta.Get("thinking").String(); thinking != "" {
			thinkingEvent := &UnifiedEvent{Type: EventTypeReasoning, Reasoning: thinking}
			if state != nil {
				if prev := state.BufferThinkingEvent(thinkingEvent); prev != nil {
					return []UnifiedEvent{*prev}
				}
				return nil
			}
			return []UnifiedEvent{*thinkingEvent}
		}
	case "signature_delta":
		if sig := delta.Get("signature").String(); sig != "" {
			if state != nil {
				if completed := state.AttachSignature([]byte(sig)); completed != nil {
					return []UnifiedEvent{*completed}
				}
			}
		}
		return nil
	case ClaudeDeltaRedactedThinking:
		if data := delta.Get("data").String(); data != "" {
			if state != nil {
				if pending := state.FlushPending(); pending != nil {
					return []UnifiedEvent{*pending, {Type: EventTypeReasoning, RedactedData: data}}
				}
			}
			return []UnifiedEvent{{Type: EventTypeReasoning, RedactedData: data}}
		}
	case ClaudeDeltaInputJSON:
		if state != nil {
			idx := int(parsed.Get("index").Int())
			if state.ToolUseArgs[idx] == nil {
				state.ToolUseArgs[idx] = GetStringBuilder()
			}
			if pj := delta.Get("partial_json"); pj.Exists() {
				state.ToolUseArgs[idx].WriteString(pj.String())
			}
		}
	}
	return nil
}

// ParseClaudeContentBlockStart parses content_block_start event and updates state.
func ParseClaudeContentBlockStart(parsed gjson.Result, state *ClaudeStreamParserState) []UnifiedEvent {
	if state == nil {
		return nil
	}
	cb := parsed.Get("content_block")
	idx := int(parsed.Get("index").Int())
	state.BlockTypes[idx] = cb.Get("type").String()
	if cb.Get("type").String() == ClaudeBlockToolUse {
		state.ToolUseNames[idx] = cb.Get("name").String()
		state.ToolUseIDs[idx] = cb.Get("id").String()
	} else if cb.Get("type").String() == ClaudeBlockThinking {
		if sig := cb.Get("signature").String(); sig != "" {
			state.CurrentThinkingSignature = sig
		}
	} else if cb.Get("type").String() == ClaudeBlockRedactedThinking {
		// Redacted thinking block starts - will receive data in delta
		return nil
	}
	return nil
}

// ParseClaudeContentBlockStop parses content_block_stop event and emits tool call if applicable.
func ParseClaudeContentBlockStop(parsed gjson.Result, state *ClaudeStreamParserState) []UnifiedEvent {
	if state == nil {
		return nil
	}
	idx := int(parsed.Get("index").Int())
	name, id := state.ToolUseNames[idx], state.ToolUseIDs[idx]
	if name == "" && id == "" {
		if state.BlockTypes[idx] == ClaudeBlockThinking {
			state.CurrentThinkingSignature = ""
		}
		delete(state.BlockTypes, idx)
		return nil
	}

	var events []UnifiedEvent
	if pending := state.FlushPending(); pending != nil {
		events = append(events, *pending)
	}

	args := "{}"
	if builder := state.ToolUseArgs[idx]; builder != nil {
		if s := strings.TrimSpace(builder.String()); s != "" {
			args = s
		}
		PutStringBuilder(builder)
	}

	delete(state.ToolUseNames, idx)
	delete(state.ToolUseIDs, idx)
	delete(state.ToolUseArgs, idx)
	delete(state.BlockTypes, idx)

	events = append(events, UnifiedEvent{
		Type:     EventTypeToolCall,
		ToolCall: &ToolCall{ID: id, Name: name, Args: args},
	})
	return events
}

// ParseClaudeMessageDelta parses Claude message_delta into IR events.
func ParseClaudeMessageDelta(parsed gjson.Result) []UnifiedEvent {
	finishReason := FinishReasonUnknown
	if delta := parsed.Get("delta"); delta.Exists() {
		if sr := delta.Get("stop_reason"); sr.Exists() {
			finishReason = MapClaudeFinishReason(sr.String())
		}
	}
	var usage *Usage
	if u := parsed.Get("usage"); u.Exists() {
		usage = ParseClaudeUsage(u)
	}
	return []UnifiedEvent{{Type: EventTypeFinish, Usage: usage, FinishReason: finishReason}}
}
