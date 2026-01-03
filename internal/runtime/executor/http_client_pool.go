package executor

import (
	"net/http"
	"sync"
	"time"
)

const (
	// maxTransportCacheSize limits the number of cached transports to prevent memory leaks
	maxTransportCacheSize = 100
	// transportCacheExpiry is the duration after which unused transports are cleaned up
	transportCacheExpiry = 30 * time.Minute
	// cleanupInterval is how often the cleanup goroutine runs
	cleanupInterval = 5 * time.Minute
)

// cachedTransport wraps a transport with usage tracking for LRU eviction
type cachedTransport struct {
	transport *http.Transport
	lastUsed  time.Time
}

// httpClientPool pools http.Client instances keyed by transport.
// Since http.Client is a lightweight struct (~3 pointers), the real benefit
// is ensuring connection reuse via shared transports.
var httpClientPool = sync.Pool{
	New: func() any {
		return &http.Client{}
	},
}

var (
	transportCache     = make(map[string]*cachedTransport)
	transportCacheMu   sync.RWMutex
	cleanupInitialized sync.Once
)

// AcquireHTTPClient gets an http.Client from the pool.
// The returned client has no timeout set - callers should use context
// for request timeouts instead of http.Client.Timeout.
func AcquireHTTPClient() *http.Client {
	return httpClientPool.Get().(*http.Client)
}

// ReleaseHTTPClient returns an http.Client to the pool after resetting its state.
// The transport is preserved since it manages the connection pool.
func ReleaseHTTPClient(c *http.Client) {
	if c == nil {
		return
	}
	// Reset client state but preserve transport for connection reuse
	c.Timeout = 0
	c.CheckRedirect = nil
	c.Jar = nil
	httpClientPool.Put(c)
}

// getCachedTransport returns a cached transport for the given proxy URL,
// creating one if it doesn't exist. Returns nil for empty proxyURL.
func getCachedTransport(proxyURL string) *http.Transport {
	if proxyURL == "" {
		return nil
	}

	cleanupInitialized.Do(initTransportCleanup)

	// Fast path: read lock
	transportCacheMu.RLock()
	if cached, ok := transportCache[proxyURL]; ok {
		cached.lastUsed = time.Now()
		transportCacheMu.RUnlock()
		return cached.transport
	}
	transportCacheMu.RUnlock()

	// Slow path: write lock
	transportCacheMu.Lock()
	defer transportCacheMu.Unlock()

	// Double-check after acquiring write lock
	if cached, ok := transportCache[proxyURL]; ok {
		cached.lastUsed = time.Now()
		return cached.transport
	}

	// Evict LRU entry if cache is full
	if len(transportCache) >= maxTransportCacheSize {
		evictLRUTransport()
	}

	// Build and cache the transport
	t := buildProxyTransport(proxyURL)
	if t != nil {
		transportCache[proxyURL] = &cachedTransport{
			transport: t,
			lastUsed:  time.Now(),
		}
	}
	return t
}

// ClearTransportCache clears all cached proxy transports.
// Useful for testing or when proxy configuration changes.
func ClearTransportCache() {
	transportCacheMu.Lock()
	defer transportCacheMu.Unlock()

	for _, cached := range transportCache {
		cached.transport.CloseIdleConnections()
	}
	transportCache = make(map[string]*cachedTransport)
}

func initTransportCleanup() {
	go func() {
		ticker := time.NewTicker(cleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			cleanupExpiredTransports()
		}
	}()
}

func cleanupExpiredTransports() {
	transportCacheMu.Lock()
	defer transportCacheMu.Unlock()

	now := time.Now()
	for key, cached := range transportCache {
		if now.Sub(cached.lastUsed) > transportCacheExpiry {
			cached.transport.CloseIdleConnections()
			delete(transportCache, key)
		}
	}
}

func evictLRUTransport() {
	var oldestKey string
	var oldestTime time.Time

	for key, cached := range transportCache {
		if oldestKey == "" || cached.lastUsed.Before(oldestTime) {
			oldestKey = key
			oldestTime = cached.lastUsed
		}
	}

	if oldestKey != "" {
		if cached, ok := transportCache[oldestKey]; ok {
			cached.transport.CloseIdleConnections()
		}
		delete(transportCache, oldestKey)
	}
}
