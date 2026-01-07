package provider

import (
	"time"
)

type ClaudeStrategy struct{}

func (s *ClaudeStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
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

func (s *ClaudeStrategy) OnQuotaHit(state *AuthQuotaState, cooldown *time.Duration) {
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

func (s *ClaudeStrategy) RecordUsage(state *AuthQuotaState, tokens int64) {
	if state != nil && tokens > 0 {
		state.TotalTokensUsed.Add(tokens)
	}
}

var _ ProviderStrategy = (*ClaudeStrategy)(nil)
