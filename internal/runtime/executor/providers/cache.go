package providers

import (
	"sync"
	"time"
)

type CodexCache struct {
	ID     string
	Expire time.Time
}

var (
	codexCacheMap = map[string]CodexCache{}
	codexCacheMu  sync.RWMutex
)

var initCodexCacheCleanup = sync.OnceFunc(func() {
	go func() {
		ticker := time.NewTicker(10 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			CleanupExpiredCodexCache()
		}
	}()
})

func InitCodexCacheCleanup() {
	initCodexCacheCleanup()
}

func CleanupExpiredCodexCache() {
	now := time.Now()
	codexCacheMu.Lock()
	defer codexCacheMu.Unlock()
	for key, cache := range codexCacheMap {
		if cache.Expire.Before(now) {
			delete(codexCacheMap, key)
		}
	}
}

func GetCodexCache(key string) (CodexCache, bool) {
	InitCodexCacheCleanup()
	codexCacheMu.RLock()
	defer codexCacheMu.RUnlock()
	c, ok := codexCacheMap[key]
	if !ok || c.Expire.Before(time.Now()) {
		return CodexCache{}, false
	}
	return c, true
}

func SetCodexCache(key string, c CodexCache) {
	InitCodexCacheCleanup()
	codexCacheMu.Lock()
	defer codexCacheMu.Unlock()
	codexCacheMap[key] = c
}
