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
