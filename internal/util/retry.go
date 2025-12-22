// Package util provides common utilities used throughout the application.
package util

import (
	"context"
	"fmt"
	"time"

	logrus "github.com/sirupsen/logrus"
)

// WithRetry executes a function with exponential backoff retry logic.
// It provides resilience against temporary failures by automatically retrying
// the operation with increasing delays between attempts.
// Type Parameters:
//   - T: The return type of the function to be retried
// Parameters:
//   - ctx: Context for cancellation and deadline control
//   - maxRetries: Maximum number of retry attempts (including initial try)
//   - logPrefix: Prefix for log messages (e.g., "Token refresh")
//   - fn: Function to execute, should be idempotent
// Returns:
//   - T: Result of the successful function call
//   - error: Last error if all retries fail, or context error if cancelled
// Example:
//	result, err := WithRetry(ctx, 3, "API call", func(ctx context.Context) (MyResult, error) {
//	    return makeAPICall(ctx, params)
//	})
func WithRetry[T any](ctx context.Context, maxRetries int, logPrefix string, fn func(ctx context.Context) (T, error)) (T, error) {
	var zero T
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		// Exponential backoff delay for retries (skip delay on first attempt)
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return zero, ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
				// Continue with retry
			}
		}

		// Execute the function
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}

		// Log the failure and save for final error
		lastErr = err
		logrus.Warnf("%s attempt %d failed: %v", logPrefix, attempt+1, err)
	}

	// All retries exhausted
	return zero, fmt.Errorf("%s failed after %d attempts: %w", logPrefix, maxRetries, lastErr)
}
