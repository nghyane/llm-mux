package provider

import (
	"hash/fnv"
	"sync"
	"sync/atomic"
	"time"
)

const numShards = 32

type statShard struct {
	mu    sync.RWMutex
	stats map[string]*providerMetrics
}

type ProviderStats struct {
	shards [numShards]*statShard
}

type providerMetrics struct {
	successCount   atomic.Int64
	failureCount   atomic.Int64
	totalLatencyNs atomic.Int64 // cumulative latency in nanoseconds
	lastUsed       atomic.Int64 // unix nano timestamp
	lastSuccess    atomic.Int64 // unix nano timestamp
}

// fnv32 computes FNV-1a 32-bit hash of a string.
func fnv32(s string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(s))
	return h.Sum32()
}

// shardIdx returns the shard index for a key.
func shardIdx(key string) int {
	return int(fnv32(key)) % numShards
}

// NewProviderStats creates a new stats tracker.
func NewProviderStats() *ProviderStats {
	ps := &ProviderStats{}
	for i := 0; i < numShards; i++ {
		ps.shards[i] = &statShard{
			stats: make(map[string]*providerMetrics),
		}
	}
	return ps
}

// getOrCreate returns existing metrics or creates new ones.
func (ps *ProviderStats) getOrCreate(key string) *providerMetrics {
	idx := shardIdx(key)
	shard := ps.shards[idx]

	shard.mu.RLock()
	m := shard.stats[key]
	shard.mu.RUnlock()
	if m != nil {
		return m
	}

	shard.mu.Lock()
	defer shard.mu.Unlock()
	// Double-check after acquiring write lock
	if m = shard.stats[key]; m != nil {
		return m
	}
	m = &providerMetrics{}
	shard.stats[key] = m
	return m
}

// getShard returns the shard for a key and acquires the read lock.
func (ps *ProviderStats) getShard(key string) (*statShard, *providerMetrics) {
	idx := shardIdx(key)
	shard := ps.shards[idx]

	shard.mu.RLock()
	m := shard.stats[key]
	shard.mu.RUnlock()
	return shard, m
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
	_, m := ps.getShard(key)

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
	_, m := ps.getShard(key)

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
	cutoff := time.Now().Add(-maxAge).UnixNano()
	removed := 0

	for i := 0; i < numShards; i++ {
		shard := ps.shards[i]
		shard.mu.Lock()
		for key, m := range shard.stats {
			if m.lastUsed.Load() < cutoff {
				delete(shard.stats, key)
				removed++
			}
		}
		shard.mu.Unlock()
	}

	return removed
}

// Stats returns current stats for debugging/monitoring.
func (ps *ProviderStats) Stats() map[string]map[string]int64 {
	result := make(map[string]map[string]int64)

	for i := 0; i < numShards; i++ {
		shard := ps.shards[i]
		shard.mu.RLock()
		for key, m := range shard.stats {
			result[key] = map[string]int64{
				"success":    m.successCount.Load(),
				"failure":    m.failureCount.Load(),
				"avg_lat_ms": m.totalLatencyNs.Load() / max(m.successCount.Load(), 1) / 1e6,
			}
		}
		shard.mu.RUnlock()
	}

	return result
}

func max(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
