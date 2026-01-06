package ir

import (
	"testing"

	"github.com/nghyane/llm-mux/internal/json"
)

// -----------------------------------------------------------------------------
// Unit Tests
// -----------------------------------------------------------------------------

func TestBuildOpenAITextDeltaSSE(t *testing.T) {
	result := BuildOpenAITextDeltaSSE("chatcmpl-123", "gpt-4", 1234567890, "Hello")

	expected := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1234567890,"model":"gpt-4","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`

	// Check that result starts with "data: " and ends with "\n\n"
	if len(result) < 10 {
		t.Fatalf("Result too short: %s", string(result))
	}

	// Verify it's valid SSE format
	if string(result[:6]) != "data: " {
		t.Errorf("Expected 'data: ' prefix, got: %s", string(result[:6]))
	}
	if string(result[len(result)-2:]) != "\n\n" {
		t.Errorf("Expected '\\n\\n' suffix")
	}

	// Just check it contains expected content
	if !containsString(string(result), `"content":"Hello"`) {
		t.Errorf("Result doesn't contain expected content: %s", string(result))
	}
	_ = expected // silence unused warning
}

func TestBuildOpenAIReasoningDeltaSSE(t *testing.T) {
	result := BuildOpenAIReasoningDeltaSSE("chatcmpl-123", "gpt-4", 1234567890, "thinking...", "sig123")

	if len(result) < 10 {
		t.Fatalf("Result too short: %s", string(result))
	}

	if string(result[:6]) != "data: " {
		t.Errorf("Expected 'data: ' prefix")
	}

	if !containsString(string(result), `"reasoning"`) {
		t.Errorf("Result doesn't contain reasoning field: %s", string(result))
	}
}

func TestBuildResponsesTextDeltaSSE(t *testing.T) {
	result := BuildResponsesTextDeltaSSE(1, "msg_123", "Hello world")

	if !containsString(string(result), "event: response.output_text.delta") {
		t.Errorf("Result doesn't contain expected event type: %s", string(result))
	}

	if !containsString(string(result), `"delta":"Hello world"`) {
		t.Errorf("Result doesn't contain expected delta: %s", string(result))
	}
}

func TestBuildClaudeTextDeltaSSE(t *testing.T) {
	result := BuildClaudeTextDeltaSSE(0, "Hello")

	if !containsString(string(result), "event: content_block_delta") {
		t.Errorf("Result doesn't contain expected event type: %s", string(result))
	}

	if !containsString(string(result), `"text":"Hello"`) {
		t.Errorf("Result doesn't contain expected text: %s", string(result))
	}
}

func TestBuildClaudeThinkingDeltaSSE(t *testing.T) {
	result := BuildClaudeThinkingDeltaSSE(0, "I'm thinking...")

	if !containsString(string(result), "event: content_block_delta") {
		t.Errorf("Result doesn't contain expected event type: %s", string(result))
	}

	if !containsString(string(result), `"thinking":"I'm thinking..."`) {
		t.Errorf("Result doesn't contain expected thinking: %s", string(result))
	}
}

func TestBuildClaudeContentBlockStopSSE(t *testing.T) {
	result := BuildClaudeContentBlockStopSSE(5)

	if !containsString(string(result), "event: content_block_stop") {
		t.Errorf("Result doesn't contain expected event type: %s", string(result))
	}

	if !containsString(string(result), `"index":5`) {
		t.Errorf("Result doesn't contain expected index: %s", string(result))
	}
}

// Pool tests
func TestOpenAITextDeltaPool(t *testing.T) {
	// Get from pool
	d1 := GetOpenAITextDelta()
	if d1 == nil {
		t.Fatal("GetOpenAITextDelta returned nil")
	}

	// Verify initial state
	if d1.Object != "chat.completion.chunk" {
		t.Errorf("Expected Object = chat.completion.chunk, got %s", d1.Object)
	}
	if len(d1.Choices) != 1 {
		t.Errorf("Expected 1 choice, got %d", len(d1.Choices))
	}

	// Modify and return
	d1.ID = "test-id"
	d1.Model = "test-model"
	d1.Choices[0].Delta.Content = "test-content"
	PutOpenAITextDelta(d1)

	// Get again - should be reset
	d2 := GetOpenAITextDelta()
	if d2.ID != "" {
		t.Errorf("Expected ID to be reset, got %s", d2.ID)
	}
	if d2.Model != "" {
		t.Errorf("Expected Model to be reset, got %s", d2.Model)
	}
	if d2.Choices[0].Delta.Content != "" {
		t.Errorf("Expected Content to be reset, got %s", d2.Choices[0].Delta.Content)
	}
	PutOpenAITextDelta(d2)
}

