package provider

import (
	"context"
	"testing"
	"time"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/usage"
)

func TestQuotaSyncPlugin_Integration(t *testing.T) {
	qm := NewQuotaManager()
	plugin := NewQuotaSyncPlugin(qm)

	ctx := context.Background()

	record := usage.Record{
		Provider: "antigravity",
		Model:    "claude-sonnet-4",
		AuthID:   "test-auth-1",
		Failed:   false,
		Usage: &ir.Usage{
			TotalTokens: 5000,
		},
	}

	plugin.HandleUsage(ctx, record)

	state := qm.GetState("test-auth-1")
	if state == nil {
		t.Fatal("expected state to be created")
	}

	if state.TotalTokensUsed != 5000 {
		t.Errorf("expected 5000 tokens, got %d", state.TotalTokensUsed)
	}
}

func TestQuotaSyncPlugin_TracksFailedRequests(t *testing.T) {
	qm := NewQuotaManager()
	plugin := NewQuotaSyncPlugin(qm)

	qm.RecordRequestStart("test-auth-2")

	record := usage.Record{
		Provider: "antigravity",
		Model:    "claude-sonnet-4",
		AuthID:   "test-auth-2",
		Failed:   true,
		Usage: &ir.Usage{
			TotalTokens: 5000,
		},
	}

	plugin.HandleUsage(context.Background(), record)

	state := qm.GetState("test-auth-2")
	if state == nil {
		t.Fatal("expected state to exist")
	}

	if state.TotalTokensUsed != 0 {
		t.Errorf("expected 0 tokens for failed request, got %d", state.TotalTokensUsed)
	}

	if state.ActiveRequests != 0 {
		t.Errorf("expected 0 active requests after failed, got %d", state.ActiveRequests)
	}
}

func TestQuotaManager_IntegrationWithManager(t *testing.T) {
	qm := NewQuotaManager()
	manager := NewManager(nil, qm, nil)

	if manager.GetQuotaManager() != qm {
		t.Error("expected GetQuotaManager to return the configured QuotaManager")
	}

	auth := &Auth{ID: "auth1", Provider: "antigravity"}
	_, _ = manager.Register(context.Background(), auth)

	result := Result{
		AuthID:   "auth1",
		Provider: "antigravity",
		Model:    "claude-sonnet-4",
		Success:  false,
		Error: &Error{
			HTTPStatus: 429,
			Message:    "rate limit exceeded",
		},
		RetryAfter: func() *time.Duration { d := 30 * time.Minute; return &d }(),
	}

	manager.MarkResult(context.Background(), result)

	state := qm.GetState("auth1")
	if state == nil {
		t.Fatal("expected state to exist after MarkResult")
	}

	if state.CooldownUntil.IsZero() {
		t.Error("expected CooldownUntil to be set after 429")
	}
}

func TestQuotaManager_DefaultSelectorIsQuotaManager(t *testing.T) {
	manager := NewManager(nil, nil, nil)

	if qm := manager.GetQuotaManager(); qm == nil {
		t.Error("expected default selector to be QuotaManager")
	}

	manager.Stop()
}
