package executor

import (
	"context"
	"fmt"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/resilience"
	"github.com/tidwall/gjson"
)

const (
	rateLimitMaxRetries = 1
)

type RetryConfig struct {
	MaxRetries       int
	BaseDelay        time.Duration
	MaxDelay         time.Duration
	RetryStatusCodes []int
	FallbackCodes    []int
	RetryOnErrors    bool
}

type RetryHandler struct {
	config  RetryConfig
	retrier rateLimitRetrier
}

type RetryAction int

const (
	RetryActionSuccess RetryAction = iota
	RetryActionContinueNext
	RetryActionRetryCurrent
	RetryActionFail
)

func (a RetryAction) String() string {
	switch a {
	case RetryActionSuccess:
		return "Success"
	case RetryActionContinueNext:
		return "ContinueNext"
	case RetryActionRetryCurrent:
		return "RetryCurrent"
	case RetryActionFail:
		return "Fail"
	default:
		return fmt.Sprintf("Unknown(%d)", int(a))
	}
}

func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:       1,
		BaseDelay:        RateLimitBaseDelay,
		MaxDelay:         RateLimitMaxDelay,
		RetryStatusCodes: []int{500},
		FallbackCodes:    []int{429, 502, 503, 504},
		RetryOnErrors:    true,
	}
}

func AntigravityRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries:       1,
		BaseDelay:        AntigravityRetryBaseDelay,
		MaxDelay:         AntigravityRetryMaxDelay,
		RetryStatusCodes: []int{500},
		FallbackCodes:    []int{429, 502, 503, 504},
		RetryOnErrors:    true,
	}
}

func NewRetryHandler(cfg RetryConfig) *RetryHandler {
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 1
	}
	if cfg.BaseDelay == 0 {
		cfg.BaseDelay = RateLimitBaseDelay
	}
	if cfg.MaxDelay == 0 {
		cfg.MaxDelay = RateLimitMaxDelay
	}
	if len(cfg.RetryStatusCodes) == 0 {
		cfg.RetryStatusCodes = []int{500}
	}
	if len(cfg.FallbackCodes) == 0 {
		cfg.FallbackCodes = []int{429, 502, 503, 504}
	}

	return &RetryHandler{
		config:  cfg,
		retrier: rateLimitRetrier{},
	}
}

func (h *RetryHandler) HandleResponse(ctx context.Context, statusCode int, body []byte, hasNextTarget bool) (RetryAction, error) {
	if statusCode >= 200 && statusCode < 300 {
		return RetryActionSuccess, nil
	}

	isRetryable := h.isRetryableStatus(statusCode)
	isFallbackCode := h.isFallbackCode(statusCode)

	// Smart Retry for Fallback codes (429, 503):
	// Instead of failing immediately, check if the requested wait time is short (transient error).
	// If wait time is short (<= MaxDelay), treat as retryable to preserve sticky session.
	// If wait time is long (Quota exceeded), fail immediately to trigger Provider Manager rotation.
	if isFallbackCode {
		delay := h.calculateDelay(body)
		if delay <= h.config.MaxDelay {
			log.Debugf("retry_handler: status %d (fallback code), but delay %v is within limit, converting to local retry", statusCode, delay)
			isRetryable = true
			isFallbackCode = false
		}
	}

	// Fallback codes (429, 503) should immediately try next target if available.
	// This allows Provider Manager to handle cross-auth/cross-provider fallback.
	if isFallbackCode {
		if hasNextTarget {
			log.Debugf("retry_handler: status %d, trying next executor target", statusCode)
			return RetryActionContinueNext, nil
		}
		// No more executor-level targets (e.g., base URLs exhausted).
		// Return error to Provider Manager for cross-account rotation.
		// This is expected behavior for quota exhaustion (429).
		log.Debugf("retry_handler: status %d, executor targets exhausted, returning to manager for account rotation", statusCode)
		return RetryActionFail, nil
	}

	// For non-fallback retryable codes (e.g., 500 without 503), retry with delay
	if isRetryable && h.retrier.retryCount < h.config.MaxRetries {
		delay := h.calculateDelay(body)
		h.retrier.retryCount++
		log.Debugf("retry_handler: status %d, waiting %v before retry %d/%d",
			statusCode, delay, h.retrier.retryCount, h.config.MaxRetries)

		select {
		case <-ctx.Done():
			return RetryActionFail, ctx.Err()
		case <-time.After(delay):
		}

		return RetryActionRetryCurrent, nil
	}

	if hasNextTarget && isRetryable {
		log.Debugf("retry_handler: retries exhausted for status %d, trying next target", statusCode)
		return RetryActionContinueNext, nil
	}

	log.Debugf("retry_handler: status %d, no more retries or targets available", statusCode)
	return RetryActionFail, nil
}

