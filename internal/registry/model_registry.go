// Package registry provides centralized model management for all AI service providers.
// It implements a dynamic model registry with reference counting to track active clients
// and automatically hide models when no clients are available or when quota is exceeded.
package registry

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
	misc "github.com/nghyane/llm-mux/internal/misc"
)

// ModelInfo represents information about an available model
type ModelInfo struct {
	ID                         string   `json:"id"`
	Object                     string   `json:"object"`
	Created                    int64    `json:"created"`
	OwnedBy                    string   `json:"owned_by"`
	Type                       string   `json:"type"`
	CanonicalID                string   `json:"canonical_id,omitempty"`
	DisplayName                string   `json:"display_name,omitempty"`
	Name                       string   `json:"name,omitempty"`
	Version                    string   `json:"version,omitempty"`
	Description                string   `json:"description,omitempty"`
	InputTokenLimit            int      `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int      `json:"outputTokenLimit,omitempty"`
	SupportedGenerationMethods []string `json:"supportedGenerationMethods,omitempty"`
	ContextLength              int      `json:"context_length,omitempty"`
	MaxCompletionTokens        int      `json:"max_completion_tokens,omitempty"`
	SupportedParameters        []string `json:"supported_parameters,omitempty"`

	// Thinking holds provider-specific reasoning/thinking budget capabilities.
	Thinking *ThinkingSupport `json:"thinking,omitempty"`

	// Priority controls routing order (lower = higher priority, 0 treated as 1).
	Priority int `json:"priority,omitempty"`

	// UpstreamName is the actual model name used when sending requests to the provider.
	// If set, requests for this model ID will use UpstreamName in the upstream request.
	UpstreamName string `json:"-"`

	// Hidden marks the model as excluded from model listings.
	Hidden bool `json:"-"`
}

// ThinkingSupport describes a model's supported internal reasoning budget range.
type ThinkingSupport struct {
	Min            int  `json:"min,omitempty"`
	Max            int  `json:"max,omitempty"`
	ZeroAllowed    bool `json:"zero_allowed,omitempty"`
	DynamicAllowed bool `json:"dynamic_allowed,omitempty"`
}

// ModelRegistration tracks a model's availability
type ModelRegistration struct {
	Info                 *ModelInfo
	Count                int
	LastUpdated          time.Time
	QuotaExceededClients map[string]*time.Time
	Providers            map[string]int
	SuspendedClients     map[string]string
}

// ModelRegistry manages the global registry of available models
// ProviderModelMapping holds provider and its specific model ID
type ProviderModelMapping struct {
	Provider string
	ModelID  string
	Priority int // 0/1 = primary, 2+ = fallback
}

type ModelRegistry struct {
	// models maps provider:modelID to registration information
	models map[string]*ModelRegistration
	// clientModels maps client ID to the models it provides
	clientModels map[string][]string
	// clientProviders maps client ID to its provider identifier
	clientProviders map[string]string
	// canonicalIndex maps canonical ID to provider-specific model IDs
	canonicalIndex map[string][]ProviderModelMapping
	// modelIDIndex maps bare modelID to list of provider keys (provider:modelID)
	// for O(1) lookup instead of O(n) iteration
	modelIDIndex map[string][]string
	// mutex ensures thread-safe access to the registry
	mutex *sync.RWMutex
	// showProviderPrefixes controls whether to add visual provider prefixes to model IDs
	showProviderPrefixes bool
}

var getGlobalRegistry = sync.OnceValue(func() *ModelRegistry {
	return &ModelRegistry{
		models:               make(map[string]*ModelRegistration),
		clientModels:         make(map[string][]string),
		clientProviders:      make(map[string]string),
		canonicalIndex:       make(map[string][]ProviderModelMapping),
		modelIDIndex:         make(map[string][]string),
		mutex:                &sync.RWMutex{},
		showProviderPrefixes: false,
	}
})

func GetGlobalRegistry() *ModelRegistry {
	return getGlobalRegistry()
}

// SetShowProviderPrefixes configures whether to display provider prefixes in model IDs.
// When enabled, model IDs will include visual prefixes like "[Gemini CLI] gemini-2.5-pro".
// This is purely cosmetic and does not affect model routing.
func (r *ModelRegistry) SetShowProviderPrefixes(enabled bool) {
	if r == nil {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.showProviderPrefixes = enabled
}

// RegisterClient registers a client and its supported models
// Parameters:
//   - clientID: Unique identifier for the client
//   - clientProvider: Provider name (e.g., "gemini", "claude", "openai")
//   - models: List of models that this client can provide
func (r *ModelRegistry) RegisterClient(clientID, clientProvider string, models []*ModelInfo) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	provider := strings.ToLower(clientProvider)
	uniqueModelIDs := make([]string, 0, len(models))
	rawModelIDs := make([]string, 0, len(models))
	newModels := make(map[string]*ModelInfo, len(models))
	newCounts := make(map[string]int, len(models))
	for _, model := range models {
		if model == nil || model.ID == "" {
			continue
		}
		rawModelIDs = append(rawModelIDs, model.ID)
		newCounts[model.ID]++
		if _, exists := newModels[model.ID]; exists {
			continue
		}
		newModels[model.ID] = model
		uniqueModelIDs = append(uniqueModelIDs, model.ID)
	}

	if len(uniqueModelIDs) == 0 {
		// No models supplied; unregister existing client state if present.
		r.unregisterClientInternal(clientID)
		delete(r.clientModels, clientID)
		delete(r.clientProviders, clientID)
		misc.LogCredentialSeparator()
		return
	}

	now := time.Now()

	oldModels, hadExisting := r.clientModels[clientID]
	oldProvider := r.clientProviders[clientID]
	providerChanged := oldProvider != provider
	if !hadExisting {
		// Pure addition path.
		for _, modelID := range rawModelIDs {
			model := newModels[modelID]
			r.addModelRegistration(modelID, provider, model, now)
		}
		r.clientModels[clientID] = append([]string(nil), rawModelIDs...)
		if provider != "" {
			r.clientProviders[clientID] = provider
		} else {
			delete(r.clientProviders, clientID)
		}
		log.Debugf("Registered client %s from provider %s with %d models", clientID, clientProvider, len(rawModelIDs))
		misc.LogCredentialSeparator()
		return
	}

	oldCounts := make(map[string]int, len(oldModels))
	for _, id := range oldModels {
		oldCounts[id]++
	}

	added := make([]string, 0)
	for _, id := range uniqueModelIDs {
		if oldCounts[id] == 0 {
			added = append(added, id)
		}
	}

	removed := make([]string, 0)
	for id := range oldCounts {
		if newCounts[id] == 0 {
			removed = append(removed, id)
		}
	}

	// Handle provider change for overlapping models before modifications.
	if providerChanged && oldProvider != "" {
		for id, newCount := range newCounts {
			if newCount == 0 {
				continue
			}
			oldCount := oldCounts[id]
			if oldCount == 0 {
				continue
			}
			toRemove := newCount
			if oldCount < toRemove {
				toRemove = oldCount
			}
			if reg, ok := r.models[id]; ok && reg.Providers != nil {
				if count, okProv := reg.Providers[oldProvider]; okProv {
					if count <= toRemove {
						delete(reg.Providers, oldProvider)
					} else {
						reg.Providers[oldProvider] = count - toRemove
					}
				}
			}
		}
	}

	// Apply removals first to keep counters accurate.
	for _, id := range removed {
		oldCount := oldCounts[id]
		for range oldCount {
			r.removeModelRegistration(clientID, id, oldProvider, now)
		}
	}

	for id, oldCount := range oldCounts {
		newCount := newCounts[id]
		if newCount == 0 || oldCount <= newCount {
			continue
		}
		overage := oldCount - newCount
		for range overage {
			r.removeModelRegistration(clientID, id, oldProvider, now)
		}
	}

	// Apply additions.
	for id, newCount := range newCounts {
		oldCount := oldCounts[id]
		if newCount <= oldCount {
			continue
		}
		model := newModels[id]
		diff := newCount - oldCount
		for range diff {
			r.addModelRegistration(id, provider, model, now)
		}
	}

	// Update metadata for models that remain associated with the client.
	addedSet := make(map[string]struct{}, len(added))
	for _, id := range added {
		addedSet[id] = struct{}{}
	}
	for _, id := range uniqueModelIDs {
		model := newModels[id]
		if reg, ok := r.models[id]; ok {
			reg.Info = cloneModelInfo(model)
			reg.LastUpdated = now
			if reg.QuotaExceededClients != nil {
				delete(reg.QuotaExceededClients, clientID)
			}
			if reg.SuspendedClients != nil {
				delete(reg.SuspendedClients, clientID)
			}
			if providerChanged && provider != "" {
				if _, newlyAdded := addedSet[id]; newlyAdded {
					continue
				}
				overlapCount := newCounts[id]
				if oldCount := oldCounts[id]; oldCount < overlapCount {
					overlapCount = oldCount
				}
				if overlapCount <= 0 {
					continue
				}
				if reg.Providers == nil {
					reg.Providers = make(map[string]int)
				}
				reg.Providers[provider] += overlapCount
			}
		}
	}

	// Update client bookkeeping.
	if len(rawModelIDs) > 0 {
		r.clientModels[clientID] = append([]string(nil), rawModelIDs...)
	}
	if provider != "" {
		r.clientProviders[clientID] = provider
	} else {
		delete(r.clientProviders, clientID)
	}

	if len(added) == 0 && len(removed) == 0 && !providerChanged {
		// Only metadata (e.g., display name) changed; skip separator when no log output.
		return
	}

	log.Debugf("Reconciled client %s (provider %s) models: +%d, -%d", clientID, provider, len(added), len(removed))
	misc.LogCredentialSeparator()
}

func (r *ModelRegistry) addModelRegistration(modelID, provider string, model *ModelInfo, now time.Time) {
	if model == nil || modelID == "" {
		return
	}

	// Create provider-specific model key to avoid conflicts
	// Each provider gets its own version of the model
	providerModelKey := modelID
	if provider != "" {
		providerModelKey = provider + ":" + modelID
	}

	if existing, exists := r.models[providerModelKey]; exists {
		existing.Count++
		existing.LastUpdated = now
		existing.Info = cloneModelInfo(model)
		if existing.SuspendedClients == nil {
			existing.SuspendedClients = make(map[string]string)
		}
		if provider != "" {
			if existing.Providers == nil {
				existing.Providers = make(map[string]int)
			}
			existing.Providers[provider]++
		}
		log.Debugf("Incremented count for model %s, now %d clients", providerModelKey, existing.Count)
		return
	}

	registration := &ModelRegistration{
		Info:                 cloneModelInfo(model),
		Count:                1,
		LastUpdated:          now,
		QuotaExceededClients: make(map[string]*time.Time),
		SuspendedClients:     make(map[string]string),
	}
	if provider != "" {
		registration.Providers = map[string]int{provider: 1}
	}
	r.models[providerModelKey] = registration

	// Update modelIDIndex for O(1) lookup (only for provider-prefixed keys)
	if provider != "" {
		r.addToModelIDIndex(modelID, providerModelKey)
	}

	// Update canonical index for cross-provider routing
	canonicalID := model.CanonicalID
	if canonicalID == "" {
		canonicalID = modelID // Use modelID as canonical if not specified
	}
	priority := model.Priority
	if priority == 0 {
		priority = 1 // Default to highest priority
	}
	r.addToCanonicalIndex(canonicalID, provider, modelID, priority)

	log.Debugf("Registered new model %s from provider %s (canonical: %s)", providerModelKey, provider, canonicalID)
}

func (r *ModelRegistry) removeModelRegistration(clientID, modelID, provider string, now time.Time) {
	// Create provider-specific model key to match addModelRegistration
	providerModelKey := modelID
	if provider != "" {
		providerModelKey = provider + ":" + modelID
	}

	registration, exists := r.models[providerModelKey]
	if !exists {
		return
	}
	registration.Count--
	registration.LastUpdated = now
	if registration.QuotaExceededClients != nil {
		delete(registration.QuotaExceededClients, clientID)
	}
	if registration.SuspendedClients != nil {
		delete(registration.SuspendedClients, clientID)
	}
	if registration.Count < 0 {
		registration.Count = 0
	}
	if provider != "" && registration.Providers != nil {
		if count, ok := registration.Providers[provider]; ok {
			if count <= 1 {
				delete(registration.Providers, provider)
			} else {
				registration.Providers[provider] = count - 1
			}
		}
	}
	log.Debugf("Decremented count for model %s, now %d clients", providerModelKey, registration.Count)
	if registration.Count <= 0 {
		// Clean up canonical index before removing the model
		if registration.Info != nil {
			canonicalID := registration.Info.CanonicalID
			if canonicalID == "" {
				canonicalID = modelID
			}
			r.removeFromCanonicalIndex(canonicalID, provider, modelID)
		}
		// Clean up modelIDIndex
		if provider != "" {
			r.removeFromModelIDIndex(modelID, providerModelKey)
		}
		delete(r.models, providerModelKey)
		log.Debugf("Removed model %s as no clients remain", providerModelKey)
	}
}

// addToModelIDIndex adds a provider key to the modelID index
func (r *ModelRegistry) addToModelIDIndex(modelID, providerKey string) {
	if modelID == "" || providerKey == "" {
		return
	}
	for _, k := range r.modelIDIndex[modelID] {
		if k == providerKey {
			return
		}
	}
	r.modelIDIndex[modelID] = append(r.modelIDIndex[modelID], providerKey)
}

// removeFromModelIDIndex removes a provider key from the modelID index
func (r *ModelRegistry) removeFromModelIDIndex(modelID, providerKey string) {
	if modelID == "" || providerKey == "" {
		return
	}
	keys := r.modelIDIndex[modelID]
	if len(keys) == 0 {
		return
	}
	for i, k := range keys {
		if k == providerKey {
			keys[i] = keys[len(keys)-1]
			r.modelIDIndex[modelID] = keys[:len(keys)-1]
			if len(r.modelIDIndex[modelID]) == 0 {
				delete(r.modelIDIndex, modelID)
			}
			return
		}
	}
}

// addToCanonicalIndex adds a provider-model mapping to the canonical index
func (r *ModelRegistry) addToCanonicalIndex(canonicalID, provider, modelID string, priority int) {
	if canonicalID == "" || provider == "" || modelID == "" {
		return
	}
	for _, m := range r.canonicalIndex[canonicalID] {
		if m.Provider == provider && m.ModelID == modelID {
			return
		}
	}
	r.canonicalIndex[canonicalID] = append(r.canonicalIndex[canonicalID], ProviderModelMapping{
		Provider: provider,
		ModelID:  modelID,
		Priority: priority,
	})
}

// removeFromCanonicalIndex removes a provider-model mapping from the canonical index
func (r *ModelRegistry) removeFromCanonicalIndex(canonicalID, provider, modelID string) {
	if canonicalID == "" || provider == "" {
		return
	}
	mappings := r.canonicalIndex[canonicalID]
	if len(mappings) == 0 {
		return
	}
	// Find and remove the mapping
	for i, m := range mappings {
		if m.Provider == provider && m.ModelID == modelID {
			// Remove by swapping with last element and truncating
			mappings[i] = mappings[len(mappings)-1]
			r.canonicalIndex[canonicalID] = mappings[:len(mappings)-1]
			// Clean up empty canonical entries
			if len(r.canonicalIndex[canonicalID]) == 0 {
				delete(r.canonicalIndex, canonicalID)
			}
			return
		}
	}
}

// GetProvidersWithModelID returns all providers and their provider-specific model IDs.
// For canonical models, returns the translated model ID per provider.
// For non-canonical models, returns the original model ID.
func (r *ModelRegistry) GetProvidersWithModelID(modelID string) []ProviderModelMapping {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	// Check canonical index (returns provider-specific model IDs)
	if mappings, ok := r.canonicalIndex[modelID]; ok && len(mappings) > 0 {
		result := make([]ProviderModelMapping, 0, len(mappings))
		for _, m := range mappings {
			key := m.Provider + ":" + m.ModelID
			if reg, ok := r.models[key]; ok && reg != nil && reg.Count > 0 {
				result = append(result, m)
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Fallback: use GetModelProviders and return same modelID for all
	providers := r.getModelProvidersInternal(modelID)
	if len(providers) == 0 {
		return nil
	}
	result := make([]ProviderModelMapping, len(providers))
	for i, p := range providers {
		result[i] = ProviderModelMapping{Provider: p, ModelID: modelID}
	}
	return result
}

// findModelRegistration finds a model registration using canonical index or direct lookup.
// Must be called with mutex held.
func (r *ModelRegistry) findModelRegistration(modelID string) *ModelRegistration {
	// Check canonical index first - get first available provider's registration
	if mappings, ok := r.canonicalIndex[modelID]; ok && len(mappings) > 0 {
		for _, m := range mappings {
			key := m.Provider + ":" + m.ModelID
			if reg, ok := r.models[key]; ok && reg != nil && reg.Count > 0 {
				return reg
			}
		}
	}

	// Direct lookup (non-prefixed key)
	if reg, ok := r.models[modelID]; ok {
		return reg
	}

	// O(1) lookup via modelIDIndex (instead of O(n) loop)
	if keys, ok := r.modelIDIndex[modelID]; ok && len(keys) > 0 {
		for _, key := range keys {
			if reg, ok := r.models[key]; ok && reg != nil && reg.Count > 0 {
				return reg
			}
		}
	}
	return nil
}

// getModelProvidersInternal is the lock-free internal version for use within locked contexts
func (r *ModelRegistry) getModelProvidersInternal(modelID string) []string {
	var result []string

	// Direct lookup (non-prefixed key)
	if reg, ok := r.models[modelID]; ok && reg != nil && reg.Count > 0 {
		for provider, count := range reg.Providers {
			if count > 0 {
				result = append(result, provider)
			}
		}
	}

	// O(1) lookup via modelIDIndex (instead of O(n) loop)
	if keys, ok := r.modelIDIndex[modelID]; ok && len(keys) > 0 {
		for _, key := range keys {
			if reg, ok := r.models[key]; ok && reg != nil && reg.Count > 0 {
				// Extract provider from key (format: "provider:modelID")
				if idx := strings.Index(key, ":"); idx > 0 {
					result = append(result, key[:idx])
				}
			}
		}
	}
	return result
}

// GetModelIDForProvider translates a canonical model ID to provider-specific ID.
// Returns the original modelID if no translation is found.
func (r *ModelRegistry) GetModelIDForProvider(modelID, provider string) string {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	// Check canonical index for translation
	if mappings, ok := r.canonicalIndex[modelID]; ok {
		for _, m := range mappings {
			if m.Provider == provider {
				return m.ModelID
			}
		}
	}
	return modelID
}

func cloneModelInfo(model *ModelInfo) *ModelInfo {
	if model == nil {
		return nil
	}
	copyModel := *model
	if len(model.SupportedGenerationMethods) > 0 {
		copyModel.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
	}
	if len(model.SupportedParameters) > 0 {
		copyModel.SupportedParameters = append([]string(nil), model.SupportedParameters...)
	}
	return &copyModel
}

// UnregisterClient removes a client and decrements counts for its models
// Parameters:
//   - clientID: Unique identifier for the client to remove
func (r *ModelRegistry) UnregisterClient(clientID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.unregisterClientInternal(clientID)
}

// unregisterClientInternal performs the actual client unregistration (internal, no locking)
func (r *ModelRegistry) unregisterClientInternal(clientID string) {
	models, exists := r.clientModels[clientID]
	provider, hasProvider := r.clientProviders[clientID]
	if !exists {
		if hasProvider {
			delete(r.clientProviders, clientID)
		}
		return
	}

	now := time.Now()
	for _, modelID := range models {
		// Construct provider-prefixed key to match addModelRegistration
		providerModelKey := modelID
		if hasProvider && provider != "" {
			providerModelKey = provider + ":" + modelID
		}

		if registration, isExists := r.models[providerModelKey]; isExists {
			registration.Count--
			registration.LastUpdated = now

			// Remove quota tracking for this client
			delete(registration.QuotaExceededClients, clientID)
			if registration.SuspendedClients != nil {
				delete(registration.SuspendedClients, clientID)
			}

			if hasProvider && registration.Providers != nil {
				if count, ok := registration.Providers[provider]; ok {
					if count <= 1 {
						delete(registration.Providers, provider)
					} else {
						registration.Providers[provider] = count - 1
					}
				}
			}

			log.Debugf("Decremented count for model %s, now %d clients", providerModelKey, registration.Count)

			// Remove model if no clients remain
			if registration.Count <= 0 {
				// Clean up canonical index before removing the model
				if registration.Info != nil {
					canonicalID := registration.Info.CanonicalID
					if canonicalID == "" {
						canonicalID = registration.Info.ID
					}
					r.removeFromCanonicalIndex(canonicalID, provider, registration.Info.ID)
				}
				// Clean up modelIDIndex
				if hasProvider && provider != "" {
					r.removeFromModelIDIndex(modelID, providerModelKey)
				}
				delete(r.models, providerModelKey)
				log.Debugf("Removed model %s as no clients remain", providerModelKey)
			}
		}
	}

	delete(r.clientModels, clientID)
	if hasProvider {
		delete(r.clientProviders, clientID)
	}
	log.Debugf("Unregistered client %s", clientID)
	// Separator line after completing client unregistration (after the summary line)
	misc.LogCredentialSeparator()
}

// SetModelQuotaExceeded marks a model as quota exceeded for a specific client
// Parameters:
//   - clientID: The client that exceeded quota
//   - modelID: The model that exceeded quota
func (r *ModelRegistry) SetModelQuotaExceeded(clientID, modelID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if registration, exists := r.models[modelID]; exists {
		now := time.Now()
		registration.QuotaExceededClients[clientID] = &now
		log.Debugf("Marked model %s as quota exceeded for client %s", modelID, clientID)
	}
}

// ClearModelQuotaExceeded removes quota exceeded status for a model and client
// Parameters:
//   - clientID: The client to clear quota status for
//   - modelID: The model to clear quota status for
func (r *ModelRegistry) ClearModelQuotaExceeded(clientID, modelID string) {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	if registration, exists := r.models[modelID]; exists {
		delete(registration.QuotaExceededClients, clientID)
	}
}

// SuspendClientModel marks a client's model as temporarily unavailable until explicitly resumed.
// Parameters:
//   - clientID: The client to suspend
//   - modelID: The model affected by the suspension
//   - reason: Optional description for observability
func (r *ModelRegistry) SuspendClientModel(clientID, modelID, reason string) {
	if clientID == "" || modelID == "" {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	registration, exists := r.models[modelID]
	if !exists || registration == nil {
		return
	}
	if registration.SuspendedClients == nil {
		registration.SuspendedClients = make(map[string]string)
	}
	if _, already := registration.SuspendedClients[clientID]; already {
		return
	}
	registration.SuspendedClients[clientID] = reason
	registration.LastUpdated = time.Now()
	if reason != "" {
		log.Debugf("Suspended client %s for model %s: %s", clientID, modelID, reason)
	} else {
		log.Debugf("Suspended client %s for model %s", clientID, modelID)
	}
}

// ResumeClientModel clears a previous suspension so the client counts toward availability again.
// Parameters:
//   - clientID: The client to resume
//   - modelID: The model being resumed
func (r *ModelRegistry) ResumeClientModel(clientID, modelID string) {
	if clientID == "" || modelID == "" {
		return
	}
	r.mutex.Lock()
	defer r.mutex.Unlock()

	registration, exists := r.models[modelID]
	if !exists || registration == nil || registration.SuspendedClients == nil {
		return
	}
	if _, ok := registration.SuspendedClients[clientID]; !ok {
		return
	}
	delete(registration.SuspendedClients, clientID)
	registration.LastUpdated = time.Now()
	log.Debugf("Resumed client %s for model %s", clientID, modelID)
}

// ClientSupportsModel reports whether the client registered support for modelID.
// It handles model IDs with provider prefixes (e.g., "[Gemini CLI] gemini-2.5-flash").
func (r *ModelRegistry) ClientSupportsModel(clientID, modelID string) bool {
	clientID = strings.TrimSpace(clientID)
	modelID = strings.TrimSpace(modelID)
	if clientID == "" || modelID == "" {
		return false
	}

	// Normalize model ID to remove any provider prefix
	normalizer := NewModelIDNormalizer()
	cleanModelID := normalizer.NormalizeModelID(modelID)

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	models, exists := r.clientModels[clientID]
	if !exists || len(models) == 0 {
		return false
	}

	for _, id := range models {
		if strings.EqualFold(strings.TrimSpace(id), cleanModelID) {
			return true
		}
	}

	return false
}

// GetAvailableModels returns all models that have at least one available client
// Parameters:
//   - handlerType: The handler type to filter models for (e.g., "openai", "claude", "gemini")
//
// Returns:
//   - []map[string]any: List of available models in the requested format
func (r *ModelRegistry) GetAvailableModels(handlerType string) []map[string]any {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	quotaExpiredDuration := 5 * time.Minute
	now := time.Now()

	// Phase 1: Aggregate models by ID, tracking best registration (most available clients)
	type modelAggregate struct {
		info             *ModelInfo
		effectiveClients int
		providers        map[string]int // provider -> client count
		isAvailable      bool
	}
	aggregated := make(map[string]*modelAggregate)

	for _, registration := range r.models {
		if registration.Info == nil || registration.Info.ID == "" {
			continue
		}
		modelID := registration.Info.ID

		// Calculate effective clients
		availableClients := registration.Count
		expiredClients := 0
		for _, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) < quotaExpiredDuration {
				expiredClients++
			}
		}

		cooldownSuspended := 0
		otherSuspended := 0
		if registration.SuspendedClients != nil {
			for _, reason := range registration.SuspendedClients {
				if strings.EqualFold(reason, "quota") {
					cooldownSuspended++
				} else {
					otherSuspended++
				}
			}
		}

		effectiveClients := availableClients - expiredClients - otherSuspended
		if effectiveClients < 0 {
			effectiveClients = 0
		}

		isAvailable := effectiveClients > 0 || (availableClients > 0 && (expiredClients > 0 || cooldownSuspended > 0) && otherSuspended == 0)

		// Aggregate: keep the registration with most effective clients
		existing := aggregated[modelID]
		if existing == nil {
			aggregated[modelID] = &modelAggregate{
				info:             registration.Info,
				effectiveClients: effectiveClients,
				providers:        make(map[string]int),
				isAvailable:      isAvailable,
			}
			existing = aggregated[modelID]
		} else if effectiveClients > existing.effectiveClients {
			// Update to better registration
			existing.info = registration.Info
			existing.effectiveClients = effectiveClients
			existing.isAvailable = existing.isAvailable || isAvailable
		} else if isAvailable && !existing.isAvailable {
			existing.isAvailable = true
		}

		// Merge provider counts
		for provider, count := range registration.Providers {
			existing.providers[provider] += count
		}
	}

	// Phase 2: Build output list
	models := make([]map[string]any, 0, len(aggregated))

	for _, agg := range aggregated {
		if !agg.isAvailable {
			continue
		}

		if r.showProviderPrefixes && len(agg.providers) > 0 {
			// Show model for each provider with prefix
			for providerType := range agg.providers {
				modelInfoCopy := *agg.info
				modelInfoCopy.Type = providerType
				if model := r.convertModelToMap(&modelInfoCopy, handlerType); model != nil {
					models = append(models, model)
				}
			}
		} else {
			// Single entry per model (deduplicated)
			if model := r.convertModelToMap(agg.info, handlerType); model != nil {
				models = append(models, model)
			}
		}
	}

	// Sort models alphabetically by ID
	sort.Slice(models, func(i, j int) bool {
		idI, _ := models[i]["id"].(string)
		idJ, _ := models[j]["id"].(string)
		return idI < idJ
	})

	return models
}

// GetModelCount returns the number of available clients for a specific model
// Parameters:
//   - modelID: The model ID to check
//
// Returns:
//   - int: Number of available clients for the model
func (r *ModelRegistry) GetModelCount(modelID string) int {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	// Find registration using canonical index or direct lookup
	reg := r.findModelRegistration(modelID)
	if reg == nil {
		return 0
	}

	now := time.Now()
	quotaExpiredDuration := 5 * time.Minute

	expiredClients := 0
	for _, quotaTime := range reg.QuotaExceededClients {
		if quotaTime != nil && now.Sub(*quotaTime) < quotaExpiredDuration {
			expiredClients++
		}
	}
	suspendedClients := 0
	if reg.SuspendedClients != nil {
		suspendedClients = len(reg.SuspendedClients)
	}
	result := reg.Count - expiredClients - suspendedClients
	if result < 0 {
		return 0
	}
	return result
}

// GetModelProviders returns providers for the model, sorted by priority.
func (r *ModelRegistry) GetModelProviders(modelID string) []string {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	if mappings, ok := r.canonicalIndex[modelID]; ok && len(mappings) > 0 {
		type providerWithPriority struct {
			provider string
			priority int
		}
		available := make([]providerWithPriority, 0, len(mappings))
		for _, m := range mappings {
			key := m.Provider + ":" + m.ModelID
			if reg, ok := r.models[key]; ok && reg != nil && reg.Count > 0 {
				priority := m.Priority
				if priority == 0 {
					priority = 1
				}
				available = append(available, providerWithPriority{
					provider: m.Provider,
					priority: priority,
				})
			}
		}
		if len(available) > 0 {
			sort.Slice(available, func(i, j int) bool {
				return available[i].priority < available[j].priority
			})
			result := make([]string, len(available))
			for i, p := range available {
				result[i] = p.provider
			}
			return result
		}
	}

	return r.getModelProvidersInternal(modelID)
}

// GetModelInfo returns the registered ModelInfo for the given model ID, if present.
// Uses canonical index for cross-provider routing.
func (r *ModelRegistry) GetModelInfo(modelID string) *ModelInfo {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	reg := r.findModelRegistration(modelID)
	if reg != nil {
		return reg.Info
	}

	return nil
}

// GetAvailableProviders returns a list of all provider types that currently have
// at least one model available (registered with count > 0).
func (r *ModelRegistry) GetAvailableProviders() []string {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	providerSet := make(map[string]bool)
	for _, reg := range r.models {
		if reg == nil || reg.Count == 0 {
			continue
		}
		for provider, count := range reg.Providers {
			if count > 0 {
				providerSet[provider] = true
			}
		}
	}

	providers := make([]string, 0, len(providerSet))
	for p := range providerSet {
		providers = append(providers, p)
	}
	return providers
}

// formatProviderPrefix creates a visual prefix for a model based on its type.
// ModelIDNormalizer provides centralized model ID normalization and prefix handling.
type ModelIDNormalizer struct{}

// NewModelIDNormalizer creates a new model ID normalizer.
func NewModelIDNormalizer() *ModelIDNormalizer {
	return &ModelIDNormalizer{}
}

// NormalizeModelID removes provider prefix and returns clean internal model ID.
// Examples: "[Gemini CLI] gemini-2.5-flash" -> "gemini-2.5-flash"
//
//	"gemini-2.5-flash" -> "gemini-2.5-flash"
func (n *ModelIDNormalizer) NormalizeModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if strings.HasPrefix(modelID, "[") {
		if idx := strings.Index(modelID, "] "); idx != -1 {
			return strings.TrimSpace(modelID[idx+2:])
		}
	}
	return modelID
}

// ExtractProviderFromPrefixedID extracts provider type from prefixed model ID.
// Examples: "[Gemini CLI] gemini-2.5-flash" -> "gemini-cli"
//
//	"[Antigravity] model" -> "antigravity"
//	"gemini-2.5-flash" -> ""
func (n *ModelIDNormalizer) ExtractProviderFromPrefixedID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if strings.HasPrefix(modelID, "[") {
		if idx := strings.Index(modelID, "] "); idx != -1 {
			prefix := strings.TrimSpace(modelID[1:idx])
			// Map display names back to provider types
			providerMap := map[string]string{
				"Gemini CLI":  "gemini-cli",
				"Gemini":      "gemini",
				"Vertex AI":   "vertex",
				"AI Studio":   "aistudio",
				"Antigravity": "antigravity",
				"Claude":      "claude",
				"Codex":       "codex",
				"Qwen":        "qwen",
				"iFlow":       "iflow",
				"Cline":       "cline",
				"Kiro":        "kiro",
				"OpenAI":      "openai",
				"Anthropic":   "anthropic",
				"Google":      "google",
			}
			if provider, exists := providerMap[prefix]; exists {
				return provider
			}
		}
	}
	return ""
}

// Returns empty string if prefixes are disabled or type is empty.
func (r *ModelRegistry) formatProviderPrefix(modelType string) string {
	if !r.showProviderPrefixes || modelType == "" {
		return ""
	}

	// Map provider types to human-readable names
	// CLI versions use OAuth authentication, others use API keys
	providerNames := map[string]string{
		"gemini-cli":  "Gemini CLI",
		"gemini":      "Gemini",
		"vertex":      "Vertex AI",
		"aistudio":    "AI Studio",
		"claude":      "Claude",
		"codex":       "Codex",
		"qwen":        "Qwen",
		"iflow":       "iFlow",
		"cline":       "Cline",
		"kiro":        "Kiro",
		"antigravity": "Antigravity",
		"openai":      "OpenAI",
		"anthropic":   "Anthropic",
		"google":      "Google",
	}

	typeLower := strings.ToLower(strings.TrimSpace(modelType))
	if displayName, exists := providerNames[typeLower]; exists {
		return "[" + displayName + "] "
	}

	// Fallback: capitalize first letter of type
	if len(typeLower) > 0 {
		return "[" + strings.ToUpper(typeLower[:1]) + typeLower[1:] + "] "
	}

	return ""
}

// convertModelToMap converts ModelInfo to the appropriate format for different handler types
func (r *ModelRegistry) convertModelToMap(model *ModelInfo, handlerType string) map[string]any {
	if model == nil {
		return nil
	}

	// Generate provider prefix if enabled
	prefix := r.formatProviderPrefix(model.Type)

	switch handlerType {
	case "openai":
		result := map[string]any{
			"id":       prefix + model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created"] = model.Created
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		}
		if model.Version != "" {
			result["version"] = model.Version
		}
		if model.Description != "" {
			result["description"] = model.Description
		}
		if model.ContextLength > 0 {
			result["context_length"] = model.ContextLength
		}
		if model.MaxCompletionTokens > 0 {
			result["max_completion_tokens"] = model.MaxCompletionTokens
		}
		if len(model.SupportedParameters) > 0 {
			result["supported_parameters"] = model.SupportedParameters
		}
		return result

	case "claude":
		result := map[string]any{
			"id":       prefix + model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created"] = model.Created
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		}
		return result

	case "gemini":
		result := map[string]any{}
		// Always use ID for consistency, add "models/" prefix for Gemini API format
		name := model.ID
		if !strings.HasPrefix(name, "models/") {
			name = "models/" + name
		}
		result["name"] = prefix + name
		if model.Version != "" {
			result["version"] = model.Version
		}
		if model.DisplayName != "" {
			result["displayName"] = model.DisplayName
		}
		if model.Description != "" {
			result["description"] = model.Description
		}
		if model.InputTokenLimit > 0 {
			result["inputTokenLimit"] = model.InputTokenLimit
		}
		if model.OutputTokenLimit > 0 {
			result["outputTokenLimit"] = model.OutputTokenLimit
		}
		if len(model.SupportedGenerationMethods) > 0 {
			result["supportedGenerationMethods"] = model.SupportedGenerationMethods
		}
		return result

	default:
		// Generic format
		result := map[string]any{
			"id":     prefix + model.ID,
			"object": "model",
		}
		if model.OwnedBy != "" {
			result["owned_by"] = model.OwnedBy
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.Created != 0 {
			result["created"] = model.Created
		}
		return result
	}
}

// CleanupExpiredQuotas removes expired quota tracking entries
func (r *ModelRegistry) CleanupExpiredQuotas() {
	r.mutex.Lock()
	defer r.mutex.Unlock()

	now := time.Now()
	quotaExpiredDuration := 5 * time.Minute

	for modelID, registration := range r.models {
		for clientID, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) >= quotaExpiredDuration {
				delete(registration.QuotaExceededClients, clientID)
				log.Debugf("Cleaned up expired quota tracking for model %s, client %s", modelID, clientID)
			}
		}
	}
}

// GetFirstAvailableModel returns the first available model for the given handler type.
// It prioritizes models by their creation timestamp (newest first) and checks if they have
// available clients that are not suspended or over quota.
// Parameters:
//   - handlerType: The API handler type (e.g., "openai", "claude", "gemini")
//
// Returns:
//   - string: The model ID of the first available model, or empty string if none available
//   - error: An error if no models are available
func (r *ModelRegistry) GetFirstAvailableModel(handlerType string) (string, error) {
	r.mutex.RLock()
	defer r.mutex.RUnlock()

	// Get all available models for this handler type
	models := r.GetAvailableModels(handlerType)
	if len(models) == 0 {
		return "", fmt.Errorf("no models available for handler type: %s", handlerType)
	}

	// Sort models by creation timestamp (newest first)
	sort.Slice(models, func(i, j int) bool {
		// Extract created timestamps from map
		createdI, okI := models[i]["created"].(int64)
		createdJ, okJ := models[j]["created"].(int64)
		if !okI || !okJ {
			return false
		}
		return createdI > createdJ
	})

	// Find the first model with available clients
	for _, model := range models {
		if modelID, ok := model["id"].(string); ok {
			if count := r.GetModelCount(modelID); count > 0 {
				return modelID, nil
			}
		}
	}

	return "", fmt.Errorf("no available clients for any model in handler type: %s", handlerType)
}
