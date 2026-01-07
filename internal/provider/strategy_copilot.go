package provider

import (
	"sync"
	"sync/atomic"
	"time"
)

type CopilotStrategy struct {
	counters sync.Map
}

type RequestCounter struct {
	count     atomic.Int64
	windowEnd atomic.Int64
}

func (s *CopilotStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
	var priority int64
	if state != nil {
		priority += state.ActiveRequests.Load() * 1000
	}

	if auth == nil {
		return priority
	}

	counter := s.getOrCreateCounter(auth.ID, config)

	now := time.Now().UnixNano()
	windowEnd := counter.windowEnd.Load()
	if now > windowEnd {
		windowDuration := 24 * time.Hour
		if config != nil && config.WindowDuration > 0 {
			windowDuration = config.WindowDuration
		}
		newWindowEnd := time.Now().Add(windowDuration).UnixNano()
		if counter.windowEnd.CompareAndSwap(windowEnd, newWindowEnd) {
			counter.count.Store(0)
		}
	}

	count := counter.count.Load()
	estimatedLimit := int64(10_000)
	if config != nil && config.EstimatedLimit > 0 {
		estimatedLimit = config.EstimatedLimit
	}
	priority += int64(float64(count) / float64(estimatedLimit) * 600)

	return priority
}

func (s *CopilotStrategy) getOrCreateCounter(authID string, config *ProviderQuotaConfig) *RequestCounter {
	if v, ok := s.counters.Load(authID); ok {
		return v.(*RequestCounter)
	}
	windowDuration := 24 * time.Hour
	if config != nil && config.WindowDuration > 0 {
		windowDuration = config.WindowDuration
	}
	counter := &RequestCounter{}
	counter.windowEnd.Store(time.Now().Add(windowDuration).UnixNano())
	actual, _ := s.counters.LoadOrStore(authID, counter)
	return actual.(*RequestCounter)
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
	if v, ok := s.counters.Load(authID); ok {
		v.(*RequestCounter).count.Add(1)
	}
}

var _ ProviderStrategy = (*CopilotStrategy)(nil)
