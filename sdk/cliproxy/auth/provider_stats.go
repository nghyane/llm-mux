package auth

import (
	"sync"
	"sync/atomic"
	"time"
)

// ProviderStats tracks performance metrics for intelligent load balancing.
// Uses lock-free atomic operations for high-concurrency scenarios.
type ProviderStats struct {
	mu    sync.RWMutex
	stats map[string]*providerMetrics // key: "provider:model"
}

type providerMetrics struct {
	successCount   atomic.Int64
	failureCount   atomic.Int64
	totalLatencyNs atomic.Int64 // cumulative latency in nanoseconds
	lastUsed       atomic.Int64 // unix nano timestamp
	lastSuccess    atomic.Int64 // unix nano timestamp
}

// NewProviderStats creates a new stats tracker.
func NewProviderStats() *ProviderStats {
	return &ProviderStats{
		stats: make(map[string]*providerMetrics),
	}
}

// getOrCreate returns existing metrics or creates new ones.
func (ps *ProviderStats) getOrCreate(key string) *providerMetrics {
	ps.mu.RLock()
	m := ps.stats[key]
	ps.mu.RUnlock()
	if m != nil {
		return m
	}

	ps.mu.Lock()
	defer ps.mu.Unlock()
	// Double-check after acquiring write lock
	if m = ps.stats[key]; m != nil {
		return m
	}
	m = &providerMetrics{}
	ps.stats[key] = m
	return m
}

// RecordSuccess records a successful request with latency.
func (ps *ProviderStats) RecordSuccess(provider, model string, latency time.Duration) {
	key := provider + ":" + model
	m := ps.getOrCreate(key)
	m.successCount.Add(1)
	m.totalLatencyNs.Add(int64(latency))
	now := time.Now().UnixNano()
	m.lastUsed.Store(now)
	m.lastSuccess.Store(now)
}

// RecordFailure records a failed request.
func (ps *ProviderStats) RecordFailure(provider, model string) {
	key := provider + ":" + model
	m := ps.getOrCreate(key)
	m.failureCount.Add(1)
	m.lastUsed.Store(time.Now().UnixNano())
}

// GetScore returns a weighted score for provider selection.
// Higher score = better provider. Range: 0.0 to 1.0
func (ps *ProviderStats) GetScore(provider, model string) float64 {
	key := provider + ":" + model
	ps.mu.RLock()
	m := ps.stats[key]
	ps.mu.RUnlock()

	if m == nil {
		return 0.5 // Default score for unknown providers
	}

	success := m.successCount.Load()
	failure := m.failureCount.Load()
	total := success + failure

	if total == 0 {
		return 0.5 // No data yet
	}

	// Success rate (0.0 to 1.0)
	successRate := float64(success) / float64(total)

	// Recency bonus: prefer recently successful providers
	// Decay over 5 minutes
	recencyBonus := 0.0
	lastSuccess := m.lastSuccess.Load()
	if lastSuccess > 0 {
		elapsed := time.Since(time.Unix(0, lastSuccess))
		if elapsed < 5*time.Minute {
			recencyBonus = 0.1 * (1.0 - float64(elapsed)/(5*float64(time.Minute)))
		}
	}

	// Combine: 90% success rate + 10% recency
	return successRate*0.9 + recencyBonus
}

// GetAvgLatency returns average latency for a provider:model.
func (ps *ProviderStats) GetAvgLatency(provider, model string) time.Duration {
	key := provider + ":" + model
	ps.mu.RLock()
	m := ps.stats[key]
	ps.mu.RUnlock()

	if m == nil {
		return 0
	}

	success := m.successCount.Load()
	if success == 0 {
		return 0
	}

	return time.Duration(m.totalLatencyNs.Load() / success)
}

// SortByScore sorts providers by score (highest first), preserving order for equal scores.
func (ps *ProviderStats) SortByScore(providers []string, model string) []string {
	if len(providers) <= 1 {
		return providers
	}

	type scored struct {
		provider string
		score    float64
	}
	items := make([]scored, len(providers))
	allDefault := true
	for i, p := range providers {
		score := ps.GetScore(p, model)
		items[i] = scored{provider: p, score: score}
		if score != 0.5 {
			allDefault = false
		}
	}

	// Preserve priority order when all scores are default
	if allDefault {
		return providers
	}

	// Stable insertion sort - only swap when strictly greater
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && items[j].score > items[j-1].score; j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}

	result := make([]string, len(providers))
	for i, item := range items {
		result[i] = item.provider
	}
	return result
}

// Cleanup removes stale entries older than maxAge.
func (ps *ProviderStats) Cleanup(maxAge time.Duration) int {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	cutoff := time.Now().Add(-maxAge).UnixNano()
	removed := 0

	for key, m := range ps.stats {
		if m.lastUsed.Load() < cutoff {
			delete(ps.stats, key)
			removed++
		}
	}

	return removed
}

// Stats returns current stats for debugging/monitoring.
func (ps *ProviderStats) Stats() map[string]map[string]int64 {
	ps.mu.RLock()
	defer ps.mu.RUnlock()

	result := make(map[string]map[string]int64, len(ps.stats))
	for key, m := range ps.stats {
		result[key] = map[string]int64{
			"success":    m.successCount.Load(),
			"failure":    m.failureCount.Load(),
			"avg_lat_ms": m.totalLatencyNs.Load() / max(m.successCount.Load(), 1) / 1e6,
		}
	}
	return result
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
