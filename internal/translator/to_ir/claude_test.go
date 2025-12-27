package to_ir

import (
	"testing"

	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// ==================== ParseClaudeRequest Tests ====================

func TestParseClaudeRequest_Basic(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{"role": "user", "content": "Hello"}
		]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	if req.Model != "claude-sonnet-4-20250514" {
		t.Errorf("Model = %q, want %q", req.Model, "claude-sonnet-4-20250514")
	}
	if req.MaxTokens == nil || *req.MaxTokens != 1024 {
		t.Errorf("MaxTokens = %v, want 1024", req.MaxTokens)
	}
}

// ==================== redacted_thinking Tests ====================

func TestParseClaudeRequest_RedactedThinking(t *testing.T) {
	// Test parsing of redacted_thinking block from message history
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "redacted_thinking",
						"data": "encrypted_base64_data_here"
					},
					{
						"type": "text",
						"text": "Based on my analysis..."
					}
				]
			}
		]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	// Find the redacted_thinking content part
	found := false
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeRedactedThinking {
				found = true
				if part.RedactedData != "encrypted_base64_data_here" {
					t.Errorf("RedactedData = %q, want %q", part.RedactedData, "encrypted_base64_data_here")
				}
			}
		}
	}

	if !found {
		t.Error("Expected to find redacted_thinking content part")
	}
}

func TestParseClaudeRequest_RedactedThinking_EmptyData(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "redacted_thinking",
						"data": ""
					}
				]
			}
		]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	// Should still parse even with empty data
	found := false
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeRedactedThinking {
				found = true
			}
		}
	}

	if !found {
		t.Error("Expected to find redacted_thinking content part even with empty data")
	}
}

// ==================== thinking Block Tests ====================

func TestParseClaudeRequest_ThinkingWithSignature(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "Let me analyze this step by step...",
						"signature": "sig_abc123def456"
					},
					{
						"type": "text",
						"text": "Here's my answer"
					}
				]
			}
		]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	// Find the thinking content part
	found := false
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeReasoning {
				found = true
				if part.Reasoning != "Let me analyze this step by step..." {
					t.Errorf("Reasoning = %q, want %q", part.Reasoning, "Let me analyze this step by step...")
				}
				if string(part.ThoughtSignature) != "sig_abc123def456" {
					t.Errorf("ThoughtSignature = %q, want %q", part.ThoughtSignature, "sig_abc123def456")
				}
			}
		}
	}

	if !found {
		t.Error("Expected to find thinking content part")
	}
}

// ==================== Citations Tests ====================

