package provider

import (
	"context"
	"strings"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
	"golang.org/x/sync/semaphore"
)

const (
	// maxConcurrentRefreshes limits the number of concurrent refresh goroutines
	// to prevent goroutine explosion under high load with many auths.
	maxConcurrentRefreshes = 10

	// refreshCheckInterval defines how often we check if tokens need refresh.
	// Changed from 5 seconds to 2 minutes to reduce unnecessary checks.
	// Tokens are still refreshed proactively before expiry (see defaultRefreshLead).
	refreshCheckInterval  = 2 * time.Minute
	refreshPendingBackoff = time.Minute
	refreshFailureBackoff = 5 * time.Minute

	// defaultRefreshLead is the minimum time before token expiry to trigger refresh.
	// Used as a fallback when percentage-based calculation results in less time.
	// This ensures tokens are refreshed well before they expire.
	defaultRefreshLead = 10 * time.Minute

	// refreshLeadPercentage is the percentage of token lifetime at which to refresh.
	// 0.25 means refresh when 25% of lifetime remains (industry best practice: 20-30%).
	// For a 1-hour token: refreshes at 45 minutes (15 min before expiry).
	// For an 8-hour token: refreshes at 6 hours (2 hours before expiry).
	refreshLeadPercentage = 0.25
)

// newRefreshSemaphore creates a weighted semaphore for bounding concurrent refresh operations.
func newRefreshSemaphore() *semaphore.Weighted {
	return semaphore.NewWeighted(maxConcurrentRefreshes)
}

// StartAutoRefresh launches a background loop that evaluates auth freshness
// and triggers refresh operations when required. Uses a smart timer-based approach:
// calculates when the next token will expire and sleeps until then (with a max of 2 minutes).
// Only one loop is kept alive; starting a new one cancels the previous run.
func (m *Manager) StartAutoRefresh(parent context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = refreshCheckInterval
	}
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
	ctx, cancel := context.WithCancel(parent)
	m.refreshCancel = cancel
	go func() {
		// Cleanup provider stats every hour to prevent memory leak
		cleanupTicker := time.NewTicker(1 * time.Hour)
		defer cleanupTicker.Stop()

		for {
			// Check all tokens and get next refresh time
			nextRefresh := m.checkRefreshes(ctx)

			// Calculate sleep duration: sleep until next expiry, but max 2 minutes
			sleepDuration := interval
			if !nextRefresh.IsZero() {
				untilNext := time.Until(nextRefresh)
				if untilNext > 0 && untilNext < sleepDuration {
					sleepDuration = untilNext
				}
			}

			// Add small buffer to avoid waking up too early
			sleepDuration += 5 * time.Second

			// Use a timer instead of ticker for dynamic sleep duration
			timer := time.NewTimer(sleepDuration)

			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
				// Next refresh check will happen at top of loop
			case <-cleanupTicker.C:
				timer.Stop()
				// Remove provider stats older than 24 hours
				removed := m.CleanupProviderStats(24 * time.Hour)
				if removed > 0 {
					log.Debugf("Cleaned up %d stale provider stats entries", removed)
				}
			}
		}
	}()
}

// StopAutoRefresh cancels the background refresh loop, if running.
func (m *Manager) StopAutoRefresh() {
	if m.refreshCancel != nil {
		m.refreshCancel()
		m.refreshCancel = nil
	}
}

// checkRefreshes evaluates all registered auths and triggers refreshes as needed.
// Uses a semaphore to bound the number of concurrent refresh goroutines.
// Returns the time when the next token will need refresh (zero if no upcoming refreshes).
func (m *Manager) checkRefreshes(ctx context.Context) time.Time {
	now := time.Now()
	var snapshot []*Auth
	if m.registry != nil {
		snapshot = m.registry.List()
	} else {
		snapshot = m.snapshotAuths()
	}

	var nextRefresh time.Time

	for _, a := range snapshot {
		typ, _ := a.AccountInfo()
		if typ != "api_key" {
			// Calculate when this token will need refresh
			if refreshTime := m.getNextRefreshTime(a, now); !refreshTime.IsZero() {
				if nextRefresh.IsZero() || refreshTime.Before(nextRefresh) {
					nextRefresh = refreshTime
				}
			}

			if !m.shouldRefresh(a, now) {
				continue
			}
			log.Debugf("checking refresh for %s, %s, %s", a.Provider, a.ID, typ)

			if exec := m.executorFor(a.Provider); exec == nil {
				continue
			}
			if !m.markRefreshPending(a.ID, now) {
				continue
			}
			// Use TryAcquire to avoid blocking the refresh loop.
			// If semaphore is full, skip this refresh - it will be picked up next interval.
			if m.refreshSem != nil && m.refreshSem.TryAcquire(1) {
				go func(authID string) {
					defer m.refreshSem.Release(1)
					m.refreshAuth(ctx, authID)
				}(a.ID)
			} else if m.refreshSem == nil {
				// Fallback for backwards compatibility if semaphore not initialized
				go m.refreshAuth(ctx, a.ID)
			}
		}
	}

	return nextRefresh
}

// snapshotAuths creates a copy of all currently registered auths.
func (m *Manager) snapshotAuths() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Auth, 0, len(m.auths))
	for _, a := range m.auths {
		out = append(out, a.Clone())
	}
	return out
}

