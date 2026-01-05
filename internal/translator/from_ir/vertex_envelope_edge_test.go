package from_ir

import (
    "testing"
    "github.com/nghyane/llm-mux/internal/translator/ir"
    "github.com/tidwall/gjson"
)

func TestVertexEnvelopeProvider_ThinkingWithToolCalls(t *testing.T) {
    // Edge case: thinking parts + tool calls + cacheControl
    req := &ir.UnifiedChatRequest{
        Model: "claude-sonnet-4-20250514",
        Messages: []ir.Message{
            {
                Role:    ir.RoleUser,
                Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hello"}},
                CacheControl: &ir.CacheControl{Type: "ephemeral"},
            },
            {
                Role: ir.RoleAssistant,
                Content: []ir.ContentPart{
                    {Type: ir.ContentTypeReasoning, Reasoning: "Let me think...", ThoughtSignature: []byte("sig123")},
                },
                ToolCalls: []ir.ToolCall{
                    {ID: "tool1", Name: "get_weather", Args: `{"city":"NYC"}`},
                },
                CacheControl: &ir.CacheControl{Type: "ephemeral"},
            },
            {
                Role: ir.RoleTool,
                Content: []ir.ContentPart{
                    {Type: ir.ContentTypeToolResult, ToolResult: &ir.ToolResultPart{
                        ToolCallID: "tool1",
                        Result:     "sunny",
                    }},
                },
            },
            {
                Role:    ir.RoleUser,
                Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "What about tomorrow?"}},
            },
        },
        MaxTokens: ir.Ptr(1024),
        Thinking: &ir.ThinkingConfig{
            IncludeThoughts: true,
            ThinkingBudget:  ir.Ptr(int32(5000)),
        },
    }

    p := &VertexEnvelopeProvider{}
    payload, err := p.ConvertRequest(req)
    if err != nil {
        t.Fatalf("ConvertRequest failed: %v", err)
    }

    parsed := gjson.ParseBytes(payload)
    contents := parsed.Get("request.contents").Array()
    
    t.Logf("Payload: %s", parsed.String())
    t.Logf("Number of contents: %d", len(contents))

    for i, content := range contents {
        t.Logf("Content %d: role=%s", i, content.Get("role").String())
        
        hasThinking := false
        parts := content.Get("parts").Array()
        for _, part := range parts {
            if part.Get("thought").Bool() {
                hasThinking = true
            }
            // Check that thinking parts don't have cacheControl
            if part.Get("thought").Bool() && part.Get("cacheControl").Exists() {
                t.Errorf("Content %d: thinking part should NOT have cacheControl inside it", i)
            }
        }
        
        // If content has thinking parts, it should NOT have cacheControl at content level
        if hasThinking && content.Get("cacheControl").Exists() {
            t.Errorf("Content %d: content with thinking parts should NOT have cacheControl", i)
        }
    }
}

func TestVertexEnvelopeProvider_UserWithCacheControlFollowedByThinking(t *testing.T) {
    // Edge case: user message with cacheControl followed by model message with thinking
    req := &ir.UnifiedChatRequest{
        Model: "claude-sonnet-4-20250514",
        Messages: []ir.Message{
            {
                Role:    ir.RoleUser,
                Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hello"}},
                CacheControl: &ir.CacheControl{Type: "ephemeral"},
            },
            {
                Role: ir.RoleAssistant,
                Content: []ir.ContentPart{
                    {Type: ir.ContentTypeReasoning, Reasoning: "Let me think...", ThoughtSignature: []byte("sig123")},
                    {Type: ir.ContentTypeText, Text: "Response"},
                },
                // NO CacheControl on this message
            },
            {
                Role:    ir.RoleUser,
                Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Follow up"}},
                CacheControl: &ir.CacheControl{Type: "ephemeral"},
            },
        },
        MaxTokens: ir.Ptr(1024),
        Thinking: &ir.ThinkingConfig{
            IncludeThoughts: true,
            ThinkingBudget:  ir.Ptr(int32(5000)),
        },
    }

    p := &VertexEnvelopeProvider{}
    payload, err := p.ConvertRequest(req)
    if err != nil {
        t.Fatalf("ConvertRequest failed: %v", err)
    }

    parsed := gjson.ParseBytes(payload)
    contents := parsed.Get("request.contents").Array()
    
    t.Logf("Payload: %s", parsed.String())

    if len(contents) != 3 {
        t.Fatalf("expected 3 contents, got %d", len(contents))
    }

    // Content 0 (user): should have cacheControl
    if !contents[0].Get("cacheControl").Exists() {
        t.Error("Content 0 (user): should have cacheControl")
    }

    // Content 1 (model): should NOT have cacheControl (has thinking)
    if contents[1].Get("cacheControl").Exists() {
        t.Error("Content 1 (model): should NOT have cacheControl because it has thinking parts")
    }
    
    // Verify it has thinking parts
    hasThinking := false
    for _, part := range contents[1].Get("parts").Array() {
        if part.Get("thought").Bool() || part.Get("thoughtSignature").Exists() {
            hasThinking = true
        }
        // Verify no cacheControl on individual parts
        if part.Get("cacheControl").Exists() {
            t.Error("Part should NOT have cacheControl")
        }
    }
    if !hasThinking {
        t.Error("Content 1 should have thinking parts")
    }

    // Content 2 (user): should have cacheControl
    if !contents[2].Get("cacheControl").Exists() {
        t.Error("Content 2 (user): should have cacheControl")
    }
}

func TestVertexEnvelopeProvider_RedactedThinkingNoCacheControl(t *testing.T) {
    // Test that redacted thinking blocks prevent cacheControl from being added
    req := &ir.UnifiedChatRequest{
        Model: "claude-sonnet-4-20250514",
        Messages: []ir.Message{
            {
                Role:    ir.RoleUser,
                Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hello"}},
            },
            {
                Role: ir.RoleAssistant,
                Content: []ir.ContentPart{
                    {Type: ir.ContentTypeRedactedThinking, RedactedData: "encrypted_data_123"},
                    {Type: ir.ContentTypeText, Text: "Response"},
                },
                CacheControl: &ir.CacheControl{Type: "ephemeral"},
            },
        },
        MaxTokens: ir.Ptr(1024),
    }

    p := &VertexEnvelopeProvider{}
    payload, err := p.ConvertRequest(req)
    if err != nil {
        t.Fatalf("ConvertRequest failed: %v", err)
    }

    parsed := gjson.ParseBytes(payload)
    contents := parsed.Get("request.contents").Array()
    
    t.Logf("Payload: %s", parsed.String())

    if len(contents) != 2 {
        t.Fatalf("expected 2 contents, got %d", len(contents))
    }

    // Content 1 (model): should NOT have cacheControl (has redacted thinking)
    if contents[1].Get("cacheControl").Exists() {
        t.Error("Content 1 (model): should NOT have cacheControl because it has redacted thinking")
    }
    
    // Verify the redacted thinking data is preserved
    hasRedactedData := false
    for _, part := range contents[1].Get("parts").Array() {
        if part.Get("data").String() == "encrypted_data_123" {
            hasRedactedData = true
        }
    }
    if !hasRedactedData {
        t.Error("Redacted thinking data should be preserved")
    }
}