func TestParseClaudeResponse_WithCitations(t *testing.T) {
	input := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [
			{
				"type": "text",
				"text": "According to the document, the answer is 42.",
				"citations": [
					{
						"type": "document",
						"document_index": 0,
						"start_char_index": 25,
						"end_char_index": 44
					}
				]
			}
		],
		"stop_reason": "end_turn",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50
		}
	}`

	messages, usage, err := ParseClaudeResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeResponse failed: %v", err)
	}

	if len(messages) != 1 {
		t.Fatalf("Expected 1 message, got %d", len(messages))
	}

	if len(messages[0].Content) == 0 {
		t.Fatal("Expected at least one content part")
	}

	textPart := messages[0].Content[0]
	if textPart.Type != ir.ContentTypeText {
		t.Errorf("Content type = %q, want %q", textPart.Type, ir.ContentTypeText)
	}

	if len(textPart.Citations) != 1 {
		t.Fatalf("Expected 1 citation, got %d", len(textPart.Citations))
	}

	citation := textPart.Citations[0]
	if citation.Type != "document" {
		t.Errorf("Citation type = %q, want %q", citation.Type, "document")
	}
	if citation.DocumentIndex != 0 {
		t.Errorf("DocumentIndex = %d, want 0", citation.DocumentIndex)
	}
	if citation.StartCharIndex != 25 {
		t.Errorf("StartCharIndex = %d, want 25", citation.StartCharIndex)
	}
	if citation.EndCharIndex != 44 {
		t.Errorf("EndCharIndex = %d, want 44", citation.EndCharIndex)
	}

	if usage == nil {
		t.Fatal("Usage should not be nil")
	}
}

func TestParseClaudeResponse_MultipleCitations(t *testing.T) {
	input := `{
		"content": [
			{
				"type": "text",
				"text": "Source 1 says X. Source 2 confirms Y.",
				"citations": [
					{
						"type": "document",
						"document_index": 0,
						"start_char_index": 0,
						"end_char_index": 16
					},
					{
						"type": "document",
						"document_index": 1,
						"start_char_index": 17,
						"end_char_index": 37
					}
				]
			}
		],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`

	messages, _, err := ParseClaudeResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeResponse failed: %v", err)
	}

	if len(messages) == 0 || len(messages[0].Content) == 0 {
		t.Fatal("Expected message with content")
	}

	if len(messages[0].Content[0].Citations) != 2 {
		t.Errorf("Expected 2 citations, got %d", len(messages[0].Content[0].Citations))
	}
}

// ==================== Thinking Config Tests ====================

func TestParseClaudeRequest_ThinkingEnabled(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"thinking": {
			"type": "enabled",
			"budget_tokens": 8000
		},
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	if req.Thinking == nil {
		t.Fatal("Thinking should not be nil")
	}
	if !req.Thinking.IncludeThoughts {
		t.Error("Thinking.IncludeThoughts should be true")
	}
	if req.Thinking.ThinkingBudget == nil || *req.Thinking.ThinkingBudget != 8000 {
		t.Errorf("ThinkingBudget = %v, want 8000", req.Thinking.ThinkingBudget)
	}
}

func TestParseClaudeRequest_ThinkingDisabled(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"thinking": {
			"type": "disabled"
		},
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	if req.Thinking == nil {
		t.Fatal("Thinking should not be nil")
	}
	if req.Thinking.IncludeThoughts {
		t.Error("Thinking.IncludeThoughts should be false")
	}
	if req.Thinking.ThinkingBudget == nil || *req.Thinking.ThinkingBudget != 0 {
		t.Errorf("ThinkingBudget = %v, want 0", req.Thinking.ThinkingBudget)
	}
}

// ==================== Tool Parsing Tests ====================

func TestParseClaudeRequest_ToolResult_WithIsError(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_abc123",
						"content": "Error: File not found",
						"is_error": true
					}
				]
			}
		]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	// Find the tool result
	found := false
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeToolResult && part.ToolResult != nil {
				found = true
				if !part.ToolResult.IsError {
					t.Error("ToolResult.IsError should be true")
				}
				if part.ToolResult.Result != "Error: File not found" {
					t.Errorf("ToolResult.Result = %q, want %q", part.ToolResult.Result, "Error: File not found")
				}
			}
		}
	}

	if !found {
		t.Error("Expected to find tool_result content part")
	}
}

// ==================== MCP Server Tests ====================

func TestParseClaudeRequest_MCPServers(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"mcp_servers": [
			{
				"type": "url",
				"url": "https://example.com/mcp",
				"name": "my-server",
				"authorization_token": "secret123"
			}
		],
		"messages": [{"role": "user", "content": "Hello"}]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	if len(req.MCPServers) != 1 {
		t.Fatalf("Expected 1 MCP server, got %d", len(req.MCPServers))
	}

	srv := req.MCPServers[0]
	if srv.Type != "url" {
		t.Errorf("MCPServer.Type = %q, want %q", srv.Type, "url")
	}
	if srv.URL != "https://example.com/mcp" {
		t.Errorf("MCPServer.URL = %q, want %q", srv.URL, "https://example.com/mcp")
	}
	if srv.Name != "my-server" {
		t.Errorf("MCPServer.Name = %q, want %q", srv.Name, "my-server")
	}
	if srv.AuthorizationToken != "secret123" {
		t.Errorf("MCPServer.AuthorizationToken = %q, want %q", srv.AuthorizationToken, "secret123")
	}
}

// ==================== Web Search Tool Tests ====================

func TestParseClaudeRequest_WebSearchTool(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"tools": [
			{
				"type": "web_search_20250305",
				"name": "web_search",
				"max_uses": 5
			}
		],
		"messages": [{"role": "user", "content": "Search for something"}]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	// Web search should be stored in metadata, not tools
	if req.Metadata == nil {
		t.Fatal("Metadata should not be nil")
	}

	gsConfig, ok := req.Metadata[ir.MetaGoogleSearch]
	if !ok {
		t.Error("Expected google_search metadata")
	}

	if cfg, ok := gsConfig.(map[string]any); ok {
		if cfg["max_uses"] != 5 {
			t.Errorf("max_uses = %v, want 5", cfg["max_uses"])
		}
	}
}

// ==================== ParseClaudeChunk Tests ====================

func TestParseClaudeChunk_TextDelta(t *testing.T) {
	// ParseClaudeChunk expects just the data line (SSE data is already stripped)
	input := `data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`

	events, err := ParseClaudeChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeChunk failed: %v", err)
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

func TestParseClaudeChunk_ThinkingDelta(t *testing.T) {
	input := `data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Analyzing..."}}`

	events, err := ParseClaudeChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeChunk failed: %v", err)
	}

	if len(events) == 0 {
		t.Fatal("Expected at least one event")
	}

	if events[0].Type != ir.EventTypeReasoning {
		t.Errorf("Event type = %q, want %q", events[0].Type, ir.EventTypeReasoning)
	}
	if events[0].Reasoning != "Analyzing..." {
		t.Errorf("Event reasoning = %q, want %q", events[0].Reasoning, "Analyzing...")
	}
}

