package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/nghyane/llm-mux/internal/registry"
)

const (
	quotaBackoffBase = time.Second
	quotaBackoffMax  = 30 * time.Minute
)

var quotaCooldownDisabled atomic.Bool

// SetQuotaCooldownDisabled toggles quota cooldown scheduling globally.
func SetQuotaCooldownDisabled(disable bool) {
	quotaCooldownDisabled.Store(disable)
}

// retrySettings retrieves current retry configuration.
func (m *Manager) retrySettings() (int, time.Duration) {
	if m == nil {
		return 0, 0
	}
	return int(m.requestRetry.Load()), time.Duration(m.maxRetryInterval.Load())
}

// closestCooldownWait finds the minimum wait time across all providers for a model.
func (m *Manager) closestCooldownWait(providers []string, model string) (time.Duration, bool) {
	if m == nil || len(providers) == 0 {
		return 0, false
	}
	now := time.Now()
	providerSet := make(map[string]struct{}, len(providers))
	for i := range providers {
		key := strings.ToLower(strings.TrimSpace(providers[i]))
		if key == "" {
			continue
		}
		providerSet[key] = struct{}{}
	}

	modelKey := strings.TrimSpace(model)
	registryRef := registry.GetGlobalRegistry()

	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found   bool
		minWait time.Duration
	)
	for _, auth := range m.auths {
		if auth == nil || auth.Disabled {
			continue
		}
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}
		if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(auth.ID, modelKey) {
			continue
		}
		blocked, reason, next := isAuthBlockedForModel(auth, model, now)
		if !blocked || next.IsZero() || reason == blockReasonDisabled {
			continue
		}
		wait := next.Sub(now)
		if wait < 0 {
			continue
		}
		if !found || wait < minWait {
			minWait = wait
			found = true
		}
	}
	return minWait, found
}

// shouldRetryAfterError determines if execution should be retried after an error.
// Returns true if retry should be attempted (after waiting for available auth).
func (m *Manager) shouldRetryAfterError(err error, attempt, maxAttempts int, providers []string, model string) bool {
	if err == nil || attempt >= maxAttempts-1 {
		return false
	}

	category := categoryFromError(err)
	if !category.ShouldFallback() {
		return false
	}

	if status := statusCodeFromError(err); status == http.StatusOK {
		return false
	}

	if m.hasAvailableAuth(providers, model) {
		return true
	}

	if _, found := m.closestCooldownWait(providers, model); found {
		return true
	}

	return false
}

// hasAvailableAuth checks if any auth for the given providers/model is available (not blocked).
// This is used to determine if a retry would be useful.
func (m *Manager) hasAvailableAuth(providers []string, model string) bool {
	if m == nil || len(providers) == 0 {
		return false
	}

	now := time.Now()
	providerSet := make(map[string]struct{}, len(providers))
	for _, p := range providers {
		key := strings.ToLower(strings.TrimSpace(p))
		if key != "" {
			providerSet[key] = struct{}{}
		}
	}

	modelKey := strings.TrimSpace(model)
	registryRef := registry.GetGlobalRegistry()

	m.mu.RLock()
	defer m.mu.RUnlock()

	for _, auth := range m.auths {
		if auth == nil || auth.Disabled {
			continue
		}

		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
			continue
		}

		// Check if auth supports the model (same filter as pickNext)
		if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(auth.ID, modelKey) {
			continue
		}

		// Check if auth is blocked for this model
		blocked, _, _ := isAuthBlockedForModel(auth, model, now)
		if !blocked {
			return true
		}
	}

	return false
}

// categoryFromError extracts ErrorCategory from error.
// Uses errors.As to properly unwrap wrapped errors.
func categoryFromError(err error) ErrorCategory {
	if err == nil {
		return CategoryUnknown
	}
	// Check if error has Category() method using errors.As to unwrap
	type categorizer interface {
		Category() ErrorCategory
	}
	var c categorizer
	if errors.As(err, &c) && c != nil {
		return c.Category()
	}
	// Fallback to status code classification
	status := statusCodeFromError(err)
	msg := err.Error()
	return CategorizeError(status, msg)
}

// statusCodeFromError extracts HTTP status code from error.
func statusCodeFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		return sc.StatusCode()
	}
	return 0
}

// retryAfterFromError extracts retry-after duration from error.
// Uses errors.As to properly unwrap wrapped errors.
func retryAfterFromError(err error) *time.Duration {
	if err == nil {
		return nil
	}
	type retryAfterProvider interface {
		RetryAfter() *time.Duration
	}
	var rap retryAfterProvider
	if !errors.As(err, &rap) || rap == nil {
		return nil
	}
	retryAfter := rap.RetryAfter()
	if retryAfter == nil {
		return nil
	}
	val := *retryAfter
	return &val
}

// waitForCooldown blocks until the cooldown period expires or context is cancelled.
func waitForCooldown(ctx context.Context, wait time.Duration) error {
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// errCooldownTimeout is returned when waitForAvailableAuth times out waiting for an available auth.
var errCooldownTimeout = errors.New("timeout waiting for available auth")

const cooldownPollInterval = 500 * time.Millisecond

func (m *Manager) waitForAvailableAuth(ctx context.Context, providers []string, model string, maxWait time.Duration) error {
	if maxWait <= 0 {
		return nil
	}

	if m.hasAvailableAuth(providers, model) {
		return nil
	}

	deadline := time.Now().Add(maxWait)
	ticker := time.NewTicker(cooldownPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			if m.hasAvailableAuth(providers, model) {
				return nil
			}
			if now.After(deadline) {
				return errCooldownTimeout
			}
		}
	}
}

// nextQuotaCooldown returns the next cooldown duration and updated backoff level for repeated quota errors.
func nextQuotaCooldown(prevLevel int) (time.Duration, int) {
	if prevLevel < 0 {
		prevLevel = 0
	}
	if quotaCooldownDisabled.Load() {
		return 0, prevLevel
	}
	cooldown := quotaBackoffBase * time.Duration(1<<prevLevel)
	if cooldown < quotaBackoffBase {
		cooldown = quotaBackoffBase
	}
	if cooldown >= quotaBackoffMax {
		return quotaBackoffMax, prevLevel
	}
	return cooldown, prevLevel + 1
}
