package util

import (
	"testing"

	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// =============================================================================
// IR-Based Token Counting Tests
// =============================================================================

func TestCountTokensFromIR_Basic(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "Hello world"},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count <= 0 {
		t.Errorf("Expected tokens > 0, got %d", count)
	}
	t.Logf("IR basic token count: %d", count)
}

func TestCountTokensFromIR_WithTools(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "Calculate something"},
				},
			},
		},
		Tools: []ir.ToolDefinition{
			{
				Name:        "calculator",
				Description: "A simple calculator",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"expression": map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count <= 0 {
		t.Errorf("Expected tokens > 0, got %d", count)
	}
	t.Logf("IR with tools token count: %d", count)
}

func TestCountTokensFromIR_MultiTurnToolCall(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "What's the weather in Tokyo?"},
				},
			},
			{
				Role: ir.RoleAssistant,
				ToolCalls: []ir.ToolCall{
					{
						ID:   "call_1",
						Name: "get_weather",
						Args: `{"location": "Tokyo", "unit": "celsius"}`,
					},
				},
			},
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{
						Type: ir.ContentTypeToolResult,
						ToolResult: &ir.ToolResultPart{
							ToolCallID: "call_1",
							Result:     `{"temperature": 25, "condition": "sunny", "humidity": 60}`,
						},
					},
				},
			},
			{
				Role: ir.RoleAssistant,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "The weather in Tokyo is sunny with 25°C"},
				},
			},
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "What about tomorrow?"},
				},
			},
		},
		Tools: []ir.ToolDefinition{
			{
				Name:        "get_weather",
				Description: "Get current weather for a location",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"location": map[string]any{"type": "string"},
						"unit":     map[string]any{"type": "string"},
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count < 50 {
		t.Errorf("Expected tokens >= 50 for multi-turn tool call, got %d", count)
	}
	t.Logf("IR multi-turn tool call token count: %d", count)
}

func TestCountTokensFromIR_WithImage(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "Look at this image"},
					{
						Type: ir.ContentTypeImage,
						Image: &ir.ImagePart{
							MimeType: "image/jpeg",
							Data:     "base64data",
						},
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	// Should include 258 tokens for image
	if count < ImageTokenCost {
		t.Errorf("Expected tokens >= %d (image cost), got %d", ImageTokenCost, count)
	}
	t.Logf("IR with image token count: %d", count)
}

func TestCountTokensFromIR_Nil(t *testing.T) {
	count := CountTokensFromIR("gemini-1.5-flash", nil)
	if count != 0 {
		t.Errorf("Expected 0 tokens for nil request, got %d", count)
	}
}

func TestCountTokensFromIR_EmptyMessages(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model:    "gemini-1.5-flash",
		Messages: []ir.Message{},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count != 0 {
		t.Errorf("Expected 0 tokens for empty messages, got %d", count)
	}
}

func TestCountTokensFromIR_SystemMessage(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleSystem,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "You are a helpful assistant."},
				},
			},
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "Hello"},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count <= 0 {
		t.Errorf("Expected tokens > 0 for system + user message, got %d", count)
	}
	t.Logf("IR with system message token count: %d", count)
}

