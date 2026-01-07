package provider

import (
	"context"
	"testing"
	"time"
)

func newTestState(tokensUsed, activeRequests int64) *AuthQuotaState {
	state := &AuthQuotaState{}
	state.TotalTokensUsed.Store(tokensUsed)
	state.ActiveRequests.Store(activeRequests)
	return state
}

func TestDefaultStrategy_Score(t *testing.T) {
	strategy := &DefaultStrategy{}

	config := &ProviderQuotaConfig{
		Provider:       "test",
		EstimatedLimit: 500_000,
	}

	tests := []struct {
		name           string
		state          *AuthQuotaState
		expectedBetter bool
	}{
		{
			name:           "no state has zero priority",
			state:          nil,
			expectedBetter: true,
		},
		{
			name:           "low usage has lower priority",
			state:          newTestState(100_000, 0),
			expectedBetter: true,
		},
		{
			name:           "high usage has higher priority",
			state:          newTestState(400_000, 0),
			expectedBetter: false,
		},
	}

	baseState := newTestState(200_000, 0)
	basePriority := strategy.Score(nil, baseState, config)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			priority := strategy.Score(nil, tt.state, config)

			if tt.expectedBetter && priority >= basePriority {
				t.Errorf("expected priority %d to be lower than base %d", priority, basePriority)
			}
			if !tt.expectedBetter && priority <= basePriority {
				t.Errorf("expected priority %d to be higher than base %d", priority, basePriority)
			}
		})
	}
}

func TestDefaultStrategy_Score_ActiveRequestsPenalty(t *testing.T) {
	strategy := &DefaultStrategy{}

	config := &ProviderQuotaConfig{
		Provider:       "test",
		EstimatedLimit: 500_000,
	}

	idleState := newTestState(100_000, 0)
	busyState := newTestState(100_000, 3)

	idlePriority := strategy.Score(nil, idleState, config)
	busyPriority := strategy.Score(nil, busyState, config)

	if busyPriority <= idlePriority {
		t.Errorf("busy priority %d should be higher than idle %d", busyPriority, idlePriority)
	}

	expectedMinPenalty := int64(3 * 1000)
	if busyPriority-idlePriority < expectedMinPenalty {
		t.Errorf("expected at least %d penalty for 3 active requests, got %d", expectedMinPenalty, busyPriority-idlePriority)
	}
}

func TestQuotaManager_Pick_SelectsLeastUsed(t *testing.T) {
	m := NewQuotaManager()

	auth1 := &Auth{ID: "auth1", Provider: "antigravity"}
	auth2 := &Auth{ID: "auth2", Provider: "antigravity"}
	auth3 := &Auth{ID: "auth3", Provider: "antigravity"}

	m.RecordRequestEnd("auth1", "antigravity", 1_000_000, false)
	m.RecordRequestEnd("auth2", "antigravity", 500_000, false)
	m.RecordRequestEnd("auth3", "antigravity", 10_000, false)

	selected, err := m.Pick(context.Background(), "antigravity", "claude-sonnet-4", Options{ForceRotate: true}, []*Auth{auth1, auth2, auth3})
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}

	if selected.ID != "auth3" {
		t.Errorf("expected auth3 (least usage), got %s", selected.ID)
	}
}

func TestQuotaManager_Pick_StickyBehavior(t *testing.T) {
	m := NewQuotaManager()

	auth1 := &Auth{ID: "auth1", Provider: "claude"}
	auth2 := &Auth{ID: "auth2", Provider: "claude"}

	selected1, err := m.Pick(context.Background(), "claude", "claude-sonnet-4", Options{}, []*Auth{auth1, auth2})
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}

	selected2, err := m.Pick(context.Background(), "claude", "claude-sonnet-4", Options{}, []*Auth{auth1, auth2})
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}

	if selected1.ID != selected2.ID {
		t.Errorf("sticky should return same auth: got %s then %s", selected1.ID, selected2.ID)
	}
}

func TestQuotaManager_Pick_NoStickyForAntigravity(t *testing.T) {
	m := NewQuotaManager()

	auth1 := &Auth{ID: "auth1", Provider: "antigravity"}
	auth2 := &Auth{ID: "auth2", Provider: "antigravity"}

	m.RecordRequestEnd("auth1", "antigravity", 400_000, false)

	selected, err := m.Pick(context.Background(), "antigravity", "gemini-2.5-pro", Options{ForceRotate: true}, []*Auth{auth1, auth2})
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}

	if selected.ID != "auth2" {
		t.Errorf("expected auth2 (less usage, no sticky), got %s", selected.ID)
	}
}