func (h *RetryHandler) HandleError(ctx context.Context, err error, hasNextTarget bool) (RetryAction, error) {
	if err == nil {
		return RetryActionSuccess, nil
	}

	if ctx.Err() != nil {
		return RetryActionFail, ctx.Err()
	}

	if hasNextTarget {
		log.Debugf("retry_handler: error occurred, falling back to next target: %v", err)
		return RetryActionContinueNext, nil
	}

	if h.config.RetryOnErrors && h.retrier.retryCount < h.config.MaxRetries {
		delay := h.calculateDelayForError()
		h.retrier.retryCount++
		log.Debugf("retry_handler: error occurred, waiting %v before retry %d/%d: %v",
			delay, h.retrier.retryCount, h.config.MaxRetries, err)

		select {
		case <-ctx.Done():
			return RetryActionFail, ctx.Err()
		case <-time.After(delay):
		}

		return RetryActionRetryCurrent, nil
	}

	return RetryActionFail, err
}

func (h *RetryHandler) Reset() {
	h.retrier.retryCount = 0
}

func (h *RetryHandler) RetryCount() int {
	return h.retrier.retryCount
}

func (h *RetryHandler) Config() RetryConfig {
	return h.config
}

func (h *RetryHandler) isRetryableStatus(statusCode int) bool {
	for _, code := range h.config.RetryStatusCodes {
		if statusCode == code {
			return true
		}
	}
	return false
}

func (h *RetryHandler) isFallbackCode(statusCode int) bool {
	for _, code := range h.config.FallbackCodes {
		if statusCode == code {
			return true
		}
	}
	return false
}

func (h *RetryHandler) calculateDelay(body []byte) time.Duration {
	if serverDelay, err := parseRetryDelay(body); err == nil && serverDelay != nil {
		delay := *serverDelay + 500*time.Millisecond
		if delay > h.config.MaxDelay {
			delay = h.config.MaxDelay
		}
		return delay
	}

	return h.calculateDelayForError()
}

func (h *RetryHandler) calculateDelayForError() time.Duration {
	jitter := h.config.MaxDelay / 4
	return resilience.CalculateBackoff(h.retrier.retryCount, h.config.BaseDelay, h.config.MaxDelay, jitter)
}

type rateLimitRetrier struct {
	retryCount int
}

// RateLimitRetrier is an exported alias for rateLimitRetrier.
type RateLimitRetrier = rateLimitRetrier

type rateLimitAction int

// RateLimitAction is an exported alias for rateLimitAction.
type RateLimitAction = rateLimitAction

const (
	rateLimitActionContinue rateLimitAction = iota
	rateLimitActionRetry
	rateLimitActionMaxExceeded
)

// Exported constants for rate limit actions.
const (
	RateLimitActionContinue    = rateLimitActionContinue
	RateLimitActionRetry       = rateLimitActionRetry
	RateLimitActionMaxExceeded = rateLimitActionMaxExceeded
)

func (r *rateLimitRetrier) handleRateLimit(ctx context.Context, hasNextModel bool, errorBody []byte) (rateLimitAction, error) {
	if hasNextModel {
		return rateLimitActionContinue, nil
	}

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

// HandleRateLimit is an exported alias for handleRateLimit.
func (r *RateLimitRetrier) HandleRateLimit(ctx context.Context, hasNextModel bool, errorBody []byte) (RateLimitAction, error) {
	return r.handleRateLimit(ctx, hasNextModel, errorBody)
}

func (r *rateLimitRetrier) calculateDelay(errorBody []byte) time.Duration {
	if serverDelay, err := parseRetryDelay(errorBody); err == nil && serverDelay != nil {
		delay := *serverDelay + 500*time.Millisecond
		if delay > RateLimitMaxDelay {
			delay = RateLimitMaxDelay
		}
		return delay
	}
	return resilience.CalculateBackoff(r.retryCount, RateLimitBaseDelay, RateLimitMaxDelay, 0)
}

func parseRetryDelay(errorBody []byte) (*time.Duration, error) {
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
		return nil, fmt.Errorf("no error.details found")
	}

	for _, detail := range details.Array() {
		typeVal := detail.Get("@type").String()
		if typeVal == "type.googleapis.com/google.rpc.RetryInfo" {
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay != "" {
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

// ParseRetryDelay is an exported alias for parseRetryDelay.
func ParseRetryDelay(errorBody []byte) (*time.Duration, error) {
	return parseRetryDelay(errorBody)
}

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

		if typeVal == "type.googleapis.com/google.rpc.RetryInfo" {
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay != "" {
				if duration, err := time.ParseDuration(retryDelay); err == nil && duration > 0 {
					capped := capQuotaDelay(duration)
					return &capped
				}
			}
		}

		if typeVal == "type.googleapis.com/google.rpc.ErrorInfo" {
			quotaDelay := detail.Get("metadata.quotaResetDelay").String()
			if quotaDelay != "" {
				if duration, err := time.ParseDuration(quotaDelay); err == nil && duration > 0 {
					capped := capQuotaDelay(duration)
					quotaResetDelay = &capped
				}
			}

			if quotaResetDelay == nil {
				resetTime := detail.Get("metadata.quotaInfo.resetTime").String()
				if resetTime != "" {
					if parsed, err := time.Parse(time.RFC3339, resetTime); err == nil {
						duration := time.Until(parsed)
						if duration > 0 {
							capped := capQuotaDelay(duration)
							quotaResetDelay = &capped
						}
					}
				}
			}
		}
	}

	return quotaResetDelay
}

func capQuotaDelay(d time.Duration) time.Duration {
	if d > MaxQuotaRetryDelay {
		return MaxQuotaRetryDelay
	}
	return d
}
