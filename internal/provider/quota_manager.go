package provider

import (
	"context"
	"fmt"
	"hash"
	"hash/fnv"
	"math/rand/v2"
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

	RealQuota      atomic.Pointer[RealQuotaSnapshot]
	refreshTrigger chan struct{}
	triggerOnce    sync.Once
}

func (s *AuthQuotaState) GetRealQuota() *RealQuotaSnapshot {
	return s.RealQuota.Load()
}

func (s *AuthQuotaState) SetRealQuota(snapshot *RealQuotaSnapshot) {
	s.RealQuota.Store(snapshot)
}

func (s *AuthQuotaState) TriggerRefresh() {
	if s.refreshTrigger != nil {
		select {
		case s.refreshTrigger <- struct{}{}:
		default:
		}
	}
}

func (s *AuthQuotaState) GetRefreshTrigger() <-chan struct{} {
	s.triggerOnce.Do(func() {
		s.refreshTrigger = make(chan struct{}, 1)
	})
	return s.refreshTrigger
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

type scored struct {
	auth     *Auth
	priority int64
}

func (m *QuotaManager) selectWithStrategy(auths []*Auth, config *ProviderQuotaConfig, strategy ProviderStrategy) *Auth {
	candidates := make([]scored, 0, len(auths))

	for _, auth := range auths {
		state := m.getState(auth.ID)
		priority := strategy.Score(auth, state, config)
		candidates = append(candidates, scored{auth: auth, priority: priority})
	}

	// Fill-first mode: exhaust higher priority accounts before using lower priority ones
	if config.FillFirst {
		return m.selectWithFillFirst(candidates)
	}

	// Default: round-robin with score-based fallback
	return m.selectWithRoundRobin(candidates)
}

func (m *QuotaManager) selectWithFillFirst(candidates []scored) *Auth {
	if len(candidates) == 0 {
		return nil
	}

	// Sort by priority (lower = better)
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].priority < candidates[j].priority
	})

	// Group by priority levels
	type priorityGroup struct {
		priority int64
		auths    []*Auth
	}

	var groups []priorityGroup
	var currentPriority int64 = -1

	for _, c := range candidates {
		// Group accounts with similar priority (within 100 points)
		if len(groups) == 0 || c.priority-currentPriority > 100 {
			groups = append(groups, priorityGroup{priority: c.priority, auths: []*Auth{c.auth}})
			currentPriority = c.priority
		} else {
			groups[len(groups)-1].auths = append(groups[len(groups)-1].auths, c.auth)
		}
	}

	// Find the first group that has accounts with 0 active requests
	for _, g := range groups {
		for _, auth := range g.auths {
			state := m.getState(auth.ID)
			if state == nil || state.ActiveRequests.Load() == 0 {
				return auth
			}
		}
		// All accounts in this group are busy, try next priority group
	}

	// All groups busy, pick from lowest priority group with random selection
	lowestGroup := groups[len(groups)-1]
	return lowestGroup.auths[rand.IntN(len(lowestGroup.auths))]
}

func (m *QuotaManager) selectWithRoundRobin(candidates []scored) *Auth {
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
		return candidates[rand.N(similarCount)].auth
	}

	return candidates[0].auth
}

func (m *QuotaManager) filterAvailable(auths []*Auth, model string, now time.Time) []*Auth {
	available := make([]*Auth, 0, len(auths))

	for _, auth := range auths {
		if auth.Disabled {
			continue
		}

		state := m.getState(auth.ID)

		switch m.checkAvailability(state, auth, model, now) {
		case availabilityBlocked:
			continue
		case availabilityAvailable, availabilityUnknown:
			available = append(available, auth)
		}
	}
	return available
}

type availabilityStatus int

const (
	availabilityUnknown availabilityStatus = iota
	availabilityAvailable
	availabilityBlocked
)

const realQuotaFreshness = 5 * time.Minute

func (m *QuotaManager) checkAvailability(state *AuthQuotaState, auth *Auth, model string, now time.Time) availabilityStatus {
	// Check model-level blocking first (handles per-model cooldowns and quota groups)
	blocked, _, _ := isAuthBlockedForModel(auth, model, now)
	if blocked {
		return availabilityBlocked
	}

	if state != nil {
		// Check cooldown BEFORE real quota to ensure 429 cooldowns are respected
		// even if real quota fetch shows recovered quota (prevents thundering herd)
		if now.Before(state.GetCooldownUntil()) {
			return availabilityBlocked
		}

		// Real quota check: use fresh quota data for availability decisions
		if real := state.GetRealQuota(); real != nil && time.Since(real.FetchedAt) < realQuotaFreshness {
			if real.RemainingFraction <= quotaExhaustedThreshold {
				return availabilityBlocked
			}
			if real.RemainingFraction > quotaRecoveredThreshold {
				return availabilityAvailable
			}
			// Between thresholds (2%-5%): fall through to availabilityUnknown
			// This prevents flapping between available/blocked states
		}
	}

	return availabilityUnknown
}

