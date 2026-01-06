package provider

import (
	"context"
	"fmt"
	"hash"
	"hash/fnv"
	"math/rand"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// AuthQuotaState tracks quota and rate limit state for a single auth.
// ActiveRequests uses atomic operations for lock-free increment/decrement.
type AuthQuotaState struct {
	ActiveRequests  atomic.Int64
	CooldownUntil   atomic.Int64 // Unix nano timestamp
	TotalTokensUsed atomic.Int64
	LastExhaustedAt atomic.Int64 // Unix nano timestamp
	LearnedLimit    atomic.Int64
	LearnedCooldown atomic.Int64 // Duration in nanoseconds
}

// GetCooldownUntil returns the cooldown deadline as time.Time.
func (s *AuthQuotaState) GetCooldownUntil() time.Time {
	ns := s.CooldownUntil.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// SetCooldownUntil sets the cooldown deadline.
func (s *AuthQuotaState) SetCooldownUntil(t time.Time) {
	if t.IsZero() {
		s.CooldownUntil.Store(0)
	} else {
		s.CooldownUntil.Store(t.UnixNano())
	}
}

// GetLastExhaustedAt returns the last exhausted time.
func (s *AuthQuotaState) GetLastExhaustedAt() time.Time {
	ns := s.LastExhaustedAt.Load()
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

// SetLastExhaustedAt sets the last exhausted time.
func (s *AuthQuotaState) SetLastExhaustedAt(t time.Time) {
	if t.IsZero() {
		s.LastExhaustedAt.Store(0)
	} else {
		s.LastExhaustedAt.Store(t.UnixNano())
	}
}

// GetLearnedCooldown returns the learned cooldown duration.
func (s *AuthQuotaState) GetLearnedCooldown() time.Duration {
	return time.Duration(s.LearnedCooldown.Load())
}

// SetLearnedCooldown sets the learned cooldown duration.
func (s *AuthQuotaState) SetLearnedCooldown(d time.Duration) {
	s.LearnedCooldown.Store(int64(d))
}

// AuthQuotaStateSnapshot is a point-in-time copy of AuthQuotaState for external use.
// All fields are regular values (not atomics) for easy consumption.
type AuthQuotaStateSnapshot struct {
	CooldownUntil   time.Time
	ActiveRequests  int64
	TotalTokensUsed int64
	LastExhaustedAt time.Time
	LearnedLimit    int64
	LearnedCooldown time.Duration
}

// Snapshot creates a point-in-time snapshot of the state.
func (s *AuthQuotaState) Snapshot() *AuthQuotaStateSnapshot {
	return &AuthQuotaStateSnapshot{
		CooldownUntil:   s.GetCooldownUntil(),
		ActiveRequests:  s.ActiveRequests.Load(),
		TotalTokensUsed: s.TotalTokensUsed.Load(),
		LastExhaustedAt: s.GetLastExhaustedAt(),
		LearnedLimit:    s.LearnedLimit.Load(),
		LearnedCooldown: s.GetLearnedCooldown(),
	}
}

const (
	numQuotaShards = 32 // More shards than StickyStore since this is hotter
)

// quotaShard holds a subset of auth states.
type quotaShard struct {
	mu     sync.RWMutex
	states map[string]*AuthQuotaState
}

// QuotaManager provides sharded, low-contention quota tracking.
type QuotaManager struct {
	shards [numQuotaShards]*quotaShard
	sticky *StickyStore

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

var quotaHasherPool = sync.Pool{
	New: func() any { return fnv.New64a() },
}

func quotaHashKey(key string) uint64 {
	h := quotaHasherPool.Get().(hash.Hash64)
	h.Reset()
	h.Write([]byte(key))
	sum := h.Sum64()
	quotaHasherPool.Put(h)
	return sum
}

func NewQuotaManager() *QuotaManager {
	m := &QuotaManager{
		sticky:   NewStickyStore(),
		stopChan: make(chan struct{}),
	}
	for i := range m.shards {
		m.shards[i] = &quotaShard{
			states: make(map[string]*AuthQuotaState),
		}
	}
	return m
}

func (m *QuotaManager) getShard(authID string) *quotaShard {
	return m.shards[quotaHashKey(authID)%numQuotaShards]
}

func (m *QuotaManager) Start() {
	m.sticky.Start()
	m.wg.Add(1)
	go m.cleanupLoop()
}

func (m *QuotaManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopChan)
	})
	m.sticky.Stop()
	m.wg.Wait()
}

func (m *QuotaManager) Pick(
	ctx context.Context,
	provider, model string,
	opts Options,
	auths []*Auth,
) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	now := time.Now()
	config := GetProviderQuotaConfig(provider)

	available := m.filterAvailable(auths, model, now)
	if len(available) == 0 {
		return nil, m.buildRetryError(auths, now)
	}

	if len(available) == 1 {
		m.incrementActive(available[0].ID)
		return available[0], nil
	}

	if config.StickyEnabled && !opts.ForceRotate {
		key := provider + ":" + model
		if authID, ok := m.sticky.Get(key); ok {
			for _, auth := range available {
				if auth.ID == authID {
					m.incrementActive(auth.ID)
					return auth, nil
				}
			}
		}
	}

	selected := m.selectOptimal(available, config)

	if config.StickyEnabled {
		m.sticky.Set(provider+":"+model, selected.ID)
	}

	m.incrementActive(selected.ID)
	return selected, nil
}

