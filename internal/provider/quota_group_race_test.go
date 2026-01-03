package provider

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestQuotaGroupIndexManagerProtection tests that quota group index operations
// are properly protected by the Manager's lock, simulating real usage patterns.
func TestQuotaGroupIndexManagerProtection(t *testing.T) {
	// Simulate Manager holding lock during state updates
	var managerMu sync.RWMutex
	
	auth := &Auth{
		ID:       "test-auth",
		Provider: "antigravity",
		Runtime:  nil,
	}

	RegisterQuotaGroupResolver("antigravity", AntigravityQuotaGroupResolver)

	var wg sync.WaitGroup
	iterations := 100

	// Goroutines simulating MarkResult (write operations with full lock)
	for i := 0; i < iterations/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			
			managerMu.Lock() // Manager holds lock during state updates
			idx := getOrCreateQuotaGroupIndex(auth)
			if idx != nil {
				idx.setGroupBlocked("claude", "claude-sonnet", time.Now().Add(time.Minute), time.Now().Add(time.Minute))
			}
			managerMu.Unlock()
		}()
	}

	// Goroutines simulating selector Pick (read operations with read lock)
	for i := 0; i < iterations/2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			
			time.Sleep(time.Millisecond) // Let some writes happen first
			
			managerMu.RLock() // Selector gets auth pointer while holding read lock
			// In real code, selector receives auth pointer from pickNext
			idx := getQuotaGroupIndex(auth)
			managerMu.RUnlock()
			
			// Selector can safely call isGroupBlocked on the index it received
			// because quotaGroupIndex has its own internal mutex
			if idx != nil {
				_, _ = idx.isGroupBlocked("claude", time.Now())
			}
		}()
	}

	wg.Wait()

	// Verify the index is in a valid state
	managerMu.RLock()
	idx := getQuotaGroupIndex(auth)
	managerMu.RUnlock()
	
	if idx == nil {
		t.Fatal("Index should exist after concurrent operations")
	}

	// Should be able to check blocked state without panic
	_, _ = idx.isGroupBlocked("claude", time.Now())
}

// TestConcurrentQuotaPropagation tests concurrent quota propagation and clearing
// with proper Manager lock protection.
func TestConcurrentQuotaPropagation(t *testing.T) {
	RegisterQuotaGroupResolver("antigravity", AntigravityQuotaGroupResolver)

	var managerMu sync.RWMutex
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

	// Concurrent propagation (with lock, simulating MarkResult)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			managerMu.Lock()
			quota := QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: now.Add(time.Minute),
				BackoffLevel:  1,
			}
			_ = propagateQuotaToGroup(auth, "claude-sonnet-4", quota, now.Add(time.Minute), now)
			managerMu.Unlock()
		}()
	}

	// Concurrent clearing (with lock, simulating MarkResult)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(10 * time.Millisecond)
			managerMu.Lock()
			_ = clearQuotaGroupOnSuccess(auth, "claude-opus-4", now)
			managerMu.Unlock()
		}()
	}

	wg.Wait()

	// Verify auth is in a consistent state
	managerMu.RLock()
	if auth.ModelStates == nil {
		managerMu.RUnlock()
		t.Fatal("ModelStates should not be nil")
	}

	for model, state := range auth.ModelStates {
		if state == nil {
			managerMu.RUnlock()
			t.Errorf("Model %s has nil state", model)
			return
		}
	}
	managerMu.RUnlock()
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

// TestSelectorWithQuotaGroups tests the full workflow: Manager passes auth clones
// to selector, which safely reads quota state.
func TestSelectorWithQuotaGroups(t *testing.T) {
	RegisterQuotaGroupResolver("antigravity", AntigravityQuotaGroupResolver)
	
	var managerMu sync.RWMutex
	auth := &Auth{
		ID:       "test-auth",
		Provider: "antigravity",
		ModelStates: map[string]*ModelState{
			"claude-sonnet-4": {Status: StatusActive},
		},
	}

	selector := &RoundRobinSelector{}
	selector.Start()
	defer selector.Stop()

	var wg sync.WaitGroup

	// Goroutine 1: Continuously set quota (simulating MarkResult)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			managerMu.Lock()
			quota := QuotaState{
				Exceeded:      true,
				Reason:        "quota",
				NextRecoverAt: time.Now().Add(time.Second),
				BackoffLevel:  1,
			}
			propagateQuotaToGroup(auth, "claude-sonnet-4", quota, time.Now().Add(time.Second), time.Now())
			managerMu.Unlock()
			time.Sleep(time.Millisecond)
		}
	}()

	// Goroutine 2: Continuously pick auth (simulating concurrent requests)
	// This mimics what pickNext does: clone auth under RLock before passing to selector
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			managerMu.RLock()
			// Manager clones auth before passing to selector (like pickNext does after our fix)
			authClone := auth.Clone()
			auths := []*Auth{authClone}
			managerMu.RUnlock()
			
			// Selector picks and checks if auth is blocked
			_, err := selector.Pick(context.Background(), "antigravity", "claude-opus-4", Options{}, auths)
			_ = err // Error is expected when quota is exceeded
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
}
