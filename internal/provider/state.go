package provider

import (
	"time"
)

// ensureModelState creates a model state if it doesn't exist.
func ensureModelState(auth *Auth, model string) *ModelState {
	if auth == nil || model == "" {
		return nil
	}
	if auth.ModelStates == nil {
		auth.ModelStates = make(map[string]*ModelState)
	}
	if state, ok := auth.ModelStates[model]; ok && state != nil {
		return state
	}
	state := &ModelState{Status: StatusActive}
	auth.ModelStates[model] = state
	return state
}

// resetModelState clears all error conditions from a model state.
func resetModelState(state *ModelState, now time.Time) {
	if state == nil {
		return
	}
	state.Unavailable = false
	state.Status = StatusActive
	state.StatusMessage = ""
	state.NextRetryAfter = time.Time{}
	state.LastError = nil
	state.Quota = QuotaState{}
	state.UpdatedAt = now
}

// updateAggregatedAvailability recomputes auth-level availability based on model states.
func updateAggregatedAvailability(auth *Auth, now time.Time) {
	if auth == nil || len(auth.ModelStates) == 0 {
		return
	}
	allUnavailable := true
	earliestRetry := time.Time{}
	quotaExceeded := false
	quotaRecover := time.Time{}
	maxBackoffLevel := 0
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		stateUnavailable := false
		if state.Status == StatusDisabled {
			stateUnavailable = true
		} else if state.Unavailable {
			if state.NextRetryAfter.IsZero() {
				stateUnavailable = true
			} else if state.NextRetryAfter.After(now) {
				stateUnavailable = true
				if earliestRetry.IsZero() || state.NextRetryAfter.Before(earliestRetry) {
					earliestRetry = state.NextRetryAfter
				}
			} else {
				state.Unavailable = false
				state.NextRetryAfter = time.Time{}
			}
		}
		if !stateUnavailable {
			allUnavailable = false
		}
		if state.Quota.Exceeded {
			quotaExceeded = true
			if quotaRecover.IsZero() || (!state.Quota.NextRecoverAt.IsZero() && state.Quota.NextRecoverAt.Before(quotaRecover)) {
				quotaRecover = state.Quota.NextRecoverAt
			}
			if state.Quota.BackoffLevel > maxBackoffLevel {
				maxBackoffLevel = state.Quota.BackoffLevel
			}
		}
	}
	auth.Unavailable = allUnavailable
	if allUnavailable {
		auth.NextRetryAfter = earliestRetry
	} else {
		auth.NextRetryAfter = time.Time{}
	}
	if quotaExceeded {
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		auth.Quota.NextRecoverAt = quotaRecover
		auth.Quota.BackoffLevel = maxBackoffLevel
	} else {
		auth.Quota.Exceeded = false
		auth.Quota.Reason = ""
		auth.Quota.NextRecoverAt = time.Time{}
		auth.Quota.BackoffLevel = 0
	}
}

// hasModelError checks if any model has an unresolved error.
func hasModelError(auth *Auth, now time.Time) bool {
	if auth == nil || len(auth.ModelStates) == 0 {
		return false
	}
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		if state.LastError != nil {
			return true
		}
		if state.Status == StatusError {
			if state.Unavailable && (state.NextRetryAfter.IsZero() || state.NextRetryAfter.After(now)) {
				return true
			}
		}
	}
	return false
}

// clearAuthStateOnSuccess resets all error and quota state after a successful request.
func clearAuthStateOnSuccess(auth *Auth, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = false
	auth.Status = StatusActive
	auth.StatusMessage = ""
	auth.Quota.Exceeded = false
	auth.Quota.Reason = ""
	auth.Quota.NextRecoverAt = time.Time{}
	auth.Quota.BackoffLevel = 0
	auth.LastError = nil
	auth.NextRetryAfter = time.Time{}
	auth.UpdatedAt = now
}

