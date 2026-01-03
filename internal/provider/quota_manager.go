package provider

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"sync"
	"time"
)

type AuthQuotaState struct {
	CooldownUntil   time.Time
	ActiveRequests  int64
	TotalTokensUsed int64
	LastExhaustedAt time.Time
	LearnedLimit    int64
	LearnedCooldown time.Duration
}

type QuotaManager struct {
	mu     sync.RWMutex
	states map[string]*AuthQuotaState
	sticky *StickyStore

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewQuotaManager() *QuotaManager {
	return &QuotaManager{
		states:   make(map[string]*AuthQuotaState),
		sticky:   NewStickyStore(),
		stopChan: make(chan struct{}),
	}
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
	m.mu.RLock()
	defer m.mu.RUnlock()

	type scored struct {
		auth     *Auth
		priority int64
	}

	candidates := make([]scored, 0, len(auths))

	for _, auth := range auths {
		state := m.states[auth.ID]
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

	priority += state.ActiveRequests * 1000

	limit := state.LearnedLimit
	if limit <= 0 {
		limit = config.EstimatedLimit
	}
	if limit > 0 {
		usagePercent := float64(state.TotalTokensUsed) / float64(limit)
		priority += int64(usagePercent * 500)
	}

	return priority
}

func (m *QuotaManager) filterAvailable(auths []*Auth, model string, now time.Time) []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()

	available := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		if auth.Disabled {
			continue
		}

		state := m.states[auth.ID]
		if state != nil && now.Before(state.CooldownUntil) {
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
	m.mu.RLock()
	defer m.mu.RUnlock()

	var earliest time.Time
	for _, auth := range auths {
		state := m.states[auth.ID]
		if state == nil {
			continue
		}
		if state.CooldownUntil.After(now) {
			if earliest.IsZero() || state.CooldownUntil.Before(earliest) {
				earliest = state.CooldownUntil
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
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.getOrCreateState(authID)
	if state.ActiveRequests > 0 {
		state.ActiveRequests--
	}

	if !failed && tokens > 0 {
		state.TotalTokensUsed += tokens
	}
}

func (m *QuotaManager) RecordQuotaHit(authID, provider, model string, cooldown *time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	state := m.getOrCreateState(authID)
	now := time.Now()

	state.LastExhaustedAt = now

	if state.TotalTokensUsed > state.LearnedLimit {
		state.LearnedLimit = state.TotalTokensUsed
	}

	if cooldown != nil && *cooldown > 0 {
		state.CooldownUntil = now.Add(*cooldown)
		state.LearnedCooldown = *cooldown
	} else if state.LearnedCooldown > 0 {
		state.CooldownUntil = now.Add(state.LearnedCooldown)
	} else {
		state.CooldownUntil = now.Add(5 * time.Hour)
	}

	state.TotalTokensUsed = 0
}

func (m *QuotaManager) incrementActive(authID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := m.getOrCreateState(authID)
	state.ActiveRequests++
}

func (m *QuotaManager) getOrCreateState(authID string) *AuthQuotaState {
	state, ok := m.states[authID]
	if !ok {
		state = &AuthQuotaState{}
		m.states[authID] = state
	}
	return state
}

func (m *QuotaManager) GetState(authID string) *AuthQuotaState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if state, ok := m.states[authID]; ok {
		copy := *state
		return &copy
	}
	return nil
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
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	maxAge := 24 * time.Hour

	for authID, state := range m.states {
		if state.ActiveRequests == 0 &&
			now.After(state.CooldownUntil) &&
			now.Sub(state.LastExhaustedAt) > maxAge {
			delete(m.states, authID)
		}
	}
}

var _ Selector = (*QuotaManager)(nil)
