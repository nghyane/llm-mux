package provider

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type CopilotStrategy struct {
	limiters sync.Map
}

func (s *CopilotStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
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

func (s *CopilotStrategy) getOrCreateLimiter(authID string, config *ProviderQuotaConfig) *rate.Limiter {
	if v, ok := s.limiters.Load(authID); ok {
		return v.(*rate.Limiter)
	}

	estimatedLimit := int64(10_000)
	windowDuration := 24 * time.Hour
	if config != nil {
		if config.EstimatedLimit > 0 {
			estimatedLimit = config.EstimatedLimit
		}
		if config.WindowDuration > 0 {
			windowDuration = config.WindowDuration
		}
	}

	tokenInterval := windowDuration / time.Duration(estimatedLimit)
	burstSize := 100
	limiter := rate.NewLimiter(rate.Every(tokenInterval), burstSize)
	actual, _ := s.limiters.LoadOrStore(authID, limiter)
	return actual.(*rate.Limiter)
}

func (s *CopilotStrategy) OnQuotaHit(state *AuthQuotaState, cooldown *time.Duration) {
	if state == nil {
		return
	}
	now := time.Now()
	state.SetLastExhaustedAt(now)

	if cooldown != nil && *cooldown > 0 {
		state.SetCooldownUntil(now.Add(*cooldown))
	} else {
		state.SetCooldownUntil(now.Add(1 * time.Hour))
	}
}

func (s *CopilotStrategy) RecordUsage(state *AuthQuotaState, _ int64) {
}

func (s *CopilotStrategy) IncrementRequestCount(authID string) {
	if v, ok := s.limiters.Load(authID); ok {
		v.(*rate.Limiter).Allow()
	}
}

var _ ProviderStrategy = (*CopilotStrategy)(nil)