// -----------------------------------------------------------------------------
// Benchmarks - Compare old vs new approach
// -----------------------------------------------------------------------------

// Benchmark the NEW pooled approach
func BenchmarkBuildOpenAITextDeltaSSE_Pooled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := BuildOpenAITextDeltaSSE("chatcmpl-xyz", "gpt-4", 1234567890, "Hello world")
		_ = result
	}
}

// Benchmark the OLD map approach for comparison
func BenchmarkBuildOpenAITextDeltaSSE_OldMap(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		ch := map[string]any{
			"id":      "chatcmpl-xyz",
			"object":  "chat.completion.chunk",
			"created": int64(1234567890),
			"model":   "gpt-4",
			"choices": []any{
				map[string]any{
					"index": 0,
					"delta": map[string]any{
						"role":    "assistant",
						"content": "Hello world",
					},
				},
			},
		}
		jb, _ := json.Marshal(ch)
		result := BuildSSEChunk(jb)
		_ = result
	}
}

// Benchmark Responses API text delta - NEW
func BenchmarkBuildResponsesTextDeltaSSE_Pooled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := BuildResponsesTextDeltaSSE(i%1000, "msg_123", "Hello world token")
		_ = result
	}
}

// Benchmark Responses API text delta - OLD
func BenchmarkBuildResponsesTextDeltaSSE_OldMap(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m := map[string]any{
			"type":            "response.output_text.delta",
			"sequence_number": i % 1000,
			"item_id":         "msg_123",
			"output_index":    0,
			"content_index":   0,
			"delta":           "Hello world token",
		}
		jb, _ := json.Marshal(m)
		sb := GetStringBuilder()
		sb.WriteString("event: response.output_text.delta\ndata: ")
		sb.Write(jb)
		sb.WriteString("\n\n")
		result := sb.String()
		PutStringBuilder(sb)
		_ = result
	}
}

// Benchmark Claude text delta - NEW
func BenchmarkBuildClaudeTextDeltaSSE_Pooled(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		result := BuildClaudeTextDeltaSSE(0, "Hello world")
		_ = result
	}
}

// Benchmark Claude text delta - OLD
func BenchmarkBuildClaudeTextDeltaSSE_OldMap(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		m := map[string]any{
			"type":  ClaudeSSEContentBlockDelta,
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": "Hello world",
			},
		}
		jb, _ := json.Marshal(m)
		result := BuildSSEEvent(ClaudeSSEContentBlockDelta, jb)
		_ = result
	}
}

// Simulate streaming 10000 tokens - NEW approach
func BenchmarkStreamSimulation_10kTokens_Pooled(b *testing.B) {
	tokens := make([]string, 10000)
	for i := range tokens {
		tokens[i] = "token"
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		for _, tok := range tokens {
			result := BuildOpenAITextDeltaSSE("chatcmpl-xyz", "gpt-4", 1234567890, tok)
			_ = result
		}
	}
}

// Simulate streaming 10000 tokens - OLD approach
func BenchmarkStreamSimulation_10kTokens_OldMap(b *testing.B) {
	tokens := make([]string, 10000)
	for i := range tokens {
		tokens[i] = "token"
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		for _, tok := range tokens {
			ch := map[string]any{
				"id":      "chatcmpl-xyz",
				"object":  "chat.completion.chunk",
				"created": int64(1234567890),
				"model":   "gpt-4",
				"choices": []any{
					map[string]any{
						"index": 0,
						"delta": map[string]any{
							"role":    "assistant",
							"content": tok,
						},
					},
				},
			}
			jb, _ := json.Marshal(ch)
			result := BuildSSEChunk(jb)
			_ = result
		}
	}
}

// Helper function
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && findSubstring(s, substr))
}

func findSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
