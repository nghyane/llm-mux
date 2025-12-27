package to_ir

import (
	"testing"

	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// ==================== ParseGeminiRequest Tests ====================

func TestParseGeminiRequest_Basic(t *testing.T) {
	input := `{
		"model": "gemini-2.5-flash",
		"contents": [
			{
				"role": "user",
				"parts": [{"text": "Hello"}]
			}
		],
		"generationConfig": {
			"maxOutputTokens": 1000,
			"temperature": 0.7
		}
	}`

	req, err := ParseGeminiRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiRequest failed: %v", err)
	}

	if req.Model != "gemini-2.5-flash" {
		t.Errorf("Model = %q, want %q", req.Model, "gemini-2.5-flash")
	}
	if req.MaxTokens == nil || *req.MaxTokens != 1000 {
		t.Errorf("MaxTokens = %v, want 1000", req.MaxTokens)
	}
	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", req.Temperature)
	}
}

func TestParseGeminiRequest_WithRequestWrapper(t *testing.T) {
	// Gemini CLI format with request wrapper
	input := `{
		"request": {
			"model": "gemini-3-pro-preview",
			"contents": [
				{
					"role": "user",
					"parts": [{"text": "Hello"}]
				}
			]
		}
	}`

	req, err := ParseGeminiRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiRequest failed: %v", err)
	}

	if req.Model != "gemini-3-pro-preview" {
		t.Errorf("Model = %q, want %q", req.Model, "gemini-3-pro-preview")
	}
}

// ==================== ThinkingConfig Tests ====================

func TestParseGeminiRequest_ThinkingConfig(t *testing.T) {
	input := `{
		"model": "gemini-3-flash-preview",
		"contents": [{"role": "user", "parts": [{"text": "Hello"}]}],
		"generationConfig": {
			"thinkingConfig": {
				"thinkingBudget": 8192,
				"includeThoughts": true,
				"thinkingLevel": "MEDIUM"
			}
		}
	}`

	req, err := ParseGeminiRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiRequest failed: %v", err)
	}

	if req.Thinking == nil {
		t.Fatal("Thinking should not be nil")
	}
	if req.Thinking.ThinkingBudget == nil || *req.Thinking.ThinkingBudget != 8192 {
		t.Errorf("ThinkingBudget = %v, want 8192", req.Thinking.ThinkingBudget)
	}
	if !req.Thinking.IncludeThoughts {
		t.Error("IncludeThoughts should be true")
	}
	if req.Thinking.ThinkingLevel != ir.ThinkingLevelMedium {
		t.Errorf("ThinkingLevel = %q, want %q", req.Thinking.ThinkingLevel, ir.ThinkingLevelMedium)
	}
}

// ==================== ParseGeminiResponse Tests ====================

func TestParseGeminiResponse_Basic(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Hello! How can I help?"}]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 20,
			"totalTokenCount": 30
		}
	}`

	_, messages, usage, err := ParseGeminiResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiResponse failed: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if messages[0].Role != ir.RoleAssistant {
		t.Errorf("Message role = %q, want %q", messages[0].Role, ir.RoleAssistant)
	}

	if usage == nil {
		t.Fatal("Usage should not be nil")
	}
	if usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d, want 10", usage.PromptTokens)
	}
	if usage.CompletionTokens != 20 {
		t.Errorf("CompletionTokens = %d, want 20", usage.CompletionTokens)
	}
}

func TestParseGeminiResponse_WithThinking(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{"text": "Let me think about this...", "thought": true},
					{"text": "Here's my answer"}
				]
			},
			"finishReason": "STOP"
		}],
		"usageMetadata": {
			"promptTokenCount": 10,
			"candidatesTokenCount": 30,
			"totalTokenCount": 40,
			"thoughtsTokenCount": 15
		}
	}`

	_, messages, usage, err := ParseGeminiResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiResponse failed: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	// Should have both reasoning and text parts
	reasoningFound := false
	textFound := false
	for _, part := range messages[0].Content {
		if part.Type == ir.ContentTypeReasoning {
			reasoningFound = true
			if part.Reasoning != "Let me think about this..." {
				t.Errorf("Reasoning = %q, want %q", part.Reasoning, "Let me think about this...")
			}
		}
		if part.Type == ir.ContentTypeText {
			textFound = true
		}
	}

	if !reasoningFound {
		t.Error("Expected to find reasoning content")
	}
	if !textFound {
		t.Error("Expected to find text content")
	}

	if usage.ThoughtsTokenCount != 15 {
		t.Errorf("ThoughtsTokenCount = %d, want 15", usage.ThoughtsTokenCount)
	}
}

