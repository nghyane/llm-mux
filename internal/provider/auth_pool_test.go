package provider

import (
	"context"
	"testing"
	"time"
)

func TestAuthPool_RegisterAndGet(t *testing.T) {
	pool := NewAuthPool()
	defer pool.Stop()

	auth := &Auth{ID: "test-1", Provider: "antigravity"}
	pool.Register(auth, nil)

	entry := pool.GetEntry("test-1")
	if entry == nil {
		t.Fatal("expected entry to exist")
	}
	if entry.Auth.ID != "test-1" {
		t.Errorf("expected auth ID 'test-1', got '%s'", entry.Auth.ID)
	}
}

func TestAuthPool_Pick_SelectsLeastActive(t *testing.T) {
	pool := NewAuthPool()
	defer pool.Stop()

	auth1 := &Auth{ID: "busy", Provider: "antigravity"}
	auth2 := &Auth{ID: "idle", Provider: "antigravity"}

	pool.Register(auth1, nil)
	pool.Register(auth2, nil)

	entry1 := pool.GetEntry("busy")
	entry1.SetToken("valid", time.Now().Add(time.Hour), time.Now().Add(30*time.Minute))
	entry1.activeRequests.Store(5)

	entry2 := pool.GetEntry("idle")
	entry2.SetToken("valid", time.Now().Add(time.Hour), time.Now().Add(30*time.Minute))
	entry2.activeRequests.Store(0)

	picked, err := pool.Pick("antigravity", "test-model")
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}

	if picked.Auth.ID != "idle" {
		t.Errorf("expected idle auth to be selected, got %s", picked.Auth.ID)
	}
}

func TestAuthPool_Pick_NoReadyAuth(t *testing.T) {
	pool := NewAuthPool()
	defer pool.Stop()

	auth := &Auth{ID: "expired", Provider: "antigravity"}
	pool.Register(auth, nil)

	_, err := pool.Pick("antigravity", "test-model")
	if err == nil {
		t.Fatal("expected error when no ready auth")
	}

	provErr, ok := err.(*Error)
	if !ok {
		t.Fatalf("expected *Error, got %T", err)
	}
	if provErr.Code != "no_ready_auth" {
		t.Errorf("expected code 'no_ready_auth', got '%s'", provErr.Code)
	}
}

func TestAuthEntry_Ready(t *testing.T) {
	entry := &AuthEntry{
		Auth: &Auth{ID: "test"},
	}

	if entry.Ready(0) {
		t.Error("expected entry without token to not be ready")
	}

	entry.SetToken("valid", time.Now().Add(time.Hour), time.Now().Add(30*time.Minute))
	if !entry.Ready(30 * time.Second) {
		t.Error("expected entry with valid token to be ready")
	}

	entry.SetToken("expired", time.Now().Add(-time.Hour), time.Now().Add(-2*time.Hour))
	if entry.Ready(0) {
		t.Error("expected entry with expired token to not be ready")
	}
}

func TestAuthEntry_Cooldown(t *testing.T) {
	entry := &AuthEntry{
		Auth: &Auth{ID: "test"},
	}
	entry.SetToken("valid", time.Now().Add(time.Hour), time.Now().Add(30*time.Minute))

	if !entry.Ready(0) {
		t.Error("expected entry to be ready before cooldown")
	}

	entry.SetCooldown(time.Now().Add(time.Hour))
	if entry.Ready(0) {
		t.Error("expected entry in cooldown to not be ready")
	}
}

func TestAuthPool_GetReady_FiltersCorrectly(t *testing.T) {
	pool := NewAuthPool()
	defer pool.Stop()

	auth1 := &Auth{ID: "ready", Provider: "antigravity"}
	auth2 := &Auth{ID: "expired", Provider: "antigravity"}
	auth3 := &Auth{ID: "disabled", Provider: "antigravity", Disabled: true}
	auth4 := &Auth{ID: "other-provider", Provider: "claude"}

	pool.Register(auth1, nil)
	pool.Register(auth2, nil)
	pool.Register(auth3, nil)
	pool.Register(auth4, nil)

	pool.GetEntry("ready").SetToken("valid", time.Now().Add(time.Hour), time.Now().Add(30*time.Minute))
	pool.GetEntry("other-provider").SetToken("valid", time.Now().Add(time.Hour), time.Now().Add(30*time.Minute))

	ready := pool.GetReady("antigravity", "test", 30*time.Second)
	if len(ready) != 1 {
		t.Errorf("expected 1 ready auth, got %d", len(ready))
	}
	if len(ready) > 0 && ready[0].Auth.ID != "ready" {
		t.Errorf("expected 'ready' auth, got '%s'", ready[0].Auth.ID)
	}
}

func TestAuthPool_Refresh(t *testing.T) {
	pool := NewAuthPool()

	refreshCalled := false
	refreshFn := func(ctx context.Context, auth *Auth) (string, time.Duration, error) {
		refreshCalled = true
		return "new-token", time.Hour, nil
	}

	auth := &Auth{ID: "test", Provider: "antigravity"}
	pool.Register(auth, refreshFn)

	entry := pool.GetEntry("test")
	entry.SetToken("old-token", time.Now().Add(-time.Hour), time.Now().Add(-2*time.Hour))

	pool.doRefresh(entry)

	if !refreshCalled {
		t.Error("expected refresh function to be called")
	}
	if entry.GetToken() != "new-token" {
		t.Errorf("expected token 'new-token', got '%s'", entry.GetToken())
	}
}
