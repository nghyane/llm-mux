package provider

import (
	"net/http"
	"strings"
)

// ErrorCategory classifies errors for retry/fallback decisions
type ErrorCategory int

const (
	// CategoryUnknown is the default category for unclassified errors
	CategoryUnknown ErrorCategory = iota

	// CategoryUserError indicates client-side errors (bad request, invalid params)
	// Should NOT retry or fallback - return error to user immediately
	CategoryUserError

	// CategoryAuthError indicates authentication failures (token expired/revoked)
	// Should disable auth, NOT retry with same auth
	CategoryAuthError

	// CategoryAuthRevoked indicates permanently revoked OAuth tokens
	// Should disable auth permanently
	CategoryAuthRevoked

	// CategoryQuotaError indicates rate limiting or quota exhaustion
	// Should wait cooldown, then retry or fallback to another auth
	CategoryQuotaError

	// CategoryTransient indicates temporary server-side errors
	// Should retry with exponential backoff
	CategoryTransient

	// CategoryNotFound indicates resource not found
	// Should NOT retry
	CategoryNotFound
)

// String returns human-readable category name
func (c ErrorCategory) String() string {
	switch c {
	case CategoryUserError:
		return "user_error"
	case CategoryAuthError:
		return "auth_error"
	case CategoryAuthRevoked:
		return "auth_revoked"
	case CategoryQuotaError:
		return "quota_error"
	case CategoryTransient:
		return "transient"
	case CategoryNotFound:
		return "not_found"
	default:
		return "unknown"
	}
}

// ShouldFallback returns true if should try another auth/provider
func (c ErrorCategory) ShouldFallback() bool {
	return c == CategoryQuotaError || c == CategoryTransient || c == CategoryAuthError
}

// ShouldDisableAuth returns true if auth should be disabled
func (c ErrorCategory) ShouldDisableAuth() bool {
	return c == CategoryAuthRevoked
}

// ShouldSuspendAuth returns true if auth should be temporarily suspended
func (c ErrorCategory) ShouldSuspendAuth() bool {
	return c == CategoryAuthError || c == CategoryQuotaError
}

// IsUserFault returns true if error is caused by user's request
func (c ErrorCategory) IsUserFault() bool {
	return c == CategoryUserError || c == CategoryNotFound
}

// CategorizeHTTPStatus determines category from HTTP status code
func CategorizeHTTPStatus(statusCode int) ErrorCategory {
	switch statusCode {
	case http.StatusBadRequest: // 400
		return CategoryUserError
	case http.StatusUnauthorized: // 401
		return CategoryAuthError
	case http.StatusPaymentRequired, http.StatusForbidden: // 402, 403
		return CategoryQuotaError
	case http.StatusNotFound: // 404
		return CategoryNotFound
	case http.StatusTooManyRequests: // 429
		return CategoryQuotaError
	case http.StatusInternalServerError, // 500
		http.StatusBadGateway,         // 502
		http.StatusServiceUnavailable, // 503
		http.StatusGatewayTimeout:     // 504
		return CategoryTransient
	default:
		if statusCode >= 400 && statusCode < 500 {
			return CategoryUserError
		}
		if statusCode >= 500 {
			return CategoryTransient
		}
		return CategoryUnknown
	}
}

// CategorizeError determines category from error message and status code
func CategorizeError(statusCode int, message string) ErrorCategory {
	// Check for OAuth revoked errors first (most specific)
	if isOAuthRevokedError(message) {
		return CategoryAuthRevoked
	}

	// Check for user errors in message
	if isUserError(message) {
		return CategoryUserError
	}

	// Check for quota errors in message
	if isQuotaError(message) {
		return CategoryQuotaError
	}

	// Fall back to status code classification
	return CategorizeHTTPStatus(statusCode)
}

// isOAuthRevokedError checks if message indicates OAuth token is permanently invalid
func isOAuthRevokedError(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "invalid_grant") ||
		strings.Contains(lower, "token has been expired or revoked") ||
		strings.Contains(lower, "token revoked") ||
		strings.Contains(lower, "oauth_token_revoked")
}

// isUserError checks if message indicates user/request error
func isUserError(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "invalid_argument") ||
		strings.Contains(lower, "invalid request") ||
		strings.Contains(lower, "malformed") ||
		strings.Contains(lower, "missing required") ||
		strings.Contains(lower, "invalid json") ||
		strings.Contains(lower, "please use a valid") ||
		strings.Contains(lower, "not supported") ||
		strings.Contains(lower, "must be non-empty") ||
		strings.Contains(lower, "cannot be empty")
}

// isQuotaError checks if message indicates quota/rate limit error
func isQuotaError(msg string) bool {
	if msg == "" {
		return false
	}
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "resource_exhausted") ||
		strings.Contains(lower, "quota") ||
		strings.Contains(lower, "rate limit") ||
		strings.Contains(lower, "too many requests")
}