func TestParseClaudeChunk_MessageStop(t *testing.T) {
	input := `data: {"type":"message_stop"}`

	events, err := ParseClaudeChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeChunk failed: %v", err)
	}

	if len(events) != 1 {
		t.Fatalf("Expected 1 event, got %d", len(events))
	}

	if events[0].Type != ir.EventTypeFinish {
		t.Errorf("Event type = %q, want %q", events[0].Type, ir.EventTypeFinish)
	}
}

func TestParseClaudeChunk_Ping(t *testing.T) {
	input := `data: {"type":"ping"}`

	events, err := ParseClaudeChunk([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeChunk failed: %v", err)
	}

	// Ping should be ignored
	if len(events) != 0 {
		t.Errorf("Expected 0 events for ping, got %d", len(events))
	}
}

// ==================== round-trip Tests ====================

func TestClaudeRedactedThinking_RoundTrip(t *testing.T) {
	// ParseClaudeRequest
	input := `{
		"model": "claude-3-7-sonnet-20250219",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{"type": "redacted_thinking", "data": "secret_data_123"},
					{"type": "text", "text": "Hello"}
				]
			}
		]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	// Verify IR
	found := false
	for _, msg := range req.Messages {
		for _, part := range msg.Content {
			if part.Type == ir.ContentTypeRedactedThinking && part.RedactedData == "secret_data_123" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("redacted_thinking not found in IR")
	}

	// From IR back to Claude format (using from_ir logic)
	// We'll test this via a helper or by calling the provider
}

func TestParseClaudeResponse_WithFullCitations(t *testing.T) {
	input := `{
		"id": "msg_123",
		"type": "message",
		"role": "assistant",
		"content": [
			{
				"type": "text",
				"text": "The sky is blue.",
				"citations": [
					{
						"type": "url",
						"url": "https://example.com",
						"title": "Example Title",
						"start_char_index": 0,
						"end_char_index": 15
					}
				]
			}
		]
	}`

	messages, _, err := ParseClaudeResponse([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeResponse failed: %v", err)
	}

	citation := messages[0].Content[0].Citations[0]
	if citation.URL != "https://example.com" {
		t.Errorf("URL = %q, want %q", citation.URL, "https://example.com")
	}
	if citation.Title != "Example Title" {
		t.Errorf("Title = %q, want %q", citation.Title, "Example Title")
	}
}

// ==================== Cache Control Tests ====================

func TestParseClaudeRequest_CacheControl(t *testing.T) {
	input := `{
		"model": "claude-sonnet-4-20250514",
		"max_tokens": 1024,
		"messages": [
			{
				"role": "user",
				"content": "Hello",
				"cache_control": {
					"type": "ephemeral"
				}
			}
		]
	}`

	req, err := ParseClaudeRequest([]byte(input))
	if err != nil {
		t.Fatalf("ParseClaudeRequest failed: %v", err)
	}

	if len(req.Messages) == 0 {
		t.Fatal("Expected at least one message")
	}

	msg := req.Messages[len(req.Messages)-1] // Get the user message (not system)
	if msg.CacheControl == nil {
		t.Fatal("CacheControl should not be nil")
	}
	if msg.CacheControl.Type != "ephemeral" {
		t.Errorf("CacheControl.Type = %q, want %q", msg.CacheControl.Type, "ephemeral")
	}
}