func (m *QuotaManager) buildRetryError(auths []*Auth, now time.Time) error {
	var earliest time.Time

	for _, auth := range auths {
		state := m.getState(auth.ID)
		if state == nil {
			continue
		}

		// Check if quota has actually recovered (above exhausted threshold)
		if real := state.GetRealQuota(); real != nil {
			// If quota is above exhausted threshold and cooldown is clear, account is available
			if real.RemainingFraction > quotaExhaustedThreshold && state.GetCooldownUntil().Before(now) {
				// Account has recovered, don't include in retry error
				continue
			}
			// Account still has quota issues, calculate retry time
			if !real.WindowResetAt.IsZero() && real.WindowResetAt.After(now) {
				if earliest.IsZero() || real.WindowResetAt.Before(earliest) {
					earliest = real.WindowResetAt
				}
				continue
			}
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

	if !failed {
		// Clear cooldown on successful request - this is the counterpart to
		// RecordQuotaHit setting cooldown on 429 errors. Without this, an account
		// could stay in cooldown forever even after successful requests prove
		// the quota has been restored.
		state.SetCooldownUntil(time.Time{})

		if tokens > 0 {
			strategy := m.getStrategy(provider)
			strategy.RecordUsage(state, tokens)
		}
	}
}

func (m *QuotaManager) RecordQuotaHit(authID, provider, model string, cooldown *time.Duration) {
	state := m.getOrCreateState(authID)
	strategy := m.getStrategy(provider)
	strategy.OnQuotaHit(state, cooldown)
	state.TriggerRefresh()
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
			// Clean up if: no active requests AND cooldown is expired AND state is old
			// Also clean up if cooldown is very stale (>1 hour) even if LastExhaustedAt is recent
			if state.ActiveRequests.Load() == 0 &&
				(now.After(state.GetCooldownUntil()) ||
					now.Sub(state.GetCooldownUntil()) > time.Hour) &&
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

	state := m.getOrCreateState(auth.ID)
	ch := refresher.StartRefresh(refreshCtx, auth, state)
	go func() {
		for snapshot := range ch {
			state.SetRealQuota(snapshot)
			m.handleQuotaSnapshotUpdate(state, snapshot)
		}
	}()
}

const (
	quotaExhaustedThreshold = 0.02
	quotaRecoveredThreshold = 0.05
)

// handleQuotaSnapshotUpdate uses CAS to update cooldown based on RealQuota.
// Hysteresis: Exhausted ≤2% sets cooldown, Recovered ≥5% clears it, 2%-5% unchanged (prevents flapping).
func (m *QuotaManager) handleQuotaSnapshotUpdate(state *AuthQuotaState, snapshot *RealQuotaSnapshot) {
	if state == nil || snapshot == nil {
		return
	}

	now := time.Now()

	if snapshot.RemainingFraction <= quotaExhaustedThreshold {
		var cooldownUntil time.Time
		if !snapshot.WindowResetAt.IsZero() && snapshot.WindowResetAt.After(now) {
			cooldownUntil = snapshot.WindowResetAt
		} else {
			cooldownUntil = now.Add(5 * time.Hour)
		}
		newCooldownNs := cooldownUntil.UnixNano()

		for {
			currentNs := state.CooldownUntil.Load()
			// Allow shortening cooldown if new window reset is earlier
			// This ensures we don't block longer than necessary
			if currentNs != 0 && currentNs < newCooldownNs {
				break
			}
			if state.CooldownUntil.CompareAndSwap(currentNs, newCooldownNs) {
				state.SetLastExhaustedAt(now)
				break
			}
		}
		return
	}

	// Clear cooldown if quota has recovered above exhausted threshold
	// This ensures accounts aren't stuck in "twilight zone" between 2-5%
	if snapshot.RemainingFraction > quotaExhaustedThreshold {
		for {
			currentNs := state.CooldownUntil.Load()
			if currentNs == 0 {
				break
			}
			if state.CooldownUntil.CompareAndSwap(currentNs, 0) {
				state.TotalTokensUsed.Store(0)
				break
			}
		}
	}
}

func (m *QuotaManager) UnregisterAuth(authID string) {
	m.refreshMu.Lock()
	if cancel, exists := m.refreshCancels[authID]; exists {
		cancel()
		delete(m.refreshCancels, authID)
	}
	m.refreshMu.Unlock()
}