func TestParseGeminiResponse_WithThoughtSignature(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{
						"text": "Analyzing...",
						"thought": true,
						"thoughtSignature": "sig_abc123"
					},
					{"text": "Answer here"}
				]
			},
			"finishReason": "STOP"
		}]
	}`

	_, messages, _, err := ParseGeminiResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiResponse failed: %v", err)
	}

	if len(messages) == 0 {
		t.Fatal("Expected at least one message")
	}

	// Find the reasoning part with signature
	found := false
	for _, part := range messages[0].Content {
		if part.Type == ir.ContentTypeReasoning {
			found = true
			if string(part.ThoughtSignature) != "sig_abc123" {
				t.Errorf("ThoughtSignature = %q, want %q", part.ThoughtSignature, "sig_abc123")
			}
		}
	}

	if !found {
		t.Error("Expected to find reasoning content with signature")
	}
}

// ==================== SafetyRatings Tests ====================

func TestParseGeminiResponseCandidates_WithSafetyRatings(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Hello"}]
			},
			"finishReason": "STOP",
			"safetyRatings": [
				{
					"category": "HARM_CATEGORY_HARASSMENT",
					"probability": "NEGLIGIBLE",
					"blocked": false
				},
				{
					"category": "HARM_CATEGORY_HATE_SPEECH",
					"probability": "LOW",
					"blocked": false
				},
				{
					"category": "HARM_CATEGORY_DANGEROUS_CONTENT",
					"probability": "HIGH",
					"blocked": true,
					"severity": "SEVERITY_HIGH"
				}
			]
		}]
	}`

	candidates, _, _, err := ParseGeminiResponseCandidates([]byte(input), nil)
	if err != nil {
		t.Fatalf("ParseGeminiResponseCandidates failed: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(candidates))
	}

	ratings := candidates[0].SafetyRatings
	if len(ratings) != 3 {
		t.Fatalf("Expected 3 safety ratings, got %d", len(ratings))
	}

	// Check first rating
	if ratings[0].Category != "HARM_CATEGORY_HARASSMENT" {
		t.Errorf("Rating[0].Category = %q, want %q", ratings[0].Category, "HARM_CATEGORY_HARASSMENT")
	}
	if ratings[0].Probability != "NEGLIGIBLE" {
		t.Errorf("Rating[0].Probability = %q, want %q", ratings[0].Probability, "NEGLIGIBLE")
	}
	if ratings[0].Blocked {
		t.Error("Rating[0].Blocked should be false")
	}

	// Check blocked rating
	if !ratings[2].Blocked {
		t.Error("Rating[2].Blocked should be true")
	}
	if ratings[2].Severity != "SEVERITY_HIGH" {
		t.Errorf("Rating[2].Severity = %q, want %q", ratings[2].Severity, "SEVERITY_HIGH")
	}
}

