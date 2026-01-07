package registry

import (
	"time"
)

// registryState holds the immutable state snapshot for copy-on-write pattern.
// All fields are treated as immutable after creation - never modify in place.
type registryState struct {
	models               map[string]*ModelRegistration
	clientModels         map[string][]string
	clientProviders      map[string]string
	canonicalIndex       map[string][]ProviderModelMapping
	modelIDIndex         map[string][]string
	showProviderPrefixes bool
}

func newRegistryState() *registryState {
	return &registryState{
		models:          make(map[string]*ModelRegistration),
		clientModels:    make(map[string][]string),
		clientProviders: make(map[string]string),
		canonicalIndex:  make(map[string][]ProviderModelMapping),
		modelIDIndex:    make(map[string][]string),
	}
}

// clone creates a deep copy of the registry state for copy-on-write updates.
func (s *registryState) clone() *registryState {
	if s == nil {
		return newRegistryState()
	}

	newState := &registryState{
		models:               make(map[string]*ModelRegistration, len(s.models)),
		clientModels:         make(map[string][]string, len(s.clientModels)),
		clientProviders:      make(map[string]string, len(s.clientProviders)),
		canonicalIndex:       make(map[string][]ProviderModelMapping, len(s.canonicalIndex)),
		modelIDIndex:         make(map[string][]string, len(s.modelIDIndex)),
		showProviderPrefixes: s.showProviderPrefixes,
	}

	for k, v := range s.models {
		newState.models[k] = cloneModelRegistration(v)
	}

	for k, v := range s.clientModels {
		newState.clientModels[k] = append([]string(nil), v...)
	}

	for k, v := range s.clientProviders {
		newState.clientProviders[k] = v
	}

	for k, v := range s.canonicalIndex {
		newState.canonicalIndex[k] = append([]ProviderModelMapping(nil), v...)
	}

	for k, v := range s.modelIDIndex {
		newState.modelIDIndex[k] = append([]string(nil), v...)
	}

	return newState
}

func cloneModelRegistration(reg *ModelRegistration) *ModelRegistration {
	if reg == nil {
		return nil
	}

	newReg := &ModelRegistration{
		Info:        cloneModelInfo(reg.Info),
		Count:       reg.Count,
		LastUpdated: reg.LastUpdated,
	}

	if reg.QuotaExceededClients != nil {
		newReg.QuotaExceededClients = make(map[string]*time.Time, len(reg.QuotaExceededClients))
		for k, v := range reg.QuotaExceededClients {
			if v != nil {
				t := *v
				newReg.QuotaExceededClients[k] = &t
			} else {
				newReg.QuotaExceededClients[k] = nil
			}
		}
	}

	if reg.Providers != nil {
		newReg.Providers = make(map[string]int, len(reg.Providers))
		for k, v := range reg.Providers {
			newReg.Providers[k] = v
		}
	}

	if reg.SuspendedClients != nil {
		newReg.SuspendedClients = make(map[string]string, len(reg.SuspendedClients))
		for k, v := range reg.SuspendedClients {
			newReg.SuspendedClients[k] = v
		}
	}

	return newReg
}

// Read-only helper methods on state (no locking needed - state is immutable)

func (s *registryState) findModelRegistration(modelID string) *ModelRegistration {
	if mappings, ok := s.canonicalIndex[modelID]; ok && len(mappings) > 0 {
		for _, m := range mappings {
			key := m.Provider + ":" + m.ModelID
			if reg, ok := s.models[key]; ok && reg != nil && reg.Count > 0 {
				return reg
			}
		}
	}

	if reg, ok := s.models[modelID]; ok {
		return reg
	}

	if keys, ok := s.modelIDIndex[modelID]; ok && len(keys) > 0 {
		for _, key := range keys {
			if reg, ok := s.models[key]; ok && reg != nil && reg.Count > 0 {
				return reg
			}
		}
	}
	return nil
}

func (s *registryState) getModelProvidersInternal(modelID string) []string {
	var result []string

	if reg, ok := s.models[modelID]; ok && reg != nil && reg.Count > 0 {
		for provider, count := range reg.Providers {
			if count > 0 {
				result = append(result, provider)
			}
		}
	}

	if keys, ok := s.modelIDIndex[modelID]; ok && len(keys) > 0 {
		for _, key := range keys {
			if reg, ok := s.models[key]; ok && reg != nil && reg.Count > 0 {
				if idx := findColonIndex(key); idx > 0 {
					result = append(result, key[:idx])
				}
			}
		}
	}
	return result
}

func findColonIndex(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// Index modification methods (called on state copy during writes)

// addToModelIDIndex adds a provider key to the modelID index
func (s *registryState) addToModelIDIndex(modelID, providerKey string) {
	if modelID == "" || providerKey == "" {
		return
	}
	for _, k := range s.modelIDIndex[modelID] {
		if k == providerKey {
			return
		}
	}
	s.modelIDIndex[modelID] = append(s.modelIDIndex[modelID], providerKey)
}

// removeFromModelIDIndex removes a provider key from the modelID index
func (s *registryState) removeFromModelIDIndex(modelID, providerKey string) {
	if modelID == "" || providerKey == "" {
		return
	}
	keys := s.modelIDIndex[modelID]
	if len(keys) == 0 {
		return
	}
	for i, k := range keys {
		if k == providerKey {
			keys[i] = keys[len(keys)-1]
			s.modelIDIndex[modelID] = keys[:len(keys)-1]
			if len(s.modelIDIndex[modelID]) == 0 {
				delete(s.modelIDIndex, modelID)
			}
			return
		}
	}
}

// addToCanonicalIndex adds a provider-model mapping to the canonical index
func (s *registryState) addToCanonicalIndex(canonicalID, provider, modelID string, priority int) {
	if canonicalID == "" || provider == "" || modelID == "" {
		return
	}
	for _, m := range s.canonicalIndex[canonicalID] {
		if m.Provider == provider && m.ModelID == modelID {
			return
		}
	}
	s.canonicalIndex[canonicalID] = append(s.canonicalIndex[canonicalID], ProviderModelMapping{
		Provider: provider,
		ModelID:  modelID,
		Priority: priority,
	})
}

// removeFromCanonicalIndex removes a provider-model mapping from the canonical index
func (s *registryState) removeFromCanonicalIndex(canonicalID, provider, modelID string) {
	if canonicalID == "" || provider == "" {
		return
	}
	mappings := s.canonicalIndex[canonicalID]
	if len(mappings) == 0 {
		return
	}
	for i, m := range mappings {
		if m.Provider == provider && m.ModelID == modelID {
			mappings[i] = mappings[len(mappings)-1]
			s.canonicalIndex[canonicalID] = mappings[:len(mappings)-1]
			if len(s.canonicalIndex[canonicalID]) == 0 {
				delete(s.canonicalIndex, canonicalID)
			}
			return
		}
	}
}
