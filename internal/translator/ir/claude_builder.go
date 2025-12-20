package ir

import (
	"bytes"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	ClaudeRoleUser             = "user"
	ClaudeRoleAssistant        = "assistant"
	ClaudeBlockText            = "text"
	ClaudeBlockThinking        = "thinking"
	ClaudeBlockImage           = "image"
	ClaudeBlockToolUse         = "tool_use"
	ClaudeBlockToolResult      = "tool_result"
	ClaudeSourceBase64         = "base64"
	ClaudeStopEndTurn          = "end_turn"
	ClaudeStopToolUse          = "tool_use"
	ClaudeStopMaxTokens        = "max_tokens"
	ClaudeSSEMessageStart      = "message_start"
	ClaudeSSEContentBlockStart = "content_block_start"
	ClaudeSSEContentBlockDelta = "content_block_delta"
	ClaudeSSEContentBlockStop  = "content_block_stop"
	ClaudeSSEMessageDelta      = "message_delta"
	ClaudeSSEMessageStop       = "message_stop"
	ClaudeSSEError             = "error"
	ClaudeDeltaText            = "text_delta"
	ClaudeDeltaThinking        = "thinking_delta"
	ClaudeDeltaInputJSON       = "input_json_delta"
	ClaudeDefaultMaxTokens     = 32000
)

// ClaudeStreamParserState tracks state for parsing Claude SSE stream with tool calls.
type ClaudeStreamParserState struct {
	ToolUseNames             map[int]string
	ToolUseIDs               map[int]string
	ToolUseArgs              map[int]*strings.Builder
	CurrentThinkingSignature string
}

func NewClaudeStreamParserState() *ClaudeStreamParserState {
	return &ClaudeStreamParserState{
		ToolUseNames: make(map[int]string),
		ToolUseIDs:   make(map[int]string),
		ToolUseArgs:  make(map[int]*strings.Builder),
	}
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
			msg.Content = append(msg.Content, ContentPart{Type: ContentTypeText, Text: text})
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
			Type:       ContentTypeToolResult,
			ToolResult: &ToolResultPart{ToolCallID: block.Get("tool_use_id").String(), Result: result},
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
			return []UnifiedEvent{{Type: EventTypeToken, Content: text}}
		}
	case ClaudeDeltaThinking:
		if thinking := delta.Get("thinking").String(); thinking != "" {
			var sig []byte
			if state != nil && state.CurrentThinkingSignature != "" {
				sig = []byte(state.CurrentThinkingSignature)
			}
			return []UnifiedEvent{{Type: EventTypeReasoning, Reasoning: thinking, ThoughtSignature: sig}}
		}
	case ClaudeDeltaInputJSON:
		if state != nil {
			idx := int(parsed.Get("index").Int())
			if state.ToolUseArgs[idx] == nil {
				state.ToolUseArgs[idx] = &strings.Builder{}
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
	if cb.Get("type").String() == ClaudeBlockToolUse {
		idx := int(parsed.Get("index").Int())
		state.ToolUseNames[idx] = cb.Get("name").String()
		state.ToolUseIDs[idx] = cb.Get("id").String()
	} else if cb.Get("type").String() == ClaudeBlockThinking {
		if sig := cb.Get("signature").String(); sig != "" {
			state.CurrentThinkingSignature = sig
		}
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
		return nil
	}

	args := "{}"
	if builder := state.ToolUseArgs[idx]; builder != nil {
		if s := strings.TrimSpace(builder.String()); s != "" {
			args = s
		}
	}

	delete(state.ToolUseNames, idx)
	delete(state.ToolUseIDs, idx)
	delete(state.ToolUseArgs, idx)

	return []UnifiedEvent{{
		Type:     EventTypeToolCall,
		ToolCall: &ToolCall{ID: id, Name: name, Args: args},
	}}
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
