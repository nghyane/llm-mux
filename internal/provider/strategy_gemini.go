package provider

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type GeminiStrategy struct {
	limiters sync.Map
}

func (s *GeminiStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
	var priority int64
	if state != nil {
		priority += state.ActiveRequests.Load() * 1000
	}

	if auth == nil {
		return priority
	}

	limiter := s.getOrCreateLimiter(auth.ID, config)
	capacity := float64(limiter.Burst())
	available := limiter.Tokens()

	if capacity > 0 {
		priority += int64((1.0 - available/capacity) * 600)
	}

	return priority
}

func (s *GeminiStrategy) getOrCreateLimiter(authID string, config *ProviderQuotaConfig) *rate.Limiter {
	if v, ok := s.limiters.Load(authID); ok {
		return v.(*rate.Limiter)
	}

	capacity := 60
	if config != nil && config.EstimatedLimit > 0 {
		capacity = int(config.EstimatedLimit)
	}

	limiter := rate.NewLimiter(rate.Every(time.Minute/time.Duration(capacity)), capacity)
	actual, _ := s.limiters.LoadOrStore(authID, limiter)
	return actual.(*rate.Limiter)
}

func (s *GeminiStrategy) OnQuotaHit(state *AuthQuotaState, cooldown *time.Duration) {
	if state == nil {
		return
	}
	now := time.Now()
	state.SetLastExhaustedAt(now)

	if cooldown != nil && *cooldown > 0 {
		state.SetCooldownUntil(now.Add(*cooldown))
	} else {
		state.SetCooldownUntil(now.Add(1 * time.Minute))
	}
}

func (s *GeminiStrategy) RecordUsage(state *AuthQuotaState, _ int64) {
}

func (s *GeminiStrategy) ConsumeToken(authID string) bool {
	if v, ok := s.limiters.Load(authID); ok {
		return v.(*rate.Limiter).Allow()
	}
	return true
}

var _ ProviderStrategy = (*GeminiStrategy)(nil)
