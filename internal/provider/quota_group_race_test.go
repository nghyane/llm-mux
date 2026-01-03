package provider

import (
	"sync"
	"testing"
	"time"
)

// TestQuotaGroupIndexRaceCondition tests for race conditions when multiple
// goroutines attempt to create/access the quota group index simultaneously.
func TestQuotaGroupIndexRaceCondition(t *testing.T) {
	auth := &Auth{
		ID:       "test-auth",
		Provider: "antigravity",
		Runtime:  nil, // Start with nil runtime
	}

	// Register the Antigravity quota resolver
	RegisterQuotaGroupResolver("antigravity", AntigravityQuotaGroupResolver)

	var wg sync.WaitGroup
	iterations := 100

	// Simulate concurrent quota group operations
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(iteration int) {
			defer wg.Done()
			
			// Alternate between set and get operations
			if iteration%2 == 0 {
				idx := getOrCreateQuotaGroupIndex(auth)
				if idx == nil {
					t.Error("getOrCreateQuotaGroupIndex returned nil")
					return
				}
				idx.setGroupBlocked("claude", "claude-sonnet", time.Now().Add(time.Minute), time.Now().Add(time.Minute))
			} else {
				idx := getQuotaGroupIndex(auth)
				if idx != nil {
					blocked, _ := idx.isGroupBlocked("claude", time.Now())
					_ = blocked // Just check if it works
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify the index is in a valid state
	idx := getQuotaGroupIndex(auth)
	if idx == nil {
		t.Fatal("Index should exist after concurrent operations")
	}

	// Should be able to check blocked state without panic
	_, _ = idx.isGroupBlocked("claude", time.Now())
}

// TestConcurrentQuotaPropagation tests concurrent quota propagation and clearing.
func TestConcurrentQuotaPropagation(t *testing.T) {
	RegisterQuotaGroupResolver("antigravity", AntigravityQuotaGroupResolver)

	auth := &Auth{
		ID:       "test-auth",
		Provider: "antigravity",
		ModelStates: map[string]*ModelState{
			"claude-sonnet-4":  {Status: StatusActive},
			"claude-opus-4":    {Status: StatusActive},
			"claude-haiku-4":   {Status: StatusActive},
			"gemini-2.5-pro":   {Status: StatusActive},
			"gemini-2.5-flash": {Status: StatusActive},
		},
	}

	var wg sync.WaitGroup
	now := time.Now()

	// Concurrent propagation
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			quota := QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: now.Add(time.Minute),
				BackoffLevel:  1,
			}
			_ = propagateQuotaToGroup(auth, "claude-sonnet-4", quota, now.Add(time.Minute), now)
		}()
	}

	// Concurrent clearing
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond) // Slight delay to let propagation happen first
			_ = clearQuotaGroupOnSuccess(auth, "claude-opus-4", now)
		}()
	}

	wg.Wait()

	// Verify auth is in a consistent state
	if auth.ModelStates == nil {
		t.Fatal("ModelStates should not be nil")
	}

	for model, state := range auth.ModelStates {
		if state == nil {
			t.Errorf("Model %s has nil state", model)
		}
	}
}

// TestQuotaGroupIndexConsistency tests that quota index and ModelStates stay in sync.
func TestQuotaGroupIndexConsistency(t *testing.T) {
	RegisterQuotaGroupResolver("antigravity", AntigravityQuotaGroupResolver)

	auth := &Auth{
		ID:       "test-auth",
		Provider: "antigravity",
		ModelStates: map[string]*ModelState{
			"claude-sonnet-4": {Status: StatusActive},
		},
	}

	now := time.Now()
	retryTime := now.Add(time.Minute)

	// Set quota exceeded via propagation
	quota := QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		NextRecoverAt: retryTime,
		BackoffLevel:  1,
	}
	propagateQuotaToGroup(auth, "claude-sonnet-4", quota, retryTime, now)

	// Check that isAuthBlockedForModel correctly detects the block for an uninitialized model
	// in the same quota group
	blocked, _, next := isAuthBlockedForModel(auth, "claude-opus-4", now)
	if !blocked {
		t.Error("claude-opus-4 should be blocked due to quota group")
	}
	if next.IsZero() || !next.After(now) {
		t.Errorf("Expected future retry time, got %v", next)
	}

	// Clear the quota
	clearQuotaGroupOnSuccess(auth, "claude-sonnet-4", now)

	// Verify the uninitialized model is no longer blocked
	blocked, _, _ = isAuthBlockedForModel(auth, "claude-opus-4", now)
	if blocked {
		t.Error("claude-opus-4 should not be blocked after quota clear")
	}
}
