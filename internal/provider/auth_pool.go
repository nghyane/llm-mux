package provider

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
)

type RefreshFn func(ctx context.Context, auth *Auth) (token string, expiresIn time.Duration, err error)

type AuthEntry struct {
	Auth *Auth

	accessToken    atomic.Pointer[string]
	tokenExpiresAt atomic.Int64
	tokenRefreshAt atomic.Int64
	refreshing     atomic.Bool

	activeRequests  atomic.Int64
	cooldownUntil   atomic.Int64
	totalTokensUsed atomic.Int64
	realQuota       atomic.Pointer[RealQuotaSnapshot]

	refreshFn RefreshFn
}

func (e *AuthEntry) Ready(buffer time.Duration) bool {
	now := time.Now()

	cooldown := e.cooldownUntil.Load()
	if cooldown > 0 && now.UnixNano() < cooldown {
		return false
	}

	expiresAt := e.tokenExpiresAt.Load()
	if expiresAt == 0 || now.Add(buffer).UnixNano() > expiresAt {
		return false
	}

	return true
}

func (e *AuthEntry) GetToken() string {
	if ptr := e.accessToken.Load(); ptr != nil {
		return *ptr
	}
	return ""
}

func (e *AuthEntry) SetToken(token string, expiresAt, refreshAt time.Time) {
	e.accessToken.Store(&token)
	e.tokenExpiresAt.Store(expiresAt.UnixNano())
	e.tokenRefreshAt.Store(refreshAt.UnixNano())
}

func (e *AuthEntry) IsTokenValid(buffer time.Duration) bool {
	expiresAt := e.tokenExpiresAt.Load()
	if expiresAt == 0 {
		return false
	}
	return time.Now().Add(buffer).UnixNano() < expiresAt
}

func (e *AuthEntry) NeedsTokenRefresh() bool {
	refreshAt := e.tokenRefreshAt.Load()
	if refreshAt == 0 {
		return false
	}
	return time.Now().UnixNano() > refreshAt
}

func (e *AuthEntry) IncrementActive() {
	e.activeRequests.Add(1)
}

func (e *AuthEntry) DecrementActive() {
	for {
		current := e.activeRequests.Load()
		if current <= 0 {
			break
		}
		if e.activeRequests.CompareAndSwap(current, current-1) {
			break
		}
	}
}

func (e *AuthEntry) SetCooldown(until time.Time) {
	e.cooldownUntil.Store(until.UnixNano())
}

type AuthPool struct {
	mu      sync.RWMutex
	entries map[string]*AuthEntry

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewAuthPool() *AuthPool {
	return &AuthPool{
		entries:  make(map[string]*AuthEntry),
		stopChan: make(chan struct{}),
	}
}

func (p *AuthPool) Start() {
	p.wg.Add(1)
	go p.refreshLoop()
}

func (p *AuthPool) Stop() {
	p.stopOnce.Do(func() { close(p.stopChan) })
	p.wg.Wait()
}

func (p *AuthPool) Register(auth *Auth, refreshFn RefreshFn) {
	p.mu.Lock()
	defer p.mu.Unlock()

	entry := &AuthEntry{
		Auth:      auth,
		refreshFn: refreshFn,
	}

	if token, ok := auth.Metadata["access_token"].(string); ok && token != "" {
		entry.accessToken.Store(&token)
		if exp, ok := auth.Metadata["expired"].(string); ok {
			if t, err := time.Parse(time.RFC3339, exp); err == nil {
				entry.tokenExpiresAt.Store(t.UnixNano())
				entry.tokenRefreshAt.Store(t.Add(-5 * time.Minute).UnixNano())
			}
		}
	}

	p.entries[auth.ID] = entry
}

func (p *AuthPool) Unregister(authID string) {
	p.mu.Lock()
	delete(p.entries, authID)
	p.mu.Unlock()
}

func (p *AuthPool) GetEntry(authID string) *AuthEntry {
	p.mu.RLock()
	entry := p.entries[authID]
	p.mu.RUnlock()
	return entry
}

func (p *AuthPool) GetReady(provider, model string, buffer time.Duration) []*AuthEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var ready []*AuthEntry
	for _, entry := range p.entries {
		if entry.Auth.Provider != provider {
			continue
		}
		if entry.Auth.Disabled {
			continue
		}
		if !entry.Ready(buffer) {
			continue
		}
		ready = append(ready, entry)
	}
	return ready
}

func (p *AuthPool) Pick(provider, model string) (*AuthEntry, error) {
	ready := p.GetReady(provider, model, 30*time.Second)
	if len(ready) == 0 {
		return nil, &Error{Code: "no_ready_auth", Message: "no auth available", HTTPStatus: 503}
	}

	best := ready[0]
	bestActive := best.activeRequests.Load()
	for _, entry := range ready[1:] {
		active := entry.activeRequests.Load()
		if active < bestActive {
			best = entry
			bestActive = active
		}
	}

	best.IncrementActive()
	return best, nil
}

func (p *AuthPool) refreshLoop() {
	defer p.wg.Done()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-p.stopChan:
			return
		case <-ticker.C:
			p.refreshExpiring()
		}
	}
}

func (p *AuthPool) refreshExpiring() {
	p.mu.RLock()
	var needsRefresh []*AuthEntry
	now := time.Now().UnixNano()

	for _, entry := range p.entries {
		refreshAt := entry.tokenRefreshAt.Load()
		if refreshAt > 0 && now > refreshAt && !entry.refreshing.Load() {
			needsRefresh = append(needsRefresh, entry)
		}
	}
	p.mu.RUnlock()

	for _, entry := range needsRefresh {
		if entry.refreshing.CompareAndSwap(false, true) {
			go p.doRefresh(entry)
		}
	}
}

func (p *AuthPool) doRefresh(entry *AuthEntry) {
	defer entry.refreshing.Store(false)

	if entry.refreshFn == nil {
		return
	}

	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		token, expiresIn, err := entry.refreshFn(ctx, entry.Auth)
		cancel()

		if err == nil && token != "" && expiresIn > 0 {
			now := time.Now()
			expiresAt := now.Add(expiresIn)
			refreshAt := expiresAt.Add(-5 * time.Minute)
			if refreshAt.Before(now) {
				refreshAt = now.Add(time.Minute)
			}
			entry.SetToken(token, expiresAt, refreshAt)
			log.Debugf("authpool: refresh success for %s", entry.Auth.ID)
			return
		}

		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
	}
	log.Warnf("authpool: refresh failed for %s after 3 attempts", entry.Auth.ID)
}

func (p *AuthPool) Size() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return len(p.entries)
}