func TestCountTokensFromIR_Reasoning(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleAssistant,
				Content: []ir.ContentPart{
					{
						Type:      ir.ContentTypeReasoning,
						Reasoning: "Let me think about this step by step...",
					},
					{
						Type: ir.ContentTypeText,
						Text: "The answer is 42.",
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count <= 0 {
		t.Errorf("Expected tokens > 0 for reasoning content, got %d", count)
	}
	t.Logf("IR with reasoning token count: %d", count)
}

func TestCountTokensFromIR_LargeToolResult(t *testing.T) {
	// Simulate a large file content in tool result
	largeContent := `{
		"files": [
			{"name": "file1.go", "size": 1024, "modified": "2024-01-01"},
			{"name": "file2.go", "size": 2048, "modified": "2024-01-02"},
			{"name": "file3.go", "size": 4096, "modified": "2024-01-03"},
			{"name": "file4.go", "size": 8192, "modified": "2024-01-04"},
			{"name": "file5.go", "size": 16384, "modified": "2024-01-05"}
		],
		"total": 5,
		"directory": "/src/project"
	}`

	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{
						Type: ir.ContentTypeToolResult,
						ToolResult: &ir.ToolResultPart{
							ToolCallID: "list_files_call",
							Result:     largeContent,
						},
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count < 30 {
		t.Errorf("Expected tokens >= 30 for large tool result, got %d", count)
	}
	t.Logf("IR with large tool result token count: %d", count)
}

func TestCountTokensFromIR_WithAudio(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "Listen to this"},
					{
						Type: ir.ContentTypeAudio,
						Audio: &ir.AudioPart{
							Data:       "base64audiodata",
							Format:     "wav",
							Transcript: "Hello world",
						},
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	// Should include AudioTokenCostGemini (300) + text tokens + transcript tokens
	if count < AudioTokenCostGemini {
		t.Errorf("Expected tokens >= %d (audio cost), got %d", AudioTokenCostGemini, count)
	}
	t.Logf("IR with audio token count: %d", count)
}

func TestCountTokensFromIR_WithVideo(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeText, Text: "Watch this video"},
					{
						Type: ir.ContentTypeVideo,
						Video: &ir.VideoPart{
							FileURI:  "gs://bucket/video.mp4",
							MimeType: "video/mp4",
						},
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	// Should include VideoTokenCostGemini (2000) + text tokens
	if count < VideoTokenCostGemini {
		t.Errorf("Expected tokens >= %d (video cost), got %d", VideoTokenCostGemini, count)
	}
	t.Logf("IR with video token count: %d", count)
}

func TestCountTokensFromIR_WithThoughtSignature(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleAssistant,
				Content: []ir.ContentPart{
					{
						Type:             ir.ContentTypeReasoning,
						Reasoning:        "Let me think...",
						ThoughtSignature: []byte("signature-data-here"),
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	if count <= 0 {
		t.Errorf("Expected tokens > 0 for thought signature, got %d", count)
	}
	t.Logf("IR with thought signature token count: %d", count)
}

func TestCountTokensFromIR_ToolResultWithFiles(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{
				Role: ir.RoleUser,
				Content: []ir.ContentPart{
					{
						Type: ir.ContentTypeToolResult,
						ToolResult: &ir.ToolResultPart{
							ToolCallID: "file_read_call",
							Result:     `{"content": "file contents here"}`,
							Files: []*ir.FilePart{
								{FileID: "file-123", Filename: "data.csv"},
								{FileURL: "https://example.com/file.pdf"},
							},
						},
					},
				},
			},
		},
	}

	count := CountTokensFromIR("gemini-1.5-flash", req)
	// Should include 2 * DocTokenCostGemini (1000) + result tokens
	if count < 2*DocTokenCostGemini {
		t.Errorf("Expected tokens >= %d (2 files), got %d", 2*DocTokenCostGemini, count)
	}
	t.Logf("IR with tool result files token count: %d", count)
}

// =============================================================================
// Model Normalization Tests
// =============================================================================

func TestModelNormalization(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"gemini-2.5-pro", "gemini-2.5-pro"}, // Supported
		{"gemini-2.0-flash", "gemini-2.0-flash"},
		{"gemini-1.5-pro", "gemini-1.5-pro"},
		{"gemini-1.5-flash", "gemini-1.5-flash"},
		{"gemini-1.0-pro", "gemini-1.0-pro"},
		{"gemini-pro", "gemini-1.0-pro"},
		{"unknown-model", "gemini-2.5-flash"}, // Default fallback
	}

	for _, tc := range testCases {
		result := normalizeModel(tc.input)
		if result != tc.expected {
			t.Errorf("normalizeModel(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

// =============================================================================
// Benchmarks
// =============================================================================

func BenchmarkCountTokensFromIR_Simple(b *testing.B) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hello world"}}},
		},
	}

	// Warm up tokenizer cache
	CountTokensFromIR("gemini-1.5-flash", req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CountTokensFromIR("gemini-1.5-flash", req)
	}
}

func BenchmarkCountTokensFromIR_MultiTurn(b *testing.B) {
	req := &ir.UnifiedChatRequest{
		Model: "gemini-1.5-flash",
		Messages: []ir.Message{
			{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "What's the weather in Tokyo?"}}},
			{Role: ir.RoleAssistant, ToolCalls: []ir.ToolCall{{ID: "1", Name: "get_weather", Args: `{"location": "Tokyo"}`}}},
			{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeToolResult, ToolResult: &ir.ToolResultPart{ToolCallID: "1", Result: `{"temp": 25}`}}}},
			{Role: ir.RoleAssistant, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "It's sunny in Tokyo"}}},
			{Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Thanks!"}}},
		},
		Tools: []ir.ToolDefinition{
			{Name: "get_weather", Description: "Get weather"},
		},
	}

	// Warm up tokenizer cache
	CountTokensFromIR("gemini-1.5-flash", req)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		CountTokensFromIR("gemini-1.5-flash", req)
	}
}
