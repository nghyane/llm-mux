package ir

import (
	"sync"
	"testing"
)

// BenchmarkCombineTextParts benchmarks the optimized CombineTextParts function.
func BenchmarkCombineTextParts(b *testing.B) {
	// Single part message (fast path)
	singlePart := Message{
		Role: RoleAssistant,
		Content: []ContentPart{
			{Type: ContentTypeText, Text: "Hello, this is a test response."},
		},
	}

	// Multi-part message (slow path)
	multiPart := Message{
		Role: RoleAssistant,
		Content: []ContentPart{
			{Type: ContentTypeText, Text: "Part 1: Introduction"},
			{Type: ContentTypeText, Text: "Part 2: Main content with more details"},
			{Type: ContentTypeText, Text: "Part 3: Conclusion and summary"},
		},
	}

	b.Run("SinglePart", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = CombineTextParts(singlePart)
		}
	})

	b.Run("MultiPart", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = CombineTextParts(multiPart)
		}
	})
}

// BenchmarkCombineTextAndReasoning benchmarks the combined extraction.
func BenchmarkCombineTextAndReasoning(b *testing.B) {
	msg := Message{
		Role: RoleAssistant,
		Content: []ContentPart{
			{Type: ContentTypeReasoning, Reasoning: "Let me think about this..."},
			{Type: ContentTypeText, Text: "Here is my answer."},
			{Type: ContentTypeReasoning, Reasoning: "Additional reasoning here."},
			{Type: ContentTypeText, Text: "More text content."},
		},
	}

	b.Run("Mixed", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = CombineTextAndReasoning(msg)
		}
	})
}

// BenchmarkGenerateUUID benchmarks the optimized UUID generation.
func BenchmarkGenerateUUID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = GenerateUUID()
	}
}

// BenchmarkGenToolCallID benchmarks tool call ID generation.
func BenchmarkGenToolCallID(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = GenToolCallID()
	}
}

// BenchmarkBuildToolMaps benchmarks the single-pass tool map building.
func BenchmarkBuildToolMaps(b *testing.B) {
	messages := []Message{
		{
			Role: RoleAssistant,
			ToolCalls: []ToolCall{
				{ID: "call_1", Name: "get_weather"},
				{ID: "call_2", Name: "search"},
			},
		},
		{
			Role: RoleTool,
			Content: []ContentPart{
				{Type: ContentTypeToolResult, ToolResult: &ToolResultPart{ToolCallID: "call_1", Result: "Sunny, 72°F"}},
			},
		},
		{
			Role: RoleTool,
			Content: []ContentPart{
				{Type: ContentTypeToolResult, ToolResult: &ToolResultPart{ToolCallID: "call_2", Result: "Found 10 results"}},
			},
		},
	}

	b.Run("BuildToolMaps", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = BuildToolMaps(messages)
		}
	})
}

// BenchmarkResponseBuilder benchmarks the response builder.
func BenchmarkResponseBuilder(b *testing.B) {
	messages := []Message{
		{
			Role: RoleAssistant,
			Content: []ContentPart{
				{Type: ContentTypeReasoning, Reasoning: "Thinking about the problem..."},
				{Type: ContentTypeText, Text: "Here is the solution."},
			},
			ToolCalls: []ToolCall{
				{ID: "call_abc123", Name: "execute_code", Args: `{"code": "print('hello')"}`},
			},
		},
	}
	usage := &Usage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150}

	b.Run("BuildClaudeContentParts", func(b *testing.B) {
		builder := NewResponseBuilder(messages, usage, "claude-3")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = builder.BuildClaudeContentParts()
		}
	})

	b.Run("BuildGeminiContentParts", func(b *testing.B) {
		builder := NewResponseBuilder(messages, usage, "gemini-pro")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = builder.BuildGeminiContentParts()
		}
	})

	b.Run("BuildOpenAIToolCalls", func(b *testing.B) {
		builder := NewResponseBuilder(messages, usage, "gpt-4")
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = builder.BuildOpenAIToolCalls()
		}
	})
}

// BenchmarkCleanJsonSchemaForClaude benchmarks schema cleaning with caching.
func BenchmarkCleanJsonSchemaForClaude(b *testing.B) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"file_path": map[string]any{
				"type":        "string",
				"description": "Path to the file",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write",
			},
		},
		"required": []string{"file_path", "content"},
	}

	b.Run("FirstCall", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// Clear cache for fair comparison
			schemaCache = sync.Map{}
			schemaCopy := copySchema(schema)
			_ = CleanJsonSchemaForClaude(schemaCopy)
		}
	})

	b.Run("CachedCall", func(b *testing.B) {
		// Prime the cache
		schemaCopy := copySchema(schema)
		_ = CleanJsonSchemaForClaude(schemaCopy)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			schemaCopy := copySchema(schema)
			_ = CleanJsonSchemaForClaude(schemaCopy)
		}
	})
}

// copySchema creates a deep copy of a schema for testing.
func copySchema(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	result := make(map[string]any, len(m))
	for k, v := range m {
		if nested, ok := v.(map[string]any); ok {
			result[k] = copySchema(nested)
		} else {
			result[k] = v
		}
	}
	return result
}

// BenchmarkBufferPool benchmarks buffer pool usage.
func BenchmarkBufferPool(b *testing.B) {
	b.Run("GetPutBuffer", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			buf := GetBuffer()
			buf.WriteString("test data")
			PutBuffer(buf)
		}
	})

	b.Run("GetPutStringBuilder", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			sb := GetStringBuilder()
			sb.WriteString("test data")
			PutStringBuilder(sb)
		}
	})
}
