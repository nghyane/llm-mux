package provider

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nghyane/llm-mux/internal/json"
	"github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/transport"
	"golang.org/x/sync/singleflight"
)

const (
	antigravityQuotaEndpoint = "https://cloudcode-pa.googleapis.com/v1internal:getQuotaInfo"
	quotaFetchTimeout        = 10 * time.Second
	quotaCacheTTL            = 30 * time.Second // Cache quota for 30s
)

// quotaCacheEntry holds cached quota data with TTL
type quotaCacheEntry struct {
	snapshot  *RealQuotaSnapshot
	fetchedAt time.Time
}

var (
	quotaCache      = make(map[string]*quotaCacheEntry)
	quotaCacheMu    sync.RWMutex
	quotaFetchCount int64
	quotaCacheHit   int64
	quotaFetchGroup singleflight.Group
)

func fetchAntigravityQuota(ctx context.Context, accessToken string) *RealQuotaSnapshot {
	if accessToken == "" {
		return nil
	}

	// Check cache first (fast path)
	if cached := getCachedQuota(accessToken); cached != nil {
		atomic.AddInt64(&quotaCacheHit, 1)
		return cached
	}

	// Use singleflight to deduplicate concurrent fetches
	type result struct {
		snapshot *RealQuotaSnapshot
	}
	ch, _, _ := quotaFetchGroup.Do(accessToken, func() (interface{}, error) {
		// Check cache again inside singleflight to avoid race and ensure freshness
		if cached := getCachedQuota(accessToken); cached != nil {
			atomic.AddInt64(&quotaCacheHit, 1)
			return result{snapshot: cached}, nil
		}
		return result{snapshot: doFetchAntigravityQuota(ctx, accessToken)}, nil
	})
	r := ch.(result)
	if r.snapshot != nil {
		setCachedQuota(accessToken, r.snapshot)
		atomic.AddInt64(&quotaFetchCount, 1)
	}
	return r.snapshot
}

func doFetchAntigravityQuota(ctx context.Context, accessToken string) *RealQuotaSnapshot {
	ctx, cancel := context.WithTimeout(ctx, quotaFetchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, antigravityQuotaEndpoint, nil)
	if err != nil {
		logging.Debugf("antigravity: failed to create quota request: %v", err)
		return nil
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := transport.SharedClient.Do(req)
	if err != nil {
		logging.Debugf("antigravity: quota request failed: %v", err)
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		logging.Debugf("antigravity: quota request failed with status %d", resp.StatusCode)
		return nil
	}

	var quotaResp struct {
		RemainingFraction float64 `json:"remainingFraction"`
		ResetTime         string  `json:"resetTime"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&quotaResp); err != nil {
		logging.Debugf("antigravity: failed to decode quota response: %v", err)
		return nil
	}

	snapshot := &RealQuotaSnapshot{
		RemainingFraction: quotaResp.RemainingFraction,
		FetchedAt:         time.Now(),
	}

	if quotaResp.ResetTime != "" {
		if t, err := time.Parse(time.RFC3339, quotaResp.ResetTime); err == nil {
			snapshot.WindowResetAt = t
		}
	}

	return snapshot
}

// getCachedQuota returns cached quota if valid, nil otherwise
func getCachedQuota(accessToken string) *RealQuotaSnapshot {
	quotaCacheMu.RLock()
	defer quotaCacheMu.RUnlock()

	entry, ok := quotaCache[accessToken]
	if !ok {
		return nil
	}

	if time.Since(entry.fetchedAt) > quotaCacheTTL {
		return nil
	}

	return entry.snapshot
}

// setCachedQuota stores quota in cache
func setCachedQuota(accessToken string, snapshot *RealQuotaSnapshot) {
	quotaCacheMu.Lock()
	defer quotaCacheMu.Unlock()

	quotaCache[accessToken] = &quotaCacheEntry{
		snapshot:  snapshot,
		fetchedAt: time.Now(),
	}
}

// ClearQuotaCache clears all cached quota data
func ClearQuotaCache() {
	quotaCacheMu.Lock()
	defer quotaCacheMu.Unlock()
	quotaCache = make(map[string]*quotaCacheEntry)
}

// GetQuotaStats returns cache statistics for debugging
func GetQuotaStats() (fetches, hits int64, cacheSize int) {
	quotaCacheMu.RLock()
	defer quotaCacheMu.RUnlock()
	return quotaFetchCount, quotaCacheHit, len(quotaCache)
}

type quotaFetcherStats struct {
	Fetches   int64 `json:"fetches"`
	CacheHits int64 `json:"cacheHits"`
	CacheSize int   `json:"cacheSize"`
}

func (q *quotaFetcherStats) String() string {
	return fmt.Sprintf("fetches=%d cache_hits=%d cache_size=%d", q.Fetches, q.CacheHits, q.CacheSize)
}
