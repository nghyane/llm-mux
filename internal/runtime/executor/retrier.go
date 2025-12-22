package executor

import (
	"context"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	// Rate limit retry settings: 1 retry to handle transient glitches, then failover to next provider
	rateLimitMaxRetries = 1
	rateLimitBaseDelay  = 1 * time.Second  // 1s, 2s, 4s, 8s, 16s = ~31s total with exponential backoff
	rateLimitMaxDelay   = 20 * time.Second // Cap individual delay at 20s
)

// rateLimitRetrier handles rate limit (429) errors with exponential backoff retry logic.
type rateLimitRetrier struct {
	retryCount int
}

// rateLimitAction represents the action to take after handling a rate limit error.
type rateLimitAction int

const (
	rateLimitActionContinue    rateLimitAction = iota // Continue to next model
	rateLimitActionRetry                              // Retry same model after delay
	rateLimitActionMaxExceeded                        // Max retries exceeded, stop
)

// handleRateLimit processes a 429 rate limit error and returns the appropriate action.
// It handles model fallback first, then applies exponential backoff with retries.
// Returns the action to take and waits if necessary (respecting context cancellation).
func (r *rateLimitRetrier) handleRateLimit(ctx context.Context, hasNextModel bool, errorBody []byte) (rateLimitAction, error) {
	// Try next model first if available
	if hasNextModel {
		return rateLimitActionContinue, nil
	}

	// No more models - apply exponential backoff with retries
	if r.retryCount >= rateLimitMaxRetries {
		log.Debug("executor: rate limited, max retries exceeded")
		return rateLimitActionMaxExceeded, nil
	}

	delay := r.calculateDelay(errorBody)
	r.retryCount++
	log.Debugf("executor: rate limited, waiting %v before retry %d/%d", delay, r.retryCount, rateLimitMaxRetries)

	select {
	case <-ctx.Done():
		return rateLimitActionMaxExceeded, ctx.Err()
	case <-time.After(delay):
	}

	return rateLimitActionRetry, nil
}

// calculateDelay calculates the delay for rate limit retry with exponential backoff.
// It first tries to use the server-provided retry delay from the error response,
// then falls back to exponential backoff: 1s, 2s, 4s, 8s, 16s (capped at 20s).
func (r *rateLimitRetrier) calculateDelay(errorBody []byte) time.Duration {
	// First, try to use server-provided retry delay
	if serverDelay, err := parseRetryDelay(errorBody); err == nil && serverDelay != nil {
		delay := *serverDelay
		// Add a small buffer to the server-provided delay
		delay += 500 * time.Millisecond
		if delay > rateLimitMaxDelay {
			delay = rateLimitMaxDelay
		}
		return delay
	}

	// Fall back to exponential backoff: baseDelay * 2^retryCount
	delay := rateLimitBaseDelay * time.Duration(1<<r.retryCount)
	if delay > rateLimitMaxDelay {
		delay = rateLimitMaxDelay
	}
	return delay
}

// parseRetryDelay extracts the retry delay from a Google API 429 error response.
// The error response contains a RetryInfo.retryDelay field in the format "0.847655010s".
// Handles both formats:
//   - Object: {"error": {"details": [...]}}
//   - Array:  [{"error": {"details": [...]}}]
// Returns the parsed duration or an error if it cannot be determined.
func parseRetryDelay(errorBody []byte) (*time.Duration, error) {
	// Try multiple paths to handle different response formats
	paths := []string{
		"error.details",   // Standard: {"error": {"details": [...]}}
		"0.error.details", // Array wrapped: [{"error": {"details": [...]}]
	}

	var details gjson.Result
	for _, path := range paths {
		details = gjson.GetBytes(errorBody, path)
		if details.Exists() && details.IsArray() {
			break
		}
	}

	if !details.Exists() || !details.IsArray() {
		return nil, fmt.Errorf("no error.details found")
	}

	for _, detail := range details.Array() {
		typeVal := detail.Get("@type").String()
		if typeVal == "type.googleapis.com/google.rpc.RetryInfo" {
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay != "" {
				// Parse duration string like "0.847655010s"
				duration, err := time.ParseDuration(retryDelay)
				if err != nil {
					return nil, fmt.Errorf("failed to parse duration: %w", err)
				}
				return &duration, nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}

// ParseQuotaRetryDelay extracts the full quota reset delay from a Google API 429 error response.
// Unlike parseRetryDelay which is used for short-term retries (capped at 20s), this function
// returns the actual quota reset time which can be hours.
// It checks multiple sources in order of preference:
//  1. RetryInfo.retryDelay (e.g., "7118.204539195s") - most accurate
//  2. ErrorInfo.metadata.quotaResetDelay (e.g., "1h58m38.204539195s") - human-readable format
// Returns nil if no quota delay information is found.
func ParseQuotaRetryDelay(errorBody []byte) *time.Duration {
	paths := []string{
		"error.details",
		"0.error.details",
	}

	var details gjson.Result
	for _, path := range paths {
		details = gjson.GetBytes(errorBody, path)
		if details.Exists() && details.IsArray() {
			break
		}
	}

	if !details.Exists() || !details.IsArray() {
		return nil
	}

	var quotaResetDelay *time.Duration

	for _, detail := range details.Array() {
		typeVal := detail.Get("@type").String()

		// Prefer RetryInfo.retryDelay (more precise, in seconds)
		if typeVal == "type.googleapis.com/google.rpc.RetryInfo" {
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay != "" {
				if duration, err := time.ParseDuration(retryDelay); err == nil && duration > 0 {
					return &duration
				}
			}
		}

		// Fallback to ErrorInfo.metadata.quotaResetDelay
		if typeVal == "type.googleapis.com/google.rpc.ErrorInfo" {
			quotaDelay := detail.Get("metadata.quotaResetDelay").String()
			if quotaDelay != "" {
				if duration, err := time.ParseDuration(quotaDelay); err == nil && duration > 0 {
					quotaResetDelay = &duration
				}
			}
		}
	}

	return quotaResetDelay
}
