package provider

import (
	"strings"
	"sync"
	"time"
)

// QuotaGroupResolver maps model IDs to their shared quota group.
// Models within the same quota group share rate limits - when one model
// hits quota, all models in the same group are blocked.
type QuotaGroupResolver func(provider, model string) string

var (
	quotaGroupMu        sync.RWMutex
	quotaGroupResolvers = make(map[string]QuotaGroupResolver)
	// quotaGroupProviders is a fast lookup set for providers that have quota grouping.
	// This avoids mutex lock for providers without grouping (common case).
	quotaGroupProviders = make(map[string]struct{})
)

// RegisterQuotaGroupResolver registers a custom quota group resolver for a provider.
// The resolver function receives provider and model, returns the quota group name.
// Return empty string for no grouping (each model has independent quota).
func RegisterQuotaGroupResolver(provider string, resolver QuotaGroupResolver) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" || resolver == nil {
		return
	}
	quotaGroupMu.Lock()
	quotaGroupResolvers[provider] = resolver
	quotaGroupProviders[provider] = struct{}{}
	quotaGroupMu.Unlock()
}

// HasQuotaGrouping returns true if the provider has quota grouping enabled.
// This is a fast check that avoids resolver lookup for providers without grouping.
func HasQuotaGrouping(provider string) bool {
	provider = strings.ToLower(provider)
	quotaGroupMu.RLock()
	_, ok := quotaGroupProviders[provider]
	quotaGroupMu.RUnlock()
	return ok
}

// ResolveQuotaGroup determines the quota group for a model under a provider.
// Models in the same quota group share rate limits.
// Returns empty string if no grouping applies.
func ResolveQuotaGroup(provider, model string) string {
	if provider == "" || model == "" {
		return ""
	}

	// Fast path: check if provider has grouping before acquiring lock
	providerLower := strings.ToLower(provider)
	quotaGroupMu.RLock()
	resolver, ok := quotaGroupResolvers[providerLower]
	quotaGroupMu.RUnlock()

	if !ok || resolver == nil {
		return ""
	}

	return resolver(providerLower, model)
}

// extractModelFamily extracts the model family prefix from a model ID.
// Examples:
//   - "claude-opus-4-5-thinking" -> "claude"
//   - "claude-sonnet-4" -> "claude"
//   - "gemini-2.5-pro" -> "gemini"
//   - "gpt-4o" -> "gpt"
func extractModelFamily(model string) string {
	if model == "" {
		return ""
	}

	// Fast path: find first delimiter without allocating
	modelLower := strings.ToLower(model)
	for i, r := range modelLower {
		if r == '-' || r == '_' || r == '.' {
			if i > 0 {
				return modelLower[:i]
			}
			return ""
		}
	}
	// No delimiter found, return entire model name as family
	return modelLower
}

// AntigravityQuotaGroupResolver groups models by their family prefix.
// This is used for providers like Antigravity where quota is shared
// across model families (all Claude models share quota, all Gemini models share quota, etc.)
func AntigravityQuotaGroupResolver(provider, model string) string {
	return extractModelFamily(model)
}

// quotaGroupIndex maintains a reverse index from quota group to blocked state.
// This enables O(1) lookup instead of O(N) iteration over ModelStates.
// All operations are protected by a mutex for thread-safety.
type quotaGroupIndex struct {
	mu            sync.RWMutex
	blockedGroups map[string]*quotaGroupState
}

type quotaGroupState struct {
	NextRetryAfter time.Time
	NextRecoverAt  time.Time
	SourceModel    string
}

// AuthRuntimeData is a composite struct that holds both quota group index
// and any original provider-specific runtime data (e.g., gemini-cli credentials).
// This prevents overwriting existing Runtime data when adding quota tracking.
// Exported so that providers can unwrap their original runtime data.
type AuthRuntimeData struct {
	QuotaIndex   *quotaGroupIndex
	ProviderData any
}

// GetProviderData returns the original provider-specific runtime data.
// This method allows providers to unwrap their data without direct type assertion.
func (rd *AuthRuntimeData) GetProviderData() any {
	if rd == nil {
		return nil
	}
	return rd.ProviderData
}

