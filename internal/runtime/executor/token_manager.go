package executor

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nghyane/llm-mux/internal/provider"
	"golang.org/x/sync/singleflight"

	log "github.com/nghyane/llm-mux/internal/logging"
)

var ErrTokenNotReady = errors.New("token not ready, refresh in progress")

type TokenManagerConfig struct {
	RefreshBuffer   time.Duration
	MinValidTime    time.Duration
	ProactiveCheck  time.Duration
	RefreshTimeout  time.Duration
	CleanupInterval time.Duration
}

func DefaultTokenManagerConfig() TokenManagerConfig {
	return TokenManagerConfig{
		RefreshBuffer:   5 * time.Minute,
		MinValidTime:    30 * time.Second,
		ProactiveCheck:  2 * time.Minute,
		RefreshTimeout:  10 * time.Second, // Reduced from 30s for fail-fast behavior
		CleanupInterval: 5 * time.Minute,
	}
}

type TokenEntry struct {
	Token      string
	ExpiresAt  time.Time
	RefreshAt  time.Time
	Refreshing bool
	Auth       *provider.Auth
}

func (e *TokenEntry) IsValid(minValid time.Duration) bool {
	return e.Token != "" && time.Now().Add(minValid).Before(e.ExpiresAt)
}

func (e *TokenEntry) NeedsRefresh() bool {
	return !e.Refreshing && time.Now().After(e.RefreshAt)
}

type RefreshFunc func(ctx context.Context, auth *provider.Auth) (token string, expiresIn time.Duration, err error)

type TokenStateStore interface {
	GetState(authID string) TokenState
	GetOrCreateState(authID string) TokenState
}

type TokenState interface {
	GetToken() string
	SetToken(token string, expiresAt, refreshAt time.Time)
	IsTokenValid(buffer time.Duration) bool
	NeedsTokenRefresh() bool
}

type TokenManager struct {
	mu         sync.RWMutex
	entries    map[string]*TokenEntry
	stateStore TokenStateStore
	config     TokenManagerConfig
	refresh    RefreshFunc
	sf         singleflight.Group

	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewTokenManager(cfg TokenManagerConfig, refreshFn RefreshFunc) *TokenManager {
	if cfg.RefreshBuffer == 0 {
		cfg.RefreshBuffer = 5 * time.Minute
	}
	if cfg.MinValidTime == 0 {
		cfg.MinValidTime = 30 * time.Second
	}
	if cfg.ProactiveCheck == 0 {
		cfg.ProactiveCheck = 2 * time.Minute
	}
	if cfg.RefreshTimeout == 0 {
		cfg.RefreshTimeout = 30 * time.Second
	}
	if cfg.CleanupInterval == 0 {
		cfg.CleanupInterval = 5 * time.Minute
	}

	tm := &TokenManager{
		entries:  make(map[string]*TokenEntry),
		config:   cfg,
		refresh:  refreshFn,
		stopChan: make(chan struct{}),
	}

	tm.wg.Add(1)
	go tm.maintenanceLoop()

	return tm
}

func (m *TokenManager) SetStateStore(store TokenStateStore) {
	m.stateStore = store
}

func (m *TokenManager) GetToken(ctx context.Context, auth *provider.Auth) (string, error) {
	if auth == nil {
		return "", NewStatusError(401, "missing auth", nil)
	}

	if m.stateStore != nil {
		if state := m.stateStore.GetState(auth.ID); state != nil {
			if state.IsTokenValid(m.config.MinValidTime) {
				if state.NeedsTokenRefresh() {
					m.scheduleRefresh(auth.ID)
				}
				return state.GetToken(), nil
			}
		}
	}

	m.mu.RLock()
	entry, exists := m.entries[auth.ID]
	m.mu.RUnlock()

	if exists && entry.IsValid(m.config.MinValidTime) {
		if entry.NeedsRefresh() {
			m.scheduleRefresh(auth.ID)
		}
		return entry.Token, nil
	}

	token := MetaStringValue(auth.Metadata, "access_token")
	expiry := TokenExpiry(auth.Metadata)
	now := time.Now()

	if token != "" && expiry.After(now.Add(TokenExpiryBuffer)) {
		m.store(auth, token, expiry.Sub(now))
		if expiry.Before(now.Add(10 * time.Minute)) {
			m.scheduleRefresh(auth.ID)
		}
		return token, nil
	}

	m.scheduleRefresh(auth.ID)
	return "", ErrTokenNotReady
}

func (m *TokenManager) refreshSync(ctx context.Context, auth *provider.Auth) (string, error) {
	result, err, _ := m.sf.Do(auth.ID, func() (any, error) {
		m.mu.RLock()
		entry, exists := m.entries[auth.ID]
		m.mu.RUnlock()
		if exists && entry.IsValid(m.config.MinValidTime) {
			return entry.Token, nil
		}

		start := time.Now()
		log.Debugf("token manager: sync refresh starting for %s", auth.ID)

		refreshCtx, cancel := context.WithTimeout(ctx, m.config.RefreshTimeout)
		defer cancel()

		authClone := auth.Clone()
		token, expiresIn, err := m.refresh(refreshCtx, authClone)
		elapsed := time.Since(start)

		if err != nil {
			log.Warnf("token manager: sync refresh failed for %s after %v: %v", auth.ID, elapsed, err)
			return "", err
		}

		log.Debugf("token manager: sync refresh success for %s, took %v", auth.ID, elapsed)
		m.store(authClone, token, expiresIn)
		return token, nil
	})

	if err != nil {
		return "", err
	}
	return result.(string), nil
}

func (m *TokenManager) store(auth *provider.Auth, token string, expiresIn time.Duration) {
	if token == "" || expiresIn <= 0 {
		return
	}

	now := time.Now()
	expiresAt := now.Add(expiresIn)

	refreshAt := expiresAt.Add(-m.config.RefreshBuffer)
	halfLife := now.Add(expiresIn / 2)
	if refreshAt.Before(halfLife) {
		refreshAt = halfLife
	}
	if refreshAt.Before(now) {
		refreshAt = now.Add(time.Minute)
	}

	m.mu.Lock()
	m.entries[auth.ID] = &TokenEntry{
		Token:      token,
		ExpiresAt:  expiresAt,
		RefreshAt:  refreshAt,
		Refreshing: false,
		Auth:       auth,
	}
	m.mu.Unlock()

	if m.stateStore != nil {
		if state := m.stateStore.GetOrCreateState(auth.ID); state != nil {
			state.SetToken(token, expiresAt, refreshAt)
		}
	}

	log.Debugf("token manager: stored %s expires=%v refresh_at=%v",
		auth.ID, expiresAt.Format(time.RFC3339), refreshAt.Format(time.RFC3339))
}

func (m *TokenManager) scheduleRefresh(authID string) {
	m.mu.Lock()
	entry, ok := m.entries[authID]
	if !ok || entry == nil || entry.Refreshing {
		m.mu.Unlock()
		return
	}
	entry.Refreshing = true
	auth := entry.Auth
	m.mu.Unlock()

	go func() {
		defer func() {
			m.mu.Lock()
			if e, exists := m.entries[authID]; exists && e != nil {
				e.Refreshing = false
			}
			m.mu.Unlock()
		}()

		for attempt := 0; attempt < 3; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), m.config.RefreshTimeout)
			authClone := auth.Clone()
			token, expiresIn, err := m.refresh(ctx, authClone)
			cancel()

			if err == nil && token != "" && expiresIn > 0 {
				m.store(authClone, token, expiresIn)
				log.Debugf("token manager: background refresh success for %s", authID)
				return
			}

			if attempt < 2 {
				backoff := time.Duration(attempt+1) * 2 * time.Second
				log.Debugf("token manager: background refresh attempt %d failed for %s: %v, retrying in %v", attempt+1, authID, err, backoff)
				time.Sleep(backoff)
			}
		}
		log.Warnf("token manager: background refresh failed for %s after 3 attempts", authID)
	}()
}

