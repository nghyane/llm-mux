package provider

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAuthRegistry_RegisterAndGet(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)

	auth := &Auth{
		ID:       "test-id-1",
		Provider: "claude",
		Label:    "Test Auth",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "test@example.com"},
	}

	ctx := context.Background()
	result, err := registry.Register(ctx, auth)
	if err != nil {
		t.Fatalf("Register failed: %v", err)
	}
	if result.ID != "test-id-1" {
		t.Errorf("Expected ID test-id-1, got %s", result.ID)
	}

	entry := registry.GetEntry("test-id-1")
	if entry == nil {
		t.Fatal("GetEntry returned nil")
	}
	if entry.Provider() != "claude" {
		t.Errorf("Expected provider claude, got %s", entry.Provider())
	}

	authResult := registry.Get("test-id-1")
	if authResult == nil {
		t.Fatal("Get returned nil")
	}
	if authResult.Label != "Test Auth" {
		t.Errorf("Expected label 'Test Auth', got %s", authResult.Label)
	}

	if registry.Count() != 1 {
		t.Errorf("Expected count 1, got %d", registry.Count())
	}
}

func TestAuthRegistry_Update(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{
		ID:       "update-test",
		Provider: "gemini",
		Label:    "Original",
		Status:   StatusActive,
	}
	_, _ = registry.Register(ctx, auth)

	auth.Label = "Updated"
	auth.Status = StatusError
	_, err := registry.Update(ctx, auth)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	entry := registry.GetEntry("update-test")
	meta := entry.Metadata()
	if meta.Label != "Updated" {
		t.Errorf("Expected label 'Updated', got %s", meta.Label)
	}
	if meta.Status != StatusError {
		t.Errorf("Expected status StatusError, got %v", meta.Status)
	}
}

func TestAuthRegistry_Delete(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{ID: "delete-test", Provider: "copilot"}
	_, _ = registry.Register(ctx, auth)

	if registry.Count() != 1 {
		t.Fatalf("Expected count 1 before delete")
	}

	err := registry.Delete(ctx, "delete-test")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	if registry.Count() != 0 {
		t.Errorf("Expected count 0 after delete, got %d", registry.Count())
	}

	if registry.GetEntry("delete-test") != nil {
		t.Error("Expected nil after delete")
	}
}

func TestAuthRegistry_List(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		auth := &Auth{
			ID:       "list-" + string(rune('a'+i)),
			Provider: "claude",
		}
		_, _ = registry.Register(ctx, auth)
	}

	list := registry.List()
	if len(list) != 10 {
		t.Errorf("Expected 10 items, got %d", len(list))
	}

	entries := registry.ListEntries()
	if len(entries) != 10 {
		t.Errorf("Expected 10 entries, got %d", len(entries))
	}
}

func TestAuthEntry_ConcurrentMetadataUpdates(t *testing.T) {
	auth := &Auth{
		ID:       "concurrent-test",
		Provider: "claude",
		Status:   StatusActive,
	}
	entry := NewAuthEntry(auth)

	var wg sync.WaitGroup
	updates := 100

	for i := 0; i < updates; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
				return old.WithStatus(StatusActive, "update-"+string(rune('0'+idx%10)))
			})
		}(i)
	}

	wg.Wait()

	meta := entry.Metadata()
	if meta.Status != StatusActive {
		t.Errorf("Expected StatusActive, got %v", meta.Status)
	}
}

func TestAuthEntry_ConcurrentQuotaUpdates(t *testing.T) {
	auth := &Auth{ID: "quota-test", Provider: "gemini"}
	entry := NewAuthEntry(auth)

	var wg sync.WaitGroup
	iterations := 1000

	for i := 0; i < iterations; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			entry.IncrementActiveRequests()
		}()
		go func() {
			defer wg.Done()
			entry.DecrementActiveRequests()
		}()
	}

	wg.Wait()

	active := entry.Quota.ActiveRequests.Load()
	if active < 0 {
		t.Errorf("Active requests went negative: %d", active)
	}
}

func TestAuthEntry_ModelStates(t *testing.T) {
	auth := &Auth{
		ID:       "model-state-test",
		Provider: "antigravity",
	}
	entry := NewAuthEntry(auth)

	now := time.Now()
	entry.UpdateModelState("claude-3-opus", func(old ModelStateSnapshot) ModelStateSnapshot {
		return ModelStateSnapshot{
			Status:        StatusError,
			QuotaExceeded: true,
			QuotaRecover:  now.Add(time.Hour).UnixNano(),
			UpdatedAt:     now.UnixNano(),
		}
	})

	states := entry.ModelStates()
	state, ok := states.Get("claude-3-opus")
	if !ok {
		t.Fatal("Model state not found")
	}
	if !state.QuotaExceeded {
		t.Error("Expected QuotaExceeded to be true")
	}
	if state.Status != StatusError {
		t.Errorf("Expected StatusError, got %v", state.Status)
	}

	entry.UpdateModelState("claude-3-opus", func(old ModelStateSnapshot) ModelStateSnapshot {
		return ModelStateSnapshot{
			Status:    StatusActive,
			UpdatedAt: time.Now().UnixNano(),
		}
	})

	states = entry.ModelStates()
	state, _ = states.Get("claude-3-opus")
	if state.QuotaExceeded {
		t.Error("Expected QuotaExceeded to be cleared")
	}
}