func (m *QuotaManager) selectOptimal(auths []*Auth, config *ProviderQuotaConfig) *Auth {
	type scored struct {
		auth     *Auth
		priority int64
	}

	candidates := make([]scored, 0, len(auths))

	// Gather scores - each state access is lock-free via atomics
	for _, auth := range auths {
		state := m.getState(auth.ID)
		priority := m.calculatePriority(state, config)
		candidates = append(candidates, scored{auth: auth, priority: priority})
	}

	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})

	topN := 3
	if len(candidates) < topN {
		topN = len(candidates)
	}

	minPriority := candidates[0].priority
	similarCount := 0
	for i := 0; i < topN; i++ {
		if candidates[i].priority-minPriority < 100 {
			similarCount++
		}
	}

	if similarCount > 1 {
		return candidates[rand.Intn(similarCount)].auth
	}

	return candidates[0].auth
}

func (m *QuotaManager) calculatePriority(state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
	if state == nil {
		return 0
	}

	var priority int64

	// Lock-free read of active requests
	priority += state.ActiveRequests.Load() * 1000

	limit := state.LearnedLimit.Load()
	if limit <= 0 {
		limit = config.EstimatedLimit
	}
	if limit > 0 {
		tokensUsed := state.TotalTokensUsed.Load()
		usagePercent := float64(tokensUsed) / float64(limit)
		priority += int64(usagePercent * 500)
	}

	return priority
}

func (m *QuotaManager) filterAvailable(auths []*Auth, model string, now time.Time) []*Auth {
	available := make([]*Auth, 0, len(auths))

	for _, auth := range auths {
		if auth.Disabled {
			continue
		}

		// Lock-free check of cooldown via atomic
		state := m.getState(auth.ID)
		if state != nil && now.Before(state.GetCooldownUntil()) {
			continue
		}

		blocked, _, _ := isAuthBlockedForModel(auth, model, now)
		if blocked {
			continue
		}

		available = append(available, auth)
	}
	return available
}

func (m *QuotaManager) buildRetryError(auths []*Auth, now time.Time) error {
	var earliest time.Time

	for _, auth := range auths {
		state := m.getState(auth.ID)
		if state == nil {
			continue
		}
		cooldownUntil := state.GetCooldownUntil()
		if cooldownUntil.After(now) {
			if earliest.IsZero() || cooldownUntil.Before(earliest) {
				earliest = cooldownUntil
			}
		}
	}

	if !earliest.IsZero() {
		retryAfter := earliest.Sub(now)
		return &Error{
			Code:       "quota_exhausted",
			Message:    fmt.Sprintf("all accounts exhausted, retry after %.0fs", retryAfter.Seconds()),
			HTTPStatus: 429,
		}
	}

	return &Error{Code: "auth_unavailable", Message: "no auth available"}
}

