package provider

import (
	"context"
	"time"
)

// ProviderStrategy defines provider-specific selection logic.
// All methods MUST be lock-free and O(1).
type ProviderStrategy interface {
	// Score returns selection priority (lower = better).
	// Must be pure and lock-free - only read from atomic state.
	Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64

	// OnQuotaHit handles 429 errors with provider-specific logic.
	OnQuotaHit(state *AuthQuotaState, cooldown *time.Duration)

	// RecordUsage tracks tokens/requests for selection decisions.
	RecordUsage(state *AuthQuotaState, tokens int64)
}

// BackgroundRefresher is optionally implemented by strategies
// that need to fetch real quota data from provider APIs.
type BackgroundRefresher interface {
	ProviderStrategy
	// StartRefresh begins background quota polling for this auth.
	// Returns a channel that receives updated quota snapshots.
	StartRefresh(ctx context.Context, auth *Auth) <-chan *RealQuotaSnapshot
}

// RealQuotaSnapshot holds data from real provider APIs.
type RealQuotaSnapshot struct {
	RemainingFraction float64   // 0.0-1.0 (from Antigravity API)
	RemainingTokens   int64     // Absolute remaining
	WindowResetAt     time.Time // When quota window resets
	FetchedAt         time.Time // When this was fetched
}

// DefaultStrategy is used for providers without specific strategy.
type DefaultStrategy struct{}

// Score implements ProviderStrategy.
func (s *DefaultStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
	if state == nil {
		return 0
	}
	var priority int64
	priority += state.ActiveRequests.Load() * 1000

	limit := state.LearnedLimit.Load()
	if limit <= 0 && config != nil {
		limit = config.EstimatedLimit
	}
	if limit > 0 {
		usagePercent := float64(state.TotalTokensUsed.Load()) / float64(limit)
		priority += int64(usagePercent * 500)
	}
	return priority
}

func (s *DefaultStrategy) OnQuotaHit(state *AuthQuotaState, cooldown *time.Duration) {
	if state == nil {
		return
	}
	now := time.Now()
	state.SetLastExhaustedAt(now)

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

	if cooldown != nil && *cooldown > 0 {
		state.SetCooldownUntil(now.Add(*cooldown))
		state.SetLearnedCooldown(*cooldown)
	} else if learned := state.GetLearnedCooldown(); learned > 0 {
		state.SetCooldownUntil(now.Add(learned))
	} else {
		state.SetCooldownUntil(now.Add(5 * time.Hour))
	}

	state.TotalTokensUsed.Store(0)
}

// RecordUsage implements ProviderStrategy.
func (s *DefaultStrategy) RecordUsage(state *AuthQuotaState, tokens int64) {
	if state != nil && tokens > 0 {
		state.TotalTokensUsed.Add(tokens)
	}
}

var _ ProviderStrategy = (*DefaultStrategy)(nil)