func TestAuthEntry_IsBlockedForModel(t *testing.T) {
	auth := &Auth{
		ID:       "blocked-test",
		Provider: "claude",
	}
	entry := NewAuthEntry(auth)
	now := time.Now()

	blocked, reason, _ := entry.IsBlockedForModel("claude-3-opus", now)
	if blocked {
		t.Error("Should not be blocked initially")
	}
	if reason != blockReasonNone {
		t.Errorf("Expected blockReasonNone, got %v", reason)
	}

	entry.UpdateModelState("claude-3-opus", func(old ModelStateSnapshot) ModelStateSnapshot {
		return ModelStateSnapshot{
			Status:         StatusError,
			Unavailable:    true,
			QuotaExceeded:  true,
			NextRetryAfter: now.Add(time.Hour).UnixNano(),
			QuotaRecover:   now.Add(time.Hour).UnixNano(),
		}
	})

	blocked, reason, retryAt := entry.IsBlockedForModel("claude-3-opus", now)
	if !blocked {
		t.Error("Should be blocked after quota exceeded")
	}
	if reason != blockReasonCooldown {
		t.Errorf("Expected blockReasonCooldown, got %v", reason)
	}
	if retryAt.IsZero() {
		t.Error("Expected non-zero retry time")
	}
}

func TestAuthEntry_Cooldown(t *testing.T) {
	auth := &Auth{ID: "cooldown-test", Provider: "copilot"}
	entry := NewAuthEntry(auth)
	now := time.Now()

	if entry.IsInCooldown(now) {
		t.Error("Should not be in cooldown initially")
	}

	entry.SetCooldown(now.Add(time.Hour))

	if !entry.IsInCooldown(now) {
		t.Error("Should be in cooldown after SetCooldown")
	}
	if !entry.IsUnavailable() {
		t.Error("Should be unavailable after SetCooldown")
	}

	entry.ClearCooldown()

	if entry.IsInCooldown(now) {
		t.Error("Should not be in cooldown after ClearCooldown")
	}
	if entry.IsUnavailable() {
		t.Error("Should be available after ClearCooldown")
	}
}

func TestAuthEntry_ToAuth(t *testing.T) {
	original := &Auth{
		ID:         "toauth-test",
		Provider:   "gemini",
		Label:      "Test Label",
		Status:     StatusActive,
		Disabled:   false,
		Metadata:   map[string]any{"key": "value"},
		Attributes: map[string]string{"attr": "val"},
		ModelStates: map[string]*ModelState{
			"model-1": {
				Status:      StatusActive,
				Unavailable: false,
			},
		},
	}
	entry := NewAuthEntry(original)

	result := entry.ToAuth()

	if result.ID != original.ID {
		t.Errorf("ID mismatch: %s vs %s", result.ID, original.ID)
	}
	if result.Provider != original.Provider {
		t.Errorf("Provider mismatch")
	}
	if result.Label != original.Label {
		t.Errorf("Label mismatch")
	}
	if result.Status != original.Status {
		t.Errorf("Status mismatch")
	}
	if result.Metadata["key"] != "value" {
		t.Errorf("Metadata not preserved")
	}
	if result.Attributes["attr"] != "val" {
		t.Errorf("Attributes not preserved")
	}
	if result.ModelStates == nil || result.ModelStates["model-1"] == nil {
		t.Error("ModelStates not preserved")
	}
}

func TestAuthRegistry_Pick(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		auth := &Auth{
			ID:       "pick-" + string(rune('a'+i)),
			Provider: "claude",
			Status:   StatusActive,
		}
		_, _ = registry.Register(ctx, auth)
	}

	entries := registry.ListByProvider("claude")
	if len(entries) != 3 {
		t.Fatalf("Expected 3 entries, got %d", len(entries))
	}

	selected, err := registry.Pick(ctx, "claude", "claude-3-opus", Options{}, entries)
	if err != nil {
		t.Fatalf("Pick failed: %v", err)
	}
	if selected == nil {
		t.Fatal("Pick returned nil")
	}

	if selected.Quota.ActiveRequests.Load() != 1 {
		t.Error("Expected ActiveRequests to be incremented")
	}
}

