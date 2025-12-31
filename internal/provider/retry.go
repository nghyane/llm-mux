package provider

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
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
	m.mu.RLock()
	defer m.mu.RUnlock()
	var (
		found   bool
		minWait time.Duration
	)
	for _, auth := range m.auths {
		if auth == nil {
			continue
		}
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		if _, ok := providerSet[providerKey]; !ok {
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
// Returns (0, true) to retry immediately, (0, false) to fail fast.
// Manager no longer waits for cooldowns - fails fast and lets client retry.
func (m *Manager) shouldRetryAfterError(err error, attempt, maxAttempts int, providers []string, model string, maxWait time.Duration) (time.Duration, bool) {
	if err == nil || attempt >= maxAttempts-1 {
		return 0, false
	}

	category := categoryFromError(err)
	if !category.ShouldFallback() {
		return 0, false
	}

	if status := statusCodeFromError(err); status == http.StatusOK {
		return 0, false
	}

	_, found := m.closestCooldownWait(providers, model)
	if found {
		return 0, false
	}
	return 0, true
}

// categoryFromError extracts ErrorCategory from error.
func categoryFromError(err error) ErrorCategory {
	if err == nil {
		return CategoryUnknown
	}
	// Check if error has Category() method
	type categorizer interface {
		Category() ErrorCategory
	}
	if c, ok := err.(categorizer); ok {
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