func (m *TokenManager) Invalidate(authID string) {
	m.mu.Lock()
	delete(m.entries, authID)
	m.mu.Unlock()
}

func (m *TokenManager) Stop() {
	m.stopOnce.Do(func() {
		close(m.stopChan)
	})
	m.wg.Wait()
}

func (m *TokenManager) maintenanceLoop() {
	defer m.wg.Done()

	cleanupTicker := time.NewTicker(m.config.CleanupInterval)
	refreshTicker := time.NewTicker(m.config.ProactiveCheck)
	defer cleanupTicker.Stop()
	defer refreshTicker.Stop()

	for {
		select {
		case <-cleanupTicker.C:
			m.cleanup()
		case <-refreshTicker.C:
			m.proactiveRefresh()
		case <-m.stopChan:
			return
		}
	}
}

func (m *TokenManager) cleanup() {
	now := time.Now()
	var expired []string

	m.mu.RLock()
	for id, entry := range m.entries {
		if entry.ExpiresAt.Before(now) {
			expired = append(expired, id)
		}
	}
	m.mu.RUnlock()

	if len(expired) == 0 {
		return
	}

	m.mu.Lock()
	for _, id := range expired {
		delete(m.entries, id)
	}
	m.mu.Unlock()

	log.Debugf("token manager: cleaned %d expired entries", len(expired))
}

func (m *TokenManager) proactiveRefresh() {
	var needsRefresh []string

	m.mu.RLock()
	for id, entry := range m.entries {
		if entry.NeedsRefresh() {
			needsRefresh = append(needsRefresh, id)
		}
	}
	m.mu.RUnlock()

	if len(needsRefresh) == 0 {
		return
	}

	log.Debugf("token manager: proactive refresh for %d entries", len(needsRefresh))

	for _, id := range needsRefresh {
		m.scheduleRefresh(id)
	}
}

func (m *TokenManager) Stats() (total, valid, refreshing int) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, entry := range m.entries {
		total++
		if entry.IsValid(m.config.MinValidTime) {
			valid++
		}
		if entry.Refreshing {
			refreshing++
		}
	}
	return
}

func (m *TokenManager) PreWarm(auth *provider.Auth) {
	if auth == nil {
		return
	}

	m.mu.RLock()
	entry, exists := m.entries[auth.ID]
	m.mu.RUnlock()

	if exists && entry.IsValid(m.config.MinValidTime) {
		return
	}

	token := MetaStringValue(auth.Metadata, "access_token")
	expiry := TokenExpiry(auth.Metadata)
	now := time.Now()

	if token != "" && expiry.After(now.Add(TokenExpiryBuffer)) {
		m.store(auth, token, expiry.Sub(now))
		log.Debugf("token manager: pre-warmed %s from metadata, expires=%v", auth.ID, expiry.Format(time.RFC3339))
		if expiry.Before(now.Add(10 * time.Minute)) {
			m.scheduleRefresh(auth.ID)
		}
		return
	}

	go func() {
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), m.config.RefreshTimeout)
		defer cancel()

		authClone := auth.Clone()
		newToken, expiresIn, err := m.refresh(ctx, authClone)
		elapsed := time.Since(start)

		if err != nil {
			log.Warnf("token manager: pre-warm refresh failed for %s after %v: %v", auth.ID, elapsed, err)
			return
		}

		if newToken != "" && expiresIn > 0 {
			m.store(authClone, newToken, expiresIn)
			log.Infof("token manager: pre-warm success for %s, took %v", auth.ID, elapsed)
		}
	}()
}
