package provider

import (
	"sync"
	"sync/atomic"
	"time"
)

type GeminiStrategy struct {
	buckets sync.Map
}

type TokenBucket struct {
	tokens   atomic.Int64
	lastFill atomic.Int64
	capacity int64
	fillRate float64
}

func (s *GeminiStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
	var priority int64
	if state != nil {
		priority += state.ActiveRequests.Load() * 1000
	}

	if auth == nil {
		return priority
	}

	bucket := s.getOrCreateBucket(auth.ID, config)
	available := bucket.availableTokens()

	if bucket.capacity > 0 {
		priority += int64((1.0 - float64(available)/float64(bucket.capacity)) * 600)
	}

	return priority
}

func (b *TokenBucket) availableTokens() int64 {
	now := time.Now().UnixNano()
	last := b.lastFill.Load()
	elapsed := float64(now - last)

	current := b.tokens.Load()
	refilled := current + int64(elapsed*b.fillRate)
	if refilled > b.capacity {
		refilled = b.capacity
	}

	if b.lastFill.CompareAndSwap(last, now) {
		b.tokens.Store(refilled)
	}

	return refilled
}

func (s *GeminiStrategy) getOrCreateBucket(authID string, config *ProviderQuotaConfig) *TokenBucket {
	if v, ok := s.buckets.Load(authID); ok {
		return v.(*TokenBucket)
	}

	capacity := int64(60)
	if config != nil && config.EstimatedLimit > 0 {
		capacity = config.EstimatedLimit
	}
	fillRate := float64(capacity) / float64(time.Minute)

	bucket := &TokenBucket{
		capacity: capacity,
		fillRate: fillRate,
	}
	bucket.tokens.Store(capacity)
	bucket.lastFill.Store(time.Now().UnixNano())

	actual, _ := s.buckets.LoadOrStore(authID, bucket)
	return actual.(*TokenBucket)
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
	if v, ok := s.buckets.Load(authID); ok {
		bucket := v.(*TokenBucket)
		for {
			available := bucket.availableTokens()
			if available <= 0 {
				return false
			}
			if bucket.tokens.CompareAndSwap(available, available-1) {
				return true
			}
		}
	}
	return true
}

var _ ProviderStrategy = (*GeminiStrategy)(nil)
