package ir

import (
	"encoding/json"
	"testing"

	"github.com/tidwall/gjson"
)

func TestExtractThoughtSignature_CamelCase(t *testing.T) {
	input := `{"thoughtSignature": "abc123encrypted"}`
	parsed := gjson.Parse(input)

	sig := ExtractThoughtSignature(parsed)
	if string(sig) != "abc123encrypted" {
		t.Errorf("expected 'abc123encrypted', got '%s'", string(sig))
	}
}

func TestExtractThoughtSignature_SnakeCase(t *testing.T) {
	input := `{"thought_signature": "xyz789encrypted"}`
	parsed := gjson.Parse(input)

	sig := ExtractThoughtSignature(parsed)
	if string(sig) != "xyz789encrypted" {
		t.Errorf("expected 'xyz789encrypted', got '%s'", string(sig))
	}
}

func TestExtractThoughtSignature_Empty(t *testing.T) {
	input := `{"text": "hello"}`
	parsed := gjson.Parse(input)

	sig := ExtractThoughtSignature(parsed)
	if sig != nil {
		t.Errorf("expected nil, got '%s'", string(sig))
	}
}

func TestBuildOpenAIToolCalls_WithSignature(t *testing.T) {
	builder := &ResponseBuilder{
		messages: []Message{{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID:               "call_123",
				Name:             "get_weather",
				Args:             `{"city": "Tokyo"}`,
				ThoughtSignature: []byte("gemini3_signature_abc"),
			}},
		}},
	}

	tcs := builder.BuildOpenAIToolCalls()
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tcs))
	}

	tc := tcs[0].(map[string]any)
	extraContent, ok := tc["extra_content"].(map[string]any)
	if !ok {
		t.Fatal("expected extra_content in tool call")
	}

	google, ok := extraContent["google"].(map[string]any)
	if !ok {
		t.Fatal("expected google in extra_content")
	}

	sig, ok := google["thought_signature"].(string)
	if !ok || sig != "gemini3_signature_abc" {
		t.Errorf("expected 'gemini3_signature_abc', got '%s'", sig)
	}
}

func TestBuildOpenAIToolCalls_WithoutSignature(t *testing.T) {
	builder := &ResponseBuilder{
		messages: []Message{{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID:   "call_456",
				Name: "get_time",
				Args: `{}`,
			}},
		}},
	}

	tcs := builder.BuildOpenAIToolCalls()
	if len(tcs) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(tcs))
	}

	tc := tcs[0].(map[string]any)
	if _, ok := tc["extra_content"]; ok {
		t.Error("expected no extra_content when signature is empty")
	}
}

func TestBuildGeminiContentParts_ToolCallWithSignature(t *testing.T) {
	builder := &ResponseBuilder{
		messages: []Message{{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID:               "call_789",
				Name:             "search",
				Args:             `{"query": "test"}`,
				ThoughtSignature: []byte("gemini_thought_sig"),
			}},
		}},
	}

	parts := builder.BuildGeminiContentParts()
	if len(parts) != 1 {
		t.Fatalf("expected 1 part, got %d", len(parts))
	}

	part := parts[0].(map[string]any)
	sig, ok := part["thoughtSignature"].(string)
	if !ok || sig != "gemini_thought_sig" {
		t.Errorf("expected 'gemini_thought_sig', got '%s'", sig)
	}
}

func TestParseClaudeSignatureDelta(t *testing.T) {
	state := NewClaudeStreamParserState()

	// Step 1: Send thinking_delta first (gets buffered)
	thinkingInput := `{"type": "content_block_delta", "index": 0, "delta": {"type": "thinking_delta", "thinking": "Let me analyze..."}}`
	thinkingParsed := gjson.Parse(thinkingInput)
	events := ParseClaudeStreamDeltaWithState(thinkingParsed, state)

	// Thinking should be buffered, not emitted yet
	if len(events) != 0 {
		t.Fatalf("thinking should be buffered, got %d events", len(events))
	}
	if !state.HasPendingEvent() {
		t.Fatal("expected pending thinking event in state")
	}

	// Step 2: Send signature_delta (attaches to buffered thinking and emits)
	sigInput := `{"type": "content_block_delta", "index": 0, "delta": {"type": "signature_delta", "signature": "claude_extended_sig"}}`
	sigParsed := gjson.Parse(sigInput)
	events = ParseClaudeStreamDeltaWithState(sigParsed, state)

	if len(events) != 1 {
		t.Fatalf("expected 1 completed event, got %d", len(events))
	}

	ev := events[0]
	if ev.Type != EventTypeReasoning {
		t.Errorf("expected EventTypeReasoning, got %s", ev.Type)
	}

	if ev.Reasoning != "Let me analyze..." {
		t.Errorf("expected 'Let me analyze...', got '%s'", ev.Reasoning)
	}

	if string(ev.ThoughtSignature) != "claude_extended_sig" {
		t.Errorf("expected 'claude_extended_sig', got '%s'", string(ev.ThoughtSignature))
	}

	// State should be cleared
	if state.HasPendingEvent() {
		t.Error("expected no pending event after signature attached")
	}
}

func TestOpenAIToolCallRoundTrip(t *testing.T) {
	// Simulate: Gemini -> IR -> OpenAI -> Client -> OpenAI Request -> IR -> Gemini
	originalSig := "encrypted_gemini3_thought_signature_xyz"

	// Step 1: Build OpenAI response with signature
	builder := &ResponseBuilder{
		messages: []Message{{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{{
				ID:               "call_roundtrip",
				Name:             "test_func",
				Args:             `{"param": "value"}`,
				ThoughtSignature: []byte(originalSig),
			}},
		}},
	}

	tcs := builder.BuildOpenAIToolCalls()
	openaiJSON, err := json.Marshal(tcs)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	// Step 2: Parse the OpenAI format back to IR
	// Simulate assistant message with tool_calls
	msgJSON := `{"role": "assistant", "tool_calls": ` + string(openaiJSON) + `}`
	parsed := gjson.Parse(msgJSON)

	// Extract tool calls like parseOpenAIMessage does
	var extractedSig string
	for _, tc := range parsed.Get("tool_calls").Array() {
		if tc.Get("type").String() == "function" {
			if sig := tc.Get("extra_content.google.thought_signature").String(); sig != "" {
				extractedSig = sig
			}
		}
	}

	if extractedSig != originalSig {
		t.Errorf("round-trip failed: expected '%s', got '%s'", originalSig, extractedSig)
	}
}
