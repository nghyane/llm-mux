package provider

import (
	"context"
	"sort"
	"sync"
	"time"
)

const (
	defaultQuotaWindow        = 5 * time.Hour
	quotaStatsCleanupInterval = 10 * time.Minute
	unknownResetPenalty       = 60 * time.Minute
)

type QuotaAwareSelector struct {
	mu          sync.RWMutex
	authStats   map[string]map[string]*AuthQuotaStats
	quotaWindow time.Duration
	sticky      *StickyStore

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

type AuthQuotaStats struct {
	RequestCount   int64
	WindowStart    time.Time
	QuotaExhausted bool
	QuotaResetAt   time.Time
}

func NewQuotaAwareSelector(quotaWindow time.Duration) *QuotaAwareSelector {
	if quotaWindow <= 0 {
		quotaWindow = defaultQuotaWindow
	}
	return &QuotaAwareSelector{
		authStats:   make(map[string]map[string]*AuthQuotaStats),
		quotaWindow: quotaWindow,
		sticky:      NewStickyStore(),
		stopChan:    make(chan struct{}),
	}
}

func (s *QuotaAwareSelector) Start() {
	s.sticky.Start()
	s.wg.Add(1)
	go s.cleanupLoop()
}

func (s *QuotaAwareSelector) Stop() {
	s.stopOnce.Do(func() {
		close(s.stopChan)
	})
	s.sticky.Stop()
	s.wg.Wait()
}

func (s *QuotaAwareSelector) cleanupLoop() {
	defer s.wg.Done()
	ticker := time.NewTicker(quotaStatsCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-s.stopChan:
			return
		case <-ticker.C:
			s.cleanupExpiredStats()
		}
	}
}

func (s *QuotaAwareSelector) cleanupExpiredStats() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	for authID, groups := range s.authStats {
		for group, stats := range groups {
			if now.Sub(stats.WindowStart) >= s.quotaWindow {
				delete(groups, group)
			}
		}
		if len(groups) == 0 {
			delete(s.authStats, authID)
		}
	}
}

func (s *QuotaAwareSelector) Pick(
	ctx context.Context,
	provider, model string,
	opts Options,
	auths []*Auth,
) (*Auth, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	now := time.Now()

	available := s.filterAvailable(auths, model, now)
	if len(available) == 0 {
		return nil, s.buildCooldownError(auths, model, provider, now)
	}

	if len(available) == 1 {
		s.recordRequest(available[0].ID, provider, model, now)
		return available[0], nil
	}

	key := provider + ":" + model
	if !opts.ForceRotate {
		if authID, ok := s.sticky.Get(key); ok {
			for _, auth := range available {
				if auth.ID == authID {
					s.recordRequest(auth.ID, provider, model, now)
					return auth, nil
				}
			}
		}
	}

	selected := s.selectOptimal(available, provider, model, now)

	s.recordRequest(selected.ID, provider, model, now)
	s.sticky.Set(key, selected.ID)

	return selected, nil
}

func (s *QuotaAwareSelector) selectOptimal(auths []*Auth, provider, model string, now time.Time) *Auth {
	s.mu.Lock()
	defer s.mu.Unlock()

	quotaGroup := ResolveQuotaGroup(provider, model)
	if quotaGroup == "" {
		quotaGroup = model
	}

	type scored struct {
		auth     *Auth
		priority int64
	}

	candidates := make([]scored, 0, len(auths))

	for _, auth := range auths {
		stats := s.getStatsLocked(auth.ID, quotaGroup, now)
		priority := s.calculatePriority(stats, now)
		candidates = append(candidates, scored{auth: auth, priority: priority})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].auth.ID < candidates[j].auth.ID
	})

	return candidates[0].auth
}

func (s *QuotaAwareSelector) calculatePriority(stats *AuthQuotaStats, now time.Time) int64 {
	if !stats.QuotaExhausted {
		return stats.RequestCount
	}

	if stats.QuotaResetAt.IsZero() {
		return stats.RequestCount + int64(unknownResetPenalty.Minutes())*100
	}

	if now.After(stats.QuotaResetAt) {
		stats.QuotaExhausted = false
		stats.QuotaResetAt = time.Time{}
		return stats.RequestCount
	}

	remainingMinutes := stats.QuotaResetAt.Sub(now).Minutes()
	return stats.RequestCount + int64(remainingMinutes)*100
}

func (s *QuotaAwareSelector) getStatsLocked(authID, quotaGroup string, now time.Time) *AuthQuotaStats {
	groups, ok := s.authStats[authID]
	if !ok {
		groups = make(map[string]*AuthQuotaStats)
		s.authStats[authID] = groups
	}

	stats, ok := groups[quotaGroup]
	if !ok {
		stats = &AuthQuotaStats{WindowStart: now}
		groups[quotaGroup] = stats
		return stats
	}

	if now.Sub(stats.WindowStart) >= s.quotaWindow {
		stats.RequestCount = 0
		stats.QuotaExhausted = false
		stats.QuotaResetAt = time.Time{}
		stats.WindowStart = now
	}

	return stats
}

func (s *QuotaAwareSelector) recordRequest(authID, provider, model string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	quotaGroup := ResolveQuotaGroup(provider, model)
	if quotaGroup == "" {
		quotaGroup = model
	}

	stats := s.getStatsLocked(authID, quotaGroup, now)
	stats.RequestCount++
}

func (s *QuotaAwareSelector) RecordLimitHit(authID, provider, model string, retryAfter *time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	quotaGroup := ResolveQuotaGroup(provider, model)
	if quotaGroup == "" {
		quotaGroup = model
	}

	stats := s.getStatsLocked(authID, quotaGroup, now)
	stats.QuotaExhausted = true

	if retryAfter != nil && *retryAfter > 0 {
		stats.QuotaResetAt = now.Add(*retryAfter)
	} else {
		stats.QuotaResetAt = now.Add(unknownResetPenalty)
	}
}

func (s *QuotaAwareSelector) filterAvailable(auths []*Auth, model string, now time.Time) []*Auth {
	available := make([]*Auth, 0, len(auths))
	for _, auth := range auths {
		blocked, _, _ := isAuthBlockedForModel(auth, model, now)
		if !blocked {
			available = append(available, auth)
		}
	}
	return available
}

func (s *QuotaAwareSelector) buildCooldownError(auths []*Auth, model, provider string, now time.Time) error {
	var earliest time.Time
	cooldownCount := 0

	for _, auth := range auths {
		blocked, reason, next := isAuthBlockedForModel(auth, model, now)
		if blocked && reason == blockReasonCooldown {
			cooldownCount++
			if earliest.IsZero() || next.Before(earliest) {
				earliest = next
			}
		}
	}

	if cooldownCount == len(auths) && !earliest.IsZero() {
		return newModelCooldownError(model, provider, earliest.Sub(now))
	}

	return &Error{Code: "auth_unavailable", Message: "no auth available"}
}

func (s *QuotaAwareSelector) GetUsageStats() map[string]map[string]AuthQuotaStats {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now()
	result := make(map[string]map[string]AuthQuotaStats, len(s.authStats))

	for authID, groups := range s.authStats {
		groupCopy := make(map[string]AuthQuotaStats, len(groups))
		for group, stats := range groups {
			if now.Sub(stats.WindowStart) < s.quotaWindow {
				groupCopy[group] = *stats
			}
		}
		if len(groupCopy) > 0 {
			result[authID] = groupCopy
		}
	}

	return result
}
