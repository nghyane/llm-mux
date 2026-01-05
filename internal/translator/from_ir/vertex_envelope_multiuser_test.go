package from_ir

import (
    "testing"
    "github.com/nghyane/llm-mux/internal/translator/ir"
    "github.com/tidwall/gjson"
)

func TestVertexEnvelopeProvider_LongConversationWithThinking(t *testing.T) {
    // Simulate a long conversation with many messages
    messages := []ir.Message{
        {Role: ir.RoleUser, Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Hello"}}, CacheControl: &ir.CacheControl{Type: "ephemeral"}},
    }
    
    // Add 42 more user-assistant pairs (total 85 messages)
    for i := 0; i < 42; i++ {
        // Add assistant with thinking
        messages = append(messages, ir.Message{
            Role: ir.RoleAssistant,
            Content: []ir.ContentPart{
                {Type: ir.ContentTypeReasoning, Reasoning: "Thinking...", ThoughtSignature: []byte("sig123")},
                {Type: ir.ContentTypeText, Text: "Response"},
            },
            CacheControl: &ir.CacheControl{Type: "ephemeral"},
        })
        // Add user message
        messages = append(messages, ir.Message{
            Role: ir.RoleUser, 
            Content: []ir.ContentPart{{Type: ir.ContentTypeText, Text: "Follow up"}},
            CacheControl: &ir.CacheControl{Type: "ephemeral"},
        })
    }
    
    // Final assistant with thinking
    messages = append(messages, ir.Message{
        Role: ir.RoleAssistant,
        Content: []ir.ContentPart{
            {Type: ir.ContentTypeReasoning, Reasoning: "Final thinking...", ThoughtSignature: []byte("sig456")},
            {Type: ir.ContentTypeText, Text: "Final response"},
        },
        CacheControl: &ir.CacheControl{Type: "ephemeral"},
    })

    req := &ir.UnifiedChatRequest{
        Model:    "claude-sonnet-4-20250514",
        Messages: messages,
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
    
    t.Logf("Total contents: %d", len(contents))

    for i, content := range contents {
        parts := content.Get("parts").Array()
        hasThinking := false
        for _, part := range parts {
            if part.Get("thought").Bool() {
                hasThinking = true
            }
            // Check that parts don't have cacheControl directly
            if part.Get("cacheControl").Exists() {
                t.Errorf("Content %d: part should NOT have cacheControl directly", i)
            }
        }
        
        // If content has thinking parts, it should NOT have cacheControl at content level
        if hasThinking && content.Get("cacheControl").Exists() {
            t.Errorf("Content %d: content with thinking parts should NOT have cacheControl", i)
        }
    }
}