// applyAuthFailureState updates auth state based on error category.
func applyAuthFailureState(auth *Auth, resultErr *Error, retryAfter *time.Duration, now time.Time) {
	if auth == nil {
		return
	}
	auth.Unavailable = true
	auth.Status = StatusError
	auth.UpdatedAt = now
	if resultErr != nil {
		auth.LastError = cloneError(resultErr)
		if resultErr.Message != "" {
			auth.StatusMessage = resultErr.Message
		}
	}

	// Use category-based decision making
	category := CategoryUnknown
	if resultErr != nil && resultErr.ErrCategory != CategoryUnknown {
		category = resultErr.ErrCategory
	} else {
		// Fallback to status code + message classification
		statusCode := statusCodeFromResult(resultErr)
		msg := ""
		if resultErr != nil {
			msg = resultErr.Message
		}
		category = CategorizeError(statusCode, msg)
	}

	switch category {
	case CategoryAuthRevoked:
		// Permanent OAuth failure - disable auth
		auth.StatusMessage = "oauth_token_revoked"
		auth.Disabled = true
		auth.Status = StatusDisabled
	case CategoryAuthError:
		// Temporary auth error - retry later
		auth.StatusMessage = "unauthorized"
		auth.NextRetryAfter = now.Add(30 * time.Minute)
	case CategoryQuotaError:
		auth.StatusMessage = "quota exhausted"
		auth.Quota.Exceeded = true
		auth.Quota.Reason = "quota"
		var next time.Time
		if retryAfter != nil {
			next = now.Add(*retryAfter)
		} else {
			cooldown, nextLevel := nextQuotaCooldown(auth.Quota.BackoffLevel)
			if cooldown > 0 {
				next = now.Add(cooldown)
			}
			auth.Quota.BackoffLevel = nextLevel
		}
		auth.Quota.NextRecoverAt = next
		auth.NextRetryAfter = next
	case CategoryNotFound:
		auth.StatusMessage = "not_found"
		auth.NextRetryAfter = now.Add(12 * time.Hour)
	case CategoryTransient:
		auth.StatusMessage = "transient upstream error"
		auth.NextRetryAfter = now.Add(1 * time.Minute)
	case CategoryUserError:
		// User errors should not affect auth state significantly
		auth.StatusMessage = "user_request_error"
		auth.Unavailable = false // Don't mark auth unavailable for user errors
		auth.Status = StatusActive
	default:
		if auth.StatusMessage == "" {
			auth.StatusMessage = "request failed"
		}
	}
}

// cloneError creates a deep copy of an Error.
func cloneError(err *Error) *Error {
	if err == nil {
		return nil
	}
	return &Error{
		Code:       err.Code,
		Message:    err.Message,
		Retryable:  err.Retryable,
		HTTPStatus: err.HTTPStatus,
	}
}

// statusCodeFromResult extracts HTTP status from an Error.
func statusCodeFromResult(err *Error) int {
	if err == nil {
		return 0
	}
	return err.StatusCode()
}

// propagateQuotaToGroup applies quota state to all models in the same quota group.
// This is used for providers where models share rate limits (e.g., Antigravity
// where all Claude models share quota, all Gemini models share quota, etc.)
//
// When a model hits quota (429), this function propagates the quota state to all
// other models in the same group, so they are also blocked until the quota resets.
func propagateQuotaToGroup(auth *Auth, sourceModel string, quota QuotaState, nextRetry time.Time, now time.Time) []string {
	if auth == nil || sourceModel == "" {
		return nil
	}

	// Fast path: check if provider has quota grouping
	if !HasQuotaGrouping(auth.Provider) {
		return nil
	}

	quotaGroup := ResolveQuotaGroup(auth.Provider, sourceModel)
	if quotaGroup == "" {
		return nil
	}

	// Update the quota group index for O(1) lookup
	idx := getOrCreateQuotaGroupIndex(auth)
	idx.setGroupBlocked(quotaGroup, sourceModel, nextRetry, quota.NextRecoverAt)

	var affectedModels []string

	// Propagate to existing model states in the same group
	for modelID, state := range auth.ModelStates {
		if state == nil || modelID == sourceModel {
			continue
		}

		modelGroup := ResolveQuotaGroup(auth.Provider, modelID)
		if modelGroup != quotaGroup {
			continue
		}

		// Apply the same quota state to this model
		state.Unavailable = true
		state.NextRetryAfter = nextRetry
		state.Quota = QuotaState{
			Exceeded:      quota.Exceeded,
			Reason:        quota.Reason,
			NextRecoverAt: quota.NextRecoverAt,
			BackoffLevel:  quota.BackoffLevel,
		}
		state.Status = StatusError
		state.StatusMessage = "quota_group_exceeded"
		state.UpdatedAt = now
		affectedModels = append(affectedModels, modelID)
	}

	return affectedModels
}

// clearQuotaGroupOnSuccess clears quota state for all models in the same quota group.
// This is called when a model succeeds, indicating the quota has recovered.
func clearQuotaGroupOnSuccess(auth *Auth, sourceModel string, now time.Time) []string {
	if auth == nil || sourceModel == "" {
		return nil
	}

	// Fast path: check if provider has quota grouping
	if !HasQuotaGrouping(auth.Provider) {
		return nil
	}

	quotaGroup := ResolveQuotaGroup(auth.Provider, sourceModel)
	if quotaGroup == "" {
		return nil
	}

	// Clear the quota group index
	if idx := getQuotaGroupIndex(auth); idx != nil {
		idx.clearGroup(quotaGroup)
	}

	var clearedModels []string

	for modelID, state := range auth.ModelStates {
		if state == nil || modelID == sourceModel {
			continue
		}

		modelGroup := ResolveQuotaGroup(auth.Provider, modelID)
		if modelGroup != quotaGroup {
			continue
		}

		// Only clear if this model was blocked due to quota group
		if state.StatusMessage == "quota_group_exceeded" || state.Quota.Exceeded {
			resetModelState(state, now)
			clearedModels = append(clearedModels, modelID)
		}
	}

	return clearedModels
}