func TestAuthRegistry_PickAllCooldown(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	now := time.Now()
	for i := 0; i < 3; i++ {
		auth := &Auth{
			ID:       "cooldown-pick-" + string(rune('a'+i)),
			Provider: "claude",
			Status:   StatusActive,
		}
		_, _ = registry.Register(ctx, auth)
		entry := registry.GetEntry(auth.ID)
		entry.SetCooldown(now.Add(time.Hour))
	}

	entries := registry.ListByProvider("claude")
	_, err := registry.Pick(ctx, "claude", "claude-3-opus", Options{}, entries)
	if err == nil {
		t.Error("Expected error when all auths in cooldown")
	}
	mcErr, ok := err.(*modelCooldownError)
	if !ok {
		t.Errorf("Expected modelCooldownError, got %T", err)
	}
	if mcErr.model != "claude-3-opus" {
		t.Errorf("Expected model claude-3-opus, got %s", mcErr.model)
	}
}

func TestAuthRegistry_MarkResultSuccess(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{
		ID:       "mark-success",
		Provider: "claude",
		Status:   StatusError,
	}
	_, _ = registry.Register(ctx, auth)

	entry := registry.GetEntry("mark-success")
	entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
		return old.WithError(&Error{Message: "previous error"})
	})

	registry.MarkResult(ctx, Result{
		AuthID:   "mark-success",
		Provider: "claude",
		Model:    "claude-3-opus",
		Success:  true,
	})

	meta := entry.Metadata()
	if meta.Status != StatusActive {
		t.Errorf("Expected StatusActive after success, got %v", meta.Status)
	}
	if meta.LastError != nil {
		t.Error("Expected LastError to be cleared")
	}
}

func TestAuthRegistry_MarkResultFailure(t *testing.T) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{
		ID:       "mark-failure",
		Provider: "claude",
		Status:   StatusActive,
	}
	_, _ = registry.Register(ctx, auth)

	registry.MarkResult(ctx, Result{
		AuthID:   "mark-failure",
		Provider: "claude",
		Model:    "claude-3-opus",
		Success:  false,
		Error: &Error{
			Code:       "quota_exceeded",
			Message:    "Rate limit exceeded",
			HTTPStatus: 429,
		},
	})

	entry := registry.GetEntry("mark-failure")
	states := entry.ModelStates()
	state, ok := states.Get("claude-3-opus")
	if !ok {
		t.Fatal("Model state not created")
	}
	if !state.QuotaExceeded {
		t.Error("Expected QuotaExceeded to be set")
	}
	if state.Status != StatusError {
		t.Errorf("Expected StatusError, got %v", state.Status)
	}
}

func TestAuthMetadata_Clone(t *testing.T) {
	original := &AuthMetadata{
		Label:      "Test",
		Status:     StatusActive,
		Attributes: map[string]string{"key": "value"},
		Metadata:   map[string]any{"token": "secret"},
		LastError:  &Error{Message: "error"},
	}

	clone := original.Clone()

	clone.Label = "Modified"
	clone.Attributes["key"] = "modified"
	clone.Metadata["token"] = "modified"

	if original.Label != "Test" {
		t.Error("Original label was modified")
	}
	if original.Attributes["key"] != "value" {
		t.Error("Original attributes was modified")
	}
	if original.Metadata["token"] != "secret" {
		t.Error("Original metadata was modified")
	}
}

func TestModelStatesSnapshot_With(t *testing.T) {
	original := &ModelStatesSnapshot{
		States: map[string]ModelStateSnapshot{
			"model-1": {Status: StatusActive},
		},
	}

	updated := original.With("model-2", ModelStateSnapshot{Status: StatusError})

	if _, ok := original.States["model-2"]; ok {
		t.Error("Original should not contain model-2")
	}

	if state, ok := updated.States["model-2"]; !ok || state.Status != StatusError {
		t.Error("Updated should contain model-2 with StatusError")
	}

	if state, ok := updated.States["model-1"]; !ok || state.Status != StatusActive {
		t.Error("Updated should still contain model-1")
	}
}

func BenchmarkAuthRegistry_GetEntry(b *testing.B) {
	registry := NewAuthRegistry(nil, nil, nil)
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		auth := &Auth{
			ID:       "bench-" + string(rune(i)),
			Provider: "claude",
		}
		_, _ = registry.Register(ctx, auth)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			registry.GetEntry("bench-" + string(rune(i%1000)))
			i++
		}
	})
}

func BenchmarkAuthEntry_UpdateMetadata(b *testing.B) {
	auth := &Auth{ID: "bench", Provider: "claude"}
	entry := NewAuthEntry(auth)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
				return old.WithStatus(StatusActive, "ok")
			})
		}
	})
}

func BenchmarkAuthEntry_QuotaOperations(b *testing.B) {
	auth := &Auth{ID: "bench", Provider: "claude"}
	entry := NewAuthEntry(auth)

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			entry.IncrementActiveRequests()
			entry.DecrementActiveRequests()
		}
	})
}
