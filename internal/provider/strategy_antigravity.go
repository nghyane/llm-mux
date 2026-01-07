package provider

import (
	"context"
	"math/rand"
	"time"
)

type AntigravityStrategy struct{}

func (s *AntigravityStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
	if state == nil {
		return 0
	}

	var priority int64
	priority += state.ActiveRequests.Load() * 1000

	if real := state.GetRealQuota(); real != nil && time.Since(real.FetchedAt) < 5*time.Minute {
		priority += int64((1.0 - real.RemainingFraction) * 800)
		return priority
	}

	limit := state.LearnedLimit.Load()
	if limit <= 0 && config != nil {
		limit = config.EstimatedLimit
	}
	if limit > 0 {
		used := state.TotalTokensUsed.Load()
		priority += int64(float64(used) / float64(limit) * 500)
	}

	return priority
}

func (s *AntigravityStrategy) OnQuotaHit(state *AuthQuotaState, cooldown *time.Duration) {
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
	} else {
		state.SetCooldownUntil(now.Add(5 * time.Hour))
	}

	state.TotalTokensUsed.Store(0)
}

func (s *AntigravityStrategy) RecordUsage(state *AuthQuotaState, tokens int64) {
	if state != nil && tokens > 0 {
		state.TotalTokensUsed.Add(tokens)
	}
}

func (s *AntigravityStrategy) StartRefresh(ctx context.Context, auth *Auth) <-chan *RealQuotaSnapshot {
	ch := make(chan *RealQuotaSnapshot, 1)

	accessToken := extractAccessToken(auth)
	if accessToken == "" {
		close(ch)
		return ch
	}

	go func() {
		defer close(ch)

		jitter := time.Duration(rand.Float64() * float64(30*time.Second))
		time.Sleep(jitter)

		ticker := time.NewTicker(2 * time.Minute)
		defer ticker.Stop()

		if snapshot := fetchAntigravityQuota(ctx, accessToken); snapshot != nil {
			select {
			case ch <- snapshot:
			default:
			}
		}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if snapshot := fetchAntigravityQuota(ctx, accessToken); snapshot != nil {
					select {
					case ch <- snapshot:
					default:
					}
				}
			}
		}
	}()
	return ch
}

func extractAccessToken(auth *Auth) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	if v, ok := auth.Metadata["access_token"].(string); ok {
		return v
	}
	return ""
}

var (
	_ ProviderStrategy    = (*AntigravityStrategy)(nil)
	_ BackgroundRefresher = (*AntigravityStrategy)(nil)
)
