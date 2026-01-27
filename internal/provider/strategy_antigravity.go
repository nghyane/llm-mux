package provider

import (
	"context"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"
)

// tokenExpiryCache stores parsed expiresAt time for each auth ID to avoid
// repeated RFC3339 parsing on every Score() call.
var tokenExpiryCache struct {
	sync.RWMutex
	data map[string]time.Time
}

type AntigravityStrategy struct{}

func (s *AntigravityStrategy) Score(auth *Auth, state *AuthQuotaState, config *ProviderQuotaConfig) int64 {
	if state == nil {
		return 0
	}

	// Factor in auth priority (lower number = higher priority)
	var priority int64
	if authPriority, ok := auth.Attributes["priority"]; ok {
		if p, err := strconv.Atoi(authPriority); err == nil {
			priority += int64(p) * 10 // Scale down to avoid dominating other factors
		}
	} else if auth.Priority > 0 {
		priority += int64(auth.Priority) * 10
	}

	priority += state.ActiveRequests.Load() * 1000

	if real := state.GetRealQuota(); real != nil && time.Since(real.FetchedAt) < 5*time.Minute {
		priority += int64((1.0 - real.RemainingFraction) * 800)
	} else {
		limit := state.LearnedLimit.Load()
		if limit <= 0 && config != nil {
			limit = config.EstimatedLimit
		}
		if limit > 0 {
			used := state.TotalTokensUsed.Load()
			priority += int64(float64(used) / float64(limit) * 500)
		}
	}

	tokenValid, needsRefresh := checkTokenFromAuth(auth, 30*time.Second)
	if !tokenValid {
		priority += 10000
	} else if needsRefresh {
		priority += 500
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

	var cooldownUntil time.Time
	switch {
	case cooldown != nil && *cooldown > 0:
		cooldownUntil = now.Add(*cooldown)
	default:
		if real := state.GetRealQuota(); real != nil && !real.WindowResetAt.IsZero() && real.WindowResetAt.After(now) {
			cooldownUntil = real.WindowResetAt
		} else {
			cooldownUntil = now.Add(5 * time.Hour)
		}
	}

	newCooldownNs := cooldownUntil.UnixNano()
	for {
		currentNs := state.CooldownUntil.Load()
		if currentNs >= newCooldownNs {
			break
		}
		if state.CooldownUntil.CompareAndSwap(currentNs, newCooldownNs) {
			break
		}
	}

	state.TotalTokensUsed.Store(0)
}

func (s *AntigravityStrategy) RecordUsage(state *AuthQuotaState, tokens int64) {
	if state != nil && tokens > 0 {
		state.TotalTokensUsed.Add(tokens)
	}
}

const (
	pollIntervalNormal     = 2 * time.Minute
	pollIntervalIncreased  = 30 * time.Second
	pollIntervalAggressive = 10 * time.Second
	minFetchInterval       = 3 * time.Second // Prevent API spam

	quotaThresholdLow      = 0.20
	quotaThresholdCritical = 0.05
)

func adaptivePollInterval(remainingFraction float64) time.Duration {
	switch {
	case remainingFraction <= quotaThresholdCritical:
		return pollIntervalAggressive
	case remainingFraction <= quotaThresholdLow:
		return pollIntervalIncreased
	default:
		return pollIntervalNormal
	}
}

func (s *AntigravityStrategy) StartRefresh(ctx context.Context, auth *Auth, state *AuthQuotaState) <-chan *RealQuotaSnapshot {
	ch := make(chan *RealQuotaSnapshot, 1)

	if auth == nil {
		close(ch)
		return ch
	}

	var triggerCh <-chan struct{}
	if state != nil {
		triggerCh = state.GetRefreshTrigger()
	}

	go func() {
		defer close(ch)

		// Startup Smear: Random delay 0-2s to prevent thundering herd on restart
		initialJitter := time.Duration(rand.Float64() * float64(2*time.Second))
		select {
		case <-time.After(initialJitter):
		case <-ctx.Done():
			return
		}

		currentInterval := pollIntervalNormal
		lastFetch := time.Time{}

		fetchQuota := func() *RealQuotaSnapshot {
			token := extractAccessToken(auth)
			if token == "" {
				return nil
			}
			snapshot := fetchAntigravityQuota(ctx, token)
			lastFetch = time.Now()
			if snapshot != nil {
				select {
				case ch <- snapshot:
				default:
				}
				currentInterval = adaptivePollInterval(snapshot.RemainingFraction)
			}
			return snapshot
		}

		fetchQuota()

		timer := time.NewTimer(currentInterval)
		defer timer.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-triggerCh:
				elapsed := time.Since(lastFetch)
				if elapsed < minFetchInterval {
					// Rate limited: Schedule fetch for when cooldown expires
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					wait := minFetchInterval - elapsed
					timer.Reset(wait)
					continue
				}
				// Safe to fetch immediately
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				fetchQuota()
				timer.Reset(currentInterval)

			case <-timer.C:
				fetchQuota()
				timer.Reset(currentInterval)
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

// getCachedExpiresAt retrieves the parsed expiresAt from cache or parses and caches it.
func getCachedExpiresAt(auth *Auth) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}

	// If no ID, parse directly without caching (for tests/edge cases)
	if auth.ID == "" {
		return parseExpiresAt(auth), !parseExpiresAt(auth).IsZero()
	}

	tokenExpiryCache.RLock()
	expiresAt, ok := tokenExpiryCache.data[auth.ID]
	tokenExpiryCache.RUnlock()

	if ok {
		return expiresAt, true
	}

	// Cache miss - parse and store
	expiresAt = parseExpiresAt(auth)
	if !expiresAt.IsZero() {
		tokenExpiryCache.Lock()
		if tokenExpiryCache.data == nil {
			tokenExpiryCache.data = make(map[string]time.Time)
		}
		tokenExpiryCache.data[auth.ID] = expiresAt
		tokenExpiryCache.Unlock()
		return expiresAt, true
	}

	return time.Time{}, false
}

// parseExpiresAt extracts and parses the expiresAt time from auth metadata.
func parseExpiresAt(auth *Auth) time.Time {
	if auth == nil || auth.Metadata == nil {
		return time.Time{}
	}

	// Try RFC3339 "expired" field first
	if expiredStr, ok := auth.Metadata["expired"].(string); ok && expiredStr != "" {
		if t, err := time.Parse(time.RFC3339, expiredStr); err == nil {
			return t
		}
	}

	// Fall back to timestamp + expires_in
	var ts int64
	switch v := auth.Metadata["timestamp"].(type) {
	case int64:
		ts = v
	case float64:
		ts = int64(v)
	}
	if ts > 0 {
		var expiresIn int64
		switch v := auth.Metadata["expires_in"].(type) {
		case int64:
			expiresIn = v
		case float64:
			expiresIn = int64(v)
		}
		if expiresIn > 0 {
			return time.UnixMilli(ts).Add(time.Duration(expiresIn) * time.Second)
		}
	}

	return time.Time{}
}

// InvalidateTokenCache removes the cached expiresAt for the given auth ID.
// Call this when auth metadata is refreshed to ensure fresh parsing.
func InvalidateTokenCache(authID string) {
	if authID == "" {
		return
	}
	tokenExpiryCache.Lock()
	delete(tokenExpiryCache.data, authID)
	tokenExpiryCache.Unlock()
}

func checkTokenFromAuth(auth *Auth, buffer time.Duration) (valid bool, needsRefresh bool) {
	if auth == nil || auth.Metadata == nil {
		return false, false
	}

	token := extractAccessToken(auth)
	if token == "" {
		return false, false
	}

	// Use cached expiresAt parsing for efficiency
	expiresAt, hasExpiry := getCachedExpiresAt(auth)
	if !hasExpiry {
		// No expiry information available
		return true, false
	}

	now := time.Now()
	valid = now.Add(buffer).Before(expiresAt)
	needsRefresh = expiresAt.Sub(now) < 5*time.Minute
	return valid, needsRefresh
}

var (
	_ ProviderStrategy    = (*AntigravityStrategy)(nil)
	_ BackgroundRefresher = (*AntigravityStrategy)(nil)
)
