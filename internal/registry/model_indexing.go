package registry

import (
	"sort"
)

func (r *ModelRegistry) GetProvidersWithModelID(modelID string) []ProviderModelMapping {
	s := r.snapshot()

	if mappings, ok := s.canonicalIndex[modelID]; ok && len(mappings) > 0 {
		result := make([]ProviderModelMapping, 0, len(mappings))
		for _, m := range mappings {
			key := m.Provider + ":" + m.ModelID
			if reg, ok := s.models[key]; ok && reg != nil && reg.Count > 0 {
				result = append(result, m)
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	providers := s.getModelProvidersInternal(modelID)
	if len(providers) == 0 {
		return nil
	}
	result := make([]ProviderModelMapping, len(providers))
	for i, p := range providers {
		result[i] = ProviderModelMapping{Provider: p, ModelID: modelID}
	}
	return result
}

func (r *ModelRegistry) GetModelIDForProvider(modelID, provider string) string {
	s := r.snapshot()

	if mappings, ok := s.canonicalIndex[modelID]; ok {
		for _, m := range mappings {
			if m.Provider == provider {
				return m.ModelID
			}
		}
	}
	return modelID
}

func (r *ModelRegistry) GetModelProviders(modelID string) []string {
	s := r.snapshot()

	if mappings, ok := s.canonicalIndex[modelID]; ok && len(mappings) > 0 {
		type providerWithPriority struct {
			provider string
			priority int
		}
		available := make([]providerWithPriority, 0, len(mappings))
		for _, m := range mappings {
			key := m.Provider + ":" + m.ModelID
			if reg, ok := s.models[key]; ok && reg != nil && reg.Count > 0 {
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

	return s.getModelProvidersInternal(modelID)
}