func TestParseGeminiResponseCandidates_EmptySafetyRatings(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Hello"}]
			},
			"finishReason": "STOP"
		}]
	}`

	candidates, _, _, err := ParseGeminiResponseCandidates([]byte(input), nil)
	if err != nil {
		t.Fatalf("ParseGeminiResponseCandidates failed: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(candidates))
	}

	if candidates[0].SafetyRatings != nil && len(candidates[0].SafetyRatings) != 0 {
		t.Errorf("Expected nil or empty safety ratings, got %d", len(candidates[0].SafetyRatings))
	}
}

// ==================== FinishReason Tests ====================

func TestParseGeminiResponse_FinishReasonMapping(t *testing.T) {
	tests := []struct {
		name           string
		geminiReason   string
		expectedReason ir.FinishReason
	}{
		{"STOP", "STOP", ir.FinishReasonStop},
		{"MAX_TOKENS", "MAX_TOKENS", ir.FinishReasonMaxTokens},
		{"SAFETY", "SAFETY", ir.FinishReasonContentFilter},
		{"RECITATION", "RECITATION", ir.FinishReasonRecitation},
		{"BLOCKLIST", "BLOCKLIST", ir.FinishReasonBlocklist},
		{"PROHIBITED_CONTENT", "PROHIBITED_CONTENT", ir.FinishReasonProhibitedContent},
		{"SPII", "SPII", ir.FinishReasonSPII},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := `{
				"candidates": [{
					"content": {
						"role": "model",
						"parts": [{"text": "Hello"}]
					},
					"finishReason": "` + tt.geminiReason + `"
				}]
			}`

			candidates, _, _, err := ParseGeminiResponseCandidates([]byte(input), nil)
			if err != nil {
				t.Fatalf("ParseGeminiResponseCandidates failed: %v", err)
			}

			if len(candidates) != 1 {
				t.Fatalf("Expected 1 candidate, got %d", len(candidates))
			}

			if candidates[0].FinishReason != tt.expectedReason {
				t.Errorf("FinishReason = %q, want %q", candidates[0].FinishReason, tt.expectedReason)
			}
		})
	}
}

// ==================== Tool Calls Tests ====================

func TestParseGeminiResponse_WithFunctionCall(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"functionCall": {
						"name": "get_weather",
						"args": {"location": "San Francisco"},
						"id": "call_123"
					}
				}]
			},
			"finishReason": "STOP"
		}]
	}`

	_, messages, _, err := ParseGeminiResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiResponse failed: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if len(messages[0].ToolCalls) != 1 {
		t.Fatalf("Expected 1 tool call, got %d", len(messages[0].ToolCalls))
	}

	tc := messages[0].ToolCalls[0]
	if tc.Name != "get_weather" {
		t.Errorf("ToolCall.Name = %q, want %q", tc.Name, "get_weather")
	}
	if tc.ID != "call_123" {
		t.Errorf("ToolCall.ID = %q, want %q", tc.ID, "call_123")
	}
}

func TestParseGeminiResponse_WithFunctionCallThoughtSignature(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{
					"functionCall": {
						"name": "get_weather",
						"args": {"location": "NYC"}
					},
					"thoughtSignature": "sig_xyz789"
				}]
			},
			"finishReason": "STOP"
		}]
	}`

	_, messages, _, err := ParseGeminiResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiResponse failed: %v", err)
	}

	if len(messages) == 0 || len(messages[0].ToolCalls) == 0 {
		t.Fatal("Expected message with tool call")
	}

	tc := messages[0].ToolCalls[0]
	if string(tc.ThoughtSignature) != "sig_xyz789" {
		t.Errorf("ThoughtSignature = %q, want %q", tc.ThoughtSignature, "sig_xyz789")
	}
}

// ==================== Code Execution Tests ====================

func TestParseGeminiResponse_WithCodeExecution(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [
					{
						"executableCode": {
							"language": "PYTHON",
							"code": "print('Hello')"
						}
					},
					{
						"codeExecutionResult": {
							"outcome": "OUTCOME_OK",
							"output": "Hello"
						}
					}
				]
			},
			"finishReason": "STOP"
		}]
	}`

	_, messages, _, err := ParseGeminiResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiResponse failed: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	// Find executable code and result parts
	execFound := false
	resultFound := false
	for _, part := range messages[0].Content {
		if part.Type == ir.ContentTypeExecutableCode {
			execFound = true
			if part.CodeExecution == nil {
				t.Error("CodeExecution should not be nil")
			} else if part.CodeExecution.Code != "print('Hello')" {
				t.Errorf("Code = %q, want %q", part.CodeExecution.Code, "print('Hello')")
			}
		}
		if part.Type == ir.ContentTypeCodeResult {
			resultFound = true
			if part.CodeExecution == nil {
				t.Error("CodeExecution should not be nil")
			} else if part.CodeExecution.Output != "Hello" {
				t.Errorf("Output = %q, want %q", part.CodeExecution.Output, "Hello")
			}
		}
	}

	if !execFound {
		t.Error("Expected to find executable code")
	}
	if !resultFound {
		t.Error("Expected to find code execution result")
	}
}

