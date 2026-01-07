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

type AuthQuotaState struct {
	ActiveRequests  atomic.Int64
	CooldownUntil   atomic.Int64
	TotalTokensUsed atomic.Int64
	LastExhaustedAt atomic.Int64
	LearnedLimit    atomic.Int64
	LearnedCooldown atomic.Int64

	RealQuota atomic.Pointer[RealQuotaSnapshot]
}

func (s *AuthQuotaState) GetRealQuota() *RealQuotaSnapshot {
	return s.RealQuota.Load()
}

func (s *AuthQuotaState) SetRealQuota(snapshot *RealQuotaSnapshot) {
	s.RealQuota.Store(snapshot)
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

type QuotaManager struct {
	shards     [numQuotaShards]*quotaShard
	sticky     *StickyStore
	strategies map[string]ProviderStrategy

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	refreshMu      sync.Mutex
	refreshCancels map[string]context.CancelFunc
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
		sticky:         NewStickyStore(),
		stopChan:       make(chan struct{}),
		strategies:     make(map[string]ProviderStrategy),
		refreshCancels: make(map[string]context.CancelFunc),
	}
	for i := range m.shards {
		m.shards[i] = &quotaShard{
			states: make(map[string]*AuthQuotaState),
		}
	}

	m.strategies["antigravity"] = &AntigravityStrategy{}
	m.strategies["claude"] = &ClaudeStrategy{}
	m.strategies["copilot"] = &CopilotStrategy{}
	m.strategies["gemini"] = &GeminiStrategy{}

	return m
}

func (m *QuotaManager) getStrategy(provider string) ProviderStrategy {
	if s, ok := m.strategies[provider]; ok {
		return s
	}
	return &DefaultStrategy{}
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

	m.refreshMu.Lock()
	for _, cancel := range m.refreshCancels {
		cancel()
	}
	m.refreshCancels = make(map[string]context.CancelFunc)
	m.refreshMu.Unlock()

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
	strategy := m.getStrategy(provider)

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

	selected := m.selectWithStrategy(available, config, strategy)

	if config.StickyEnabled {
		m.sticky.Set(provider+":"+model, selected.ID)
	}

	m.incrementActive(selected.ID)
	return selected, nil
}

func (m *QuotaManager) selectWithStrategy(auths []*Auth, config *ProviderQuotaConfig, strategy ProviderStrategy) *Auth {
	type scored struct {
		auth     *Auth
		priority int64
	}

	candidates := make([]scored, 0, len(auths))

	for _, auth := range auths {
		state := m.getState(auth.ID)
		priority := strategy.Score(auth, state, config)
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

func (m *QuotaManager) RecordRequestEnd(authID, provider string, tokens int64, failed bool) {
	state := m.getOrCreateState(authID)

	for {
		current := state.ActiveRequests.Load()
		if current <= 0 {
			break
		}
		if state.ActiveRequests.CompareAndSwap(current, current-1) {
			break
		}
	}

	if !failed && tokens > 0 {
		strategy := m.getStrategy(provider)
		strategy.RecordUsage(state, tokens)
	}
}

func (m *QuotaManager) RecordQuotaHit(authID, provider, model string, cooldown *time.Duration) {
	state := m.getOrCreateState(authID)
	strategy := m.getStrategy(provider)
	strategy.OnQuotaHit(state, cooldown)
}

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

func (m *QuotaManager) RegisterAuth(ctx context.Context, auth *Auth) {
	if auth == nil {
		return
	}
	strategy := m.getStrategy(auth.Provider)

	refresher, ok := strategy.(BackgroundRefresher)
	if !ok {
		return
	}

	refreshCtx, cancel := context.WithCancel(ctx)

	m.refreshMu.Lock()
	if oldCancel, exists := m.refreshCancels[auth.ID]; exists {
		oldCancel()
	}
	m.refreshCancels[auth.ID] = cancel
	m.refreshMu.Unlock()

	ch := refresher.StartRefresh(refreshCtx, auth)
	go func() {
		for snapshot := range ch {
			state := m.getOrCreateState(auth.ID)
			state.SetRealQuota(snapshot)
		}
	}()
}

func (m *QuotaManager) UnregisterAuth(authID string) {
	m.refreshMu.Lock()
	if cancel, exists := m.refreshCancels[authID]; exists {
		cancel()
		delete(m.refreshCancels, authID)
	}
	m.refreshMu.Unlock()
}