func TestQuotaManager_RecordQuotaHit_SetsCooldown(t *testing.T) {
	m := NewQuotaManager()

	m.RecordRequestEnd("auth1", "antigravity", 600_000, false)

	resetAfter := 30 * time.Minute
	m.RecordQuotaHit("auth1", "antigravity", "claude-sonnet-4", &resetAfter)

	state := m.GetState("auth1")
	if state == nil {
		t.Fatal("expected state to exist")
	}

	if state.CooldownUntil.IsZero() {
		t.Error("expected CooldownUntil to be set")
	}
}

func TestQuotaManager_RecordRequestStartEnd(t *testing.T) {
	m := NewQuotaManager()

	m.RecordRequestStart("auth1")

	state := m.GetState("auth1")
	if state == nil || state.ActiveRequests != 1 {
		t.Error("expected ActiveRequests to be 1 after RecordRequestStart")
	}

	m.RecordRequestEnd("auth1", "antigravity", 1000, false)

	state = m.GetState("auth1")
	if state == nil || state.ActiveRequests != 0 {
		t.Error("expected ActiveRequests to be 0 after RecordRequestEnd")
	}
	if state.TotalTokensUsed != 1000 {
		t.Errorf("expected TotalTokensUsed to be 1000, got %d", state.TotalTokensUsed)
	}
}

func TestQuotaManager_Pick_SkipsCooldown(t *testing.T) {
	m := NewQuotaManager()

	auth1 := &Auth{ID: "auth1", Provider: "antigravity"}
	auth2 := &Auth{ID: "auth2", Provider: "antigravity"}

	cooldown := 1 * time.Hour
	m.RecordQuotaHit("auth1", "antigravity", "test", &cooldown)

	selected, err := m.Pick(context.Background(), "antigravity", "test", Options{ForceRotate: true}, []*Auth{auth1, auth2})
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}

	if selected.ID != "auth2" {
		t.Errorf("expected auth2 (auth1 in cooldown), got %s", selected.ID)
	}
}

func TestQuotaManager_Pick_AllExhaustedReturnsError(t *testing.T) {
	m := NewQuotaManager()

	auth1 := &Auth{ID: "auth1", Provider: "antigravity"}

	cooldown := 1 * time.Hour
	m.RecordQuotaHit("auth1", "antigravity", "test", &cooldown)

	_, err := m.Pick(context.Background(), "antigravity", "test", Options{}, []*Auth{auth1})
	if err == nil {
		t.Fatal("expected error when all auths exhausted")
	}

	provErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if provErr.HTTPStatus != 429 {
		t.Errorf("expected 429 status, got %d", provErr.HTTPStatus)
	}
}

func TestQuotaConfig_GetProviderQuotaConfig(t *testing.T) {
	tests := []struct {
		provider     string
		expectSticky bool
	}{
		{"antigravity", false},
		{"claude", true},
		{"copilot", true},
		{"gemini", true},
		{"unknown", true},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			cfg := GetProviderQuotaConfig(tt.provider)
			if cfg.StickyEnabled != tt.expectSticky {
				t.Errorf("expected sticky %v, got %v", tt.expectSticky, cfg.StickyEnabled)
			}
		})
	}
}

func TestQuotaManager_GetStrategy(t *testing.T) {
	m := NewQuotaManager()

	tests := []struct {
		provider     string
		expectedType string
	}{
		{"antigravity", "*provider.AntigravityStrategy"},
		{"claude", "*provider.ClaudeStrategy"},
		{"copilot", "*provider.CopilotStrategy"},
		{"gemini", "*provider.GeminiStrategy"},
		{"unknown", "*provider.DefaultStrategy"},
	}

	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			strategy := m.getStrategy(tt.provider)
			strategyType := ""
			switch strategy.(type) {
			case *AntigravityStrategy:
				strategyType = "*provider.AntigravityStrategy"
			case *ClaudeStrategy:
				strategyType = "*provider.ClaudeStrategy"
			case *CopilotStrategy:
				strategyType = "*provider.CopilotStrategy"
			case *GeminiStrategy:
				strategyType = "*provider.GeminiStrategy"
			case *DefaultStrategy:
				strategyType = "*provider.DefaultStrategy"
			}
			if strategyType != tt.expectedType {
				t.Errorf("expected %s, got %s", tt.expectedType, strategyType)
			}
		})
	}
}