// getNextRefreshTime calculates when a token should be refreshed.
// Returns zero time if the token doesn't need refresh or has no expiry.
func (m *Manager) getNextRefreshTime(a *Auth, now time.Time) time.Time {
	if a == nil || a.Disabled {
		return time.Time{}
	}
	if !a.NextRefreshAfter.IsZero() && now.Before(a.NextRefreshAfter) {
		return a.NextRefreshAfter
	}

	lastRefresh := a.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(a); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := a.ExpirationTime()
	if !hasExpiry || expiry.IsZero() {
		return time.Time{}
	}

	// Get the refresh lead time for this provider
	provider := strings.ToLower(a.Provider)
	lead := ProviderRefreshLead(provider, a.Runtime)

	if lead == nil {
		// Use smart calculation: percentage of token lifetime, with minimum lead time
		if !lastRefresh.IsZero() && lastRefresh.Before(expiry) {
			tokenLifetime := expiry.Sub(lastRefresh)
			calculatedLead := time.Duration(float64(tokenLifetime) * refreshLeadPercentage)

			// Use the larger of calculated lead or minimum lead
			if calculatedLead > defaultRefreshLead {
				lead = &calculatedLead
			} else {
				defaultLead := defaultRefreshLead
				lead = &defaultLead
			}
		} else {
			// Fallback to default if we don't have last refresh time
			defaultLead := defaultRefreshLead
			lead = &defaultLead
		}
	}

	if *lead <= 0 {
		return time.Time{}
	}

	// Calculate refresh time: expiry minus lead time
	refreshTime := expiry.Add(-*lead)
	if refreshTime.Before(now) {
		// Already past refresh time, should refresh immediately
		return now
	}

	return refreshTime
}

// shouldRefresh determines if an auth needs refresh based on expiration and refresh rules.
func (m *Manager) shouldRefresh(a *Auth, now time.Time) bool {
	if a == nil || a.Disabled {
		return false
	}
	if !a.NextRefreshAfter.IsZero() && now.Before(a.NextRefreshAfter) {
		return false
	}
	if evaluator, ok := a.Runtime.(RefreshEvaluator); ok && evaluator != nil {
		return evaluator.ShouldRefresh(now, a)
	}

	lastRefresh := a.LastRefreshedAt
	if lastRefresh.IsZero() {
		if ts, ok := authLastRefreshTimestamp(a); ok {
			lastRefresh = ts
		}
	}

	expiry, hasExpiry := a.ExpirationTime()

	if interval := authPreferredInterval(a); interval > 0 {
		if hasExpiry && !expiry.IsZero() {
			if !expiry.After(now) {
				return true
			}
			if expiry.Sub(now) <= interval {
				return true
			}
		}
		if lastRefresh.IsZero() {
			return true
		}
		return now.Sub(lastRefresh) >= interval
	}

	provider := strings.ToLower(a.Provider)
	lead := ProviderRefreshLead(provider, a.Runtime)

	if lead == nil {
		// Use smart calculation: percentage of token lifetime, with minimum lead time
		if hasExpiry && !expiry.IsZero() && !lastRefresh.IsZero() && lastRefresh.Before(expiry) {
			tokenLifetime := expiry.Sub(lastRefresh)
			calculatedLead := time.Duration(float64(tokenLifetime) * refreshLeadPercentage)

			// Use the larger of calculated lead or minimum lead
			if calculatedLead > defaultRefreshLead {
				lead = &calculatedLead
			} else {
				defaultLead := defaultRefreshLead
				lead = &defaultLead
			}
		} else {
			// Fallback to default if we don't have sufficient data
			defaultLead := defaultRefreshLead
			lead = &defaultLead
		}
	}

	if *lead <= 0 {
		if hasExpiry && !expiry.IsZero() {
			return now.After(expiry)
		}
		return false
	}
	if hasExpiry && !expiry.IsZero() {
		return time.Until(expiry) <= *lead
	}
	if !lastRefresh.IsZero() {
		return now.Sub(lastRefresh) >= *lead
	}
	return true
}

// authPreferredInterval extracts the refresh interval from auth metadata and attributes.
func authPreferredInterval(a *Auth) time.Duration {
	if a == nil {
		return 0
	}
	if d := durationFromMetadata(a.Metadata, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	if d := durationFromAttributes(a.Attributes, "refresh_interval_seconds", "refreshIntervalSeconds", "refresh_interval", "refreshInterval"); d > 0 {
		return d
	}
	return 0
}

// durationFromMetadata extracts a duration from metadata.
func durationFromMetadata(meta map[string]any, keys ...string) time.Duration {
	if len(meta) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if dur := parseDurationValue(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

// durationFromAttributes extracts a duration from string attributes.
func durationFromAttributes(attrs map[string]string, keys ...string) time.Duration {
	if len(attrs) == 0 {
		return 0
	}
	for _, key := range keys {
		if val, ok := attrs[key]; ok {
			if dur := parseDurationString(val); dur > 0 {
				return dur
			}
		}
	}
	return 0
}

// authLastRefreshTimestamp looks up the last refresh time from auth metadata.
func authLastRefreshTimestamp(a *Auth) (time.Time, bool) {
	if a == nil {
		return time.Time{}, false
	}
	if a.Metadata != nil {
		if ts, ok := lookupMetadataTime(a.Metadata, "last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"); ok {
			return ts, true
		}
	}
	if a.Attributes != nil {
		for _, key := range []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"} {
			if val := strings.TrimSpace(a.Attributes[key]); val != "" {
				if ts, ok := parseTimeValue(val); ok {
					return ts, true
				}
			}
		}
	}
	return time.Time{}, false
}

// lookupMetadataTime looks up a time value from metadata.
func lookupMetadataTime(meta map[string]any, keys ...string) (time.Time, bool) {
	for _, key := range keys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseTimeValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}