// ==================== Grounding Metadata Tests ====================

func TestParseGeminiResponse_WithGroundingMetadata(t *testing.T) {
	input := `{
		"candidates": [{
			"content": {
				"role": "model",
				"parts": [{"text": "Based on my search..."}]
			},
			"finishReason": "STOP",
			"groundingMetadata": {
				"webSearchQueries": ["test query"],
				"groundingChunks": [
					{
						"web": {
							"uri": "https://example.com",
							"title": "Example Page",
							"domain": "example.com"
						}
					}
				]
			}
		}]
	}`

	candidates, _, meta, err := ParseGeminiResponseCandidates([]byte(input), nil)
	if err != nil {
		t.Fatalf("ParseGeminiResponseCandidates failed: %v", err)
	}

	if len(candidates) != 1 {
		t.Fatalf("Expected 1 candidate, got %d", len(candidates))
	}

	gm := candidates[0].GroundingMetadata
	if gm == nil {
		t.Fatal("GroundingMetadata should not be nil")
	}

	if len(gm.WebSearchQueries) != 1 || gm.WebSearchQueries[0] != "test query" {
		t.Errorf("WebSearchQueries = %v, want [test query]", gm.WebSearchQueries)
	}

	if len(gm.GroundingChunks) != 1 {
		t.Fatalf("Expected 1 grounding chunk, got %d", len(gm.GroundingChunks))
	}

	if gm.GroundingChunks[0].Web == nil {
		t.Fatal("Web should not be nil")
	}
	if gm.GroundingChunks[0].Web.URI != "https://example.com" {
		t.Errorf("URI = %q, want %q", gm.GroundingChunks[0].Web.URI, "https://example.com")
	}

	// Check meta is also populated
	if meta == nil {
		t.Error("Meta should not be nil")
	}
}

// ==================== ParseGeminiChunk Tests ====================

func TestParseGeminiChunk_TextDelta(t *testing.T) {
	input := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}`

	events, err := ParseGeminiChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiChunk failed: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("Expected at least one event")
	}

	if events[0].Type != ir.EventTypeToken {
		t.Errorf("Event type = %q, want %q", events[0].Type, ir.EventTypeToken)
	}
	if events[0].Content != "Hello" {
		t.Errorf("Event content = %q, want %q", events[0].Content, "Hello")
	}
}

func TestParseGeminiChunk_ThinkingDelta(t *testing.T) {
	input := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Thinking...","thought":true}]}}]}`

	events, err := ParseGeminiChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiChunk failed: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("Expected at least one event")
	}

	if events[0].Type != ir.EventTypeReasoning {
		t.Errorf("Event type = %q, want %q", events[0].Type, ir.EventTypeReasoning)
	}
}

func TestParseGeminiChunk_Done(t *testing.T) {
	input := `data: [DONE]`

	events, err := ParseGeminiChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiChunk failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(events))
	}

	if events[0].Type != ir.EventTypeFinish {
		t.Errorf("Event type = %q, want %q", events[0].Type, ir.EventTypeFinish)
	}
}

func TestParseGeminiChunk_WithFinishReason(t *testing.T) {
	input := `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"Done"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5}}`

	events, err := ParseGeminiChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiChunk failed: %v", err)
	}

	// Should have token and finish events
	finishFound := false
	for _, e := range events {
		if e.Type == ir.EventTypeFinish {
			finishFound = true
			if e.FinishReason != ir.FinishReasonStop {
				t.Errorf("FinishReason = %q, want %q", e.FinishReason, ir.FinishReasonStop)
			}
			if e.Usage == nil {
				t.Error("Usage should not be nil")
			}
		}
	}

	if !finishFound {
		t.Error("Expected to find finish event")
	}
}

// ==================== Antigravity Envelope Tests ====================

func TestParseGeminiChunk_AntigravityEnvelope(t *testing.T) {
	// Test unwrapping of Antigravity response envelope
	input := `data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"Hello"}]}}]}}`

	events, err := ParseGeminiChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseGeminiChunk failed: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("Expected at least one event")
	}

	if events[0].Content != "Hello" {
		t.Errorf("Event content = %q, want %q", events[0].Content, "Hello")
	}
}
