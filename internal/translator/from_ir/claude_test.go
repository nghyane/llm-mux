package from_ir

import (
	"testing"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/tidwall/gjson"
)

func TestClaudeProvider_ThinkingBlocksNoCacheControl(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []ir.Message{
			{
				Role:    ir.RoleUser,
				Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hello"}},
				CacheControl: &ir.CacheControl{
					Type: "ephemeral",
				},
			},
			{
				Role: ir.RoleAssistant,
				Content: []ir.ContentPart{
					{Type: ir.ContentTypeReasoning, Reasoning: "Let me think...", ThoughtSignature: []byte("sig123")},
					{Type: ir.ContentTypeText, Text: "Response"},
				},
				CacheControl: &ir.CacheControl{
					Type: "ephemeral",
				},
			},
			{
				Role:    ir.RoleUser,
				Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Follow up"}},
			},
		},
		MaxTokens: ir.Ptr(1024),
		Thinking: &ir.ThinkingConfig{
			IncludeThoughts: true,
			ThinkingBudget:  ir.Ptr(int32(1024)),
		},
	}

	p := &ClaudeProvider{}
	payload, err := p.ConvertRequest(req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	parsed := gjson.ParseBytes(payload)
	messages := parsed.Get("messages").Array()

	if len(messages) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(messages))
	}

	if !messages[0].Get("cache_control").Exists() {
		t.Error("user message should have cache_control")
	}

	if messages[1].Get("cache_control").Exists() {
		t.Error("assistant message with thinking parts should NOT have cache_control (Claude API restriction)")
	}

	hasThinking := false
	for _, part := range messages[1].Get("content").Array() {
		if part.Get("type").String() == "thinking" {
			hasThinking = true
			break
		}
	}
	if !hasThinking {
		t.Error("assistant message should have thinking content block")
	}
}

func TestClaudeProvider_NormalMessagesKeepCacheControl(t *testing.T) {
	req := &ir.UnifiedChatRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []ir.Message{
			{
				Role:    ir.RoleUser,
				Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hello"}},
				CacheControl: &ir.CacheControl{
					Type: "ephemeral",
				},
			},
			{
				Role:    ir.RoleAssistant,
				Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hi there!"}},
				CacheControl: &ir.CacheControl{
					Type: "ephemeral",
				},
			},
		},
		MaxTokens: ir.Ptr(1024),
	}

	p := &ClaudeProvider{}
	payload, err := p.ConvertRequest(req)
	if err != nil {
		t.Fatalf("ConvertRequest failed: %v", err)
	}

	parsed := gjson.ParseBytes(payload)
	messages := parsed.Get("messages").Array()

	if len(messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(messages))
	}

	if !messages[0].Get("cache_control").Exists() {
		t.Error("user message should have cache_control")
	}

	if !messages[1].Get("cache_control").Exists() {
		t.Error("assistant message without thinking should have cache_control")
	}
}