func (m *QuotaManager) RecordRequestStart(authID string) {
	m.incrementActive(authID)
}

func (m *QuotaManager) RecordRequestEnd(authID string, tokens int64, failed bool) {
	state := m.getOrCreateState(authID)

	// Atomic decrement with CAS loop to prevent going negative
	for {
		current := state.ActiveRequests.Load()
		if current <= 0 {
			break
		}
		if state.ActiveRequests.CompareAndSwap(current, current-1) {
			break
		}
	}

	// Lock-free token update
	if !failed && tokens > 0 {
		state.TotalTokensUsed.Add(tokens)
	}
}

func (m *QuotaManager) RecordQuotaHit(authID, provider, model string, cooldown *time.Duration) {
	state := m.getOrCreateState(authID)
	now := time.Now()

	state.SetLastExhaustedAt(now)

	// Update learned limit if we exceeded it
	tokensUsed := state.TotalTokensUsed.Load()
	for {
		currentLimit := state.LearnedLimit.Load()
		if tokensUsed <= currentLimit {
			break
		}
		if state.LearnedLimit.CompareAndSwap(currentLimit, tokensUsed) {
			break
		}
	}

	// Set cooldown
	if cooldown != nil && *cooldown > 0 {
		state.SetCooldownUntil(now.Add(*cooldown))
		state.SetLearnedCooldown(*cooldown)
	} else if learned := state.GetLearnedCooldown(); learned > 0 {
		state.SetCooldownUntil(now.Add(learned))
	} else {
		state.SetCooldownUntil(now.Add(5 * time.Hour))
	}

	// Reset token counter
	state.TotalTokensUsed.Store(0)
}

// incrementActive atomically increments the active request counter.
func (m *QuotaManager) incrementActive(authID string) {
	state := m.getOrCreateState(authID)
	state.ActiveRequests.Add(1)
}

// getState returns the state for the given auth ID, or nil if not found.
// This is lock-free for the common case where state exists.
func (m *QuotaManager) getState(authID string) *AuthQuotaState {
	shard := m.getShard(authID)
	shard.mu.RLock()
	state := shard.states[authID]
	shard.mu.RUnlock()
	return state
}

// getOrCreateState returns the state for the given auth ID, creating it if needed.
// Uses optimistic locking: tries RLock first, upgrades to Lock only if creation needed.
func (m *QuotaManager) getOrCreateState(authID string) *AuthQuotaState {
	shard := m.getShard(authID)

	// Fast path: state already exists
	shard.mu.RLock()
	state, ok := shard.states[authID]
	shard.mu.RUnlock()

	if ok {
		return state
	}

	// Slow path: need to create state
	shard.mu.Lock()
	// Double-check after acquiring write lock
	state, ok = shard.states[authID]
	if !ok {
		state = &AuthQuotaState{}
		shard.states[authID] = state
	}
	shard.mu.Unlock()

	return state
}

// GetState returns a snapshot of the state for external use.
func (m *QuotaManager) GetState(authID string) *AuthQuotaStateSnapshot {
	state := m.getState(authID)
	if state == nil {
		return nil
	}
	return state.Snapshot()
}

func (m *QuotaManager) cleanupLoop() {
	defer m.wg.Done()
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.stopChan:
			return
		case <-ticker.C:
			m.cleanup()
		}
	}
}

func (m *QuotaManager) cleanup() {
	now := time.Now()
	maxAge := 24 * time.Hour

	// Clean up each shard independently to minimize lock contention
	for _, shard := range m.shards {
		shard.mu.Lock()
		for authID, state := range shard.states {
			if state.ActiveRequests.Load() == 0 &&
				now.After(state.GetCooldownUntil()) &&
				now.Sub(state.GetLastExhaustedAt()) > maxAge {
				delete(shard.states, authID)
			}
		}
		shard.mu.Unlock()
	}
}

var _ Selector = (*QuotaManager)(nil)