// getOrCreateQuotaGroupIndex returns the quota group index from auth.Runtime,
// creating it if necessary.
//
// CRITICAL: This function MUST only be called while holding the Manager's lock (m.mu).
// It is NOT safe to call this concurrently on the same auth object without external
// synchronization. The Manager ensures this by holding m.mu during MarkResult and other
// state-modifying operations.
//
// The returned quotaGroupIndex has its own internal mutex for thread-safe read/write
// operations after initialization.
func getOrCreateQuotaGroupIndex(auth *Auth) *quotaGroupIndex {
	if auth == nil {
		return nil
	}

	// Case 1: No runtime data yet
	if auth.Runtime == nil {
		idx := &quotaGroupIndex{
			blockedGroups: make(map[string]*quotaGroupState),
		}
		auth.Runtime = &AuthRuntimeData{QuotaIndex: idx}
		return idx
	}

	// Case 2: Already using AuthRuntimeData
	if rd, ok := auth.Runtime.(*AuthRuntimeData); ok {
		if rd.QuotaIndex == nil {
			rd.QuotaIndex = &quotaGroupIndex{
				blockedGroups: make(map[string]*quotaGroupState),
			}
		}
		return rd.QuotaIndex
	}

	// Case 3: Runtime is used by provider (e.g., gemini-cli credentials)
	// Wrap it in AuthRuntimeData to preserve the original data
	idx := &quotaGroupIndex{
		blockedGroups: make(map[string]*quotaGroupState),
	}
	auth.Runtime = &AuthRuntimeData{
		QuotaIndex:   idx,
		ProviderData: auth.Runtime, // Preserve original
	}
	return idx
}

// getQuotaGroupIndex returns the quota group index from auth.Runtime if it exists.
func getQuotaGroupIndex(auth *Auth) *quotaGroupIndex {
	if auth == nil || auth.Runtime == nil {
		return nil
	}

	// Check for direct quotaGroupIndex (legacy, shouldn't happen with new code)
	if idx, ok := auth.Runtime.(*quotaGroupIndex); ok {
		return idx
	}

	// Check for wrapped AuthRuntimeData
	if rd, ok := auth.Runtime.(*AuthRuntimeData); ok {
		return rd.QuotaIndex
	}

	return nil
}

// setGroupBlocked marks a quota group as blocked.
// Thread-safe.
func (idx *quotaGroupIndex) setGroupBlocked(group, sourceModel string, nextRetry, nextRecover time.Time) {
	if idx == nil || group == "" {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.blockedGroups == nil {
		idx.blockedGroups = make(map[string]*quotaGroupState)
	}
	idx.blockedGroups[group] = &quotaGroupState{
		NextRetryAfter: nextRetry,
		NextRecoverAt:  nextRecover,
		SourceModel:    sourceModel,
	}
}

// clearGroup removes a quota group from blocked state.
// Thread-safe.
func (idx *quotaGroupIndex) clearGroup(group string) {
	if idx == nil {
		return
	}
	idx.mu.Lock()
	defer idx.mu.Unlock()

	if idx.blockedGroups == nil {
		return
	}
	delete(idx.blockedGroups, group)
}

// isGroupBlocked checks if a quota group is blocked.
// Returns (blocked, nextRetryAfter) - O(1) lookup.
// Thread-safe. Performs lazy cleanup of expired entries.
func (idx *quotaGroupIndex) isGroupBlocked(group string, now time.Time) (bool, time.Time) {
	if idx == nil || group == "" {
		return false, time.Time{}
	}

	idx.mu.RLock()
	if idx.blockedGroups == nil {
		idx.mu.RUnlock()
		return false, time.Time{}
	}
	state, ok := idx.blockedGroups[group]
	if !ok || state == nil {
		idx.mu.RUnlock()
		return false, time.Time{}
	}

	// Check if still blocked
	if state.NextRetryAfter.After(now) {
		next := state.NextRetryAfter
		if !state.NextRecoverAt.IsZero() && state.NextRecoverAt.After(now) && state.NextRecoverAt.After(next) {
			next = state.NextRecoverAt
		}
		idx.mu.RUnlock()
		return true, next
	}
	idx.mu.RUnlock()

	// Expired, clean up (upgrade to write lock)
	idx.mu.Lock()
	// Double-check after acquiring write lock
	if state, ok := idx.blockedGroups[group]; ok && state != nil && !state.NextRetryAfter.After(now) {
		delete(idx.blockedGroups, group)
	}
	idx.mu.Unlock()

	return false, time.Time{}
}

// init registers default quota group resolvers for known providers
func init() {
	// Antigravity: Claude models share quota, Gemini models share quota
	RegisterQuotaGroupResolver("antigravity", AntigravityQuotaGroupResolver)
}
