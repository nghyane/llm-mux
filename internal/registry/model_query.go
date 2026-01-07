package registry

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

func (r *ModelRegistry) GetAvailableModels(handlerType string) []map[string]any {
	s := r.snapshot()
	return r.getAvailableModelsFromState(s, handlerType)
}

func (r *ModelRegistry) getAvailableModelsFromState(s *registryState, handlerType string) []map[string]any {
	quotaExpiredDuration := 5 * time.Minute
	now := time.Now()

	type modelAggregate struct {
		info             *ModelInfo
		effectiveClients int
		providers        map[string]int
		isAvailable      bool
	}
	aggregated := make(map[string]*modelAggregate)

	for _, registration := range s.models {
		if registration.Info == nil || registration.Info.ID == "" {
			continue
		}
		modelID := registration.Info.ID

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
			existing.info = registration.Info
			existing.effectiveClients = effectiveClients
			existing.isAvailable = existing.isAvailable || isAvailable
		} else if isAvailable && !existing.isAvailable {
			existing.isAvailable = true
		}

		for provider, count := range registration.Providers {
			existing.providers[provider] += count
		}
	}

	models := make([]map[string]any, 0, len(aggregated))

	for _, agg := range aggregated {
		if !agg.isAvailable {
			continue
		}

		if s.showProviderPrefixes && len(agg.providers) > 0 {
			for providerType := range agg.providers {
				modelInfoCopy := *agg.info
				modelInfoCopy.Type = providerType
				if model := r.convertModelToMapWithState(s, &modelInfoCopy, handlerType); model != nil {
					models = append(models, model)
				}
			}
		} else {
			if model := r.convertModelToMapWithState(s, agg.info, handlerType); model != nil {
				models = append(models, model)
			}
		}
	}

	sort.Slice(models, func(i, j int) bool {
		idI, _ := models[i]["id"].(string)
		idJ, _ := models[j]["id"].(string)
		return idI < idJ
	})

	return models
}

func (r *ModelRegistry) GetModelCount(modelID string) int {
	s := r.snapshot()
	return r.getModelCountFromState(s, modelID)
}

func (r *ModelRegistry) getModelCountFromState(s *registryState, modelID string) int {
	reg := s.findModelRegistration(modelID)
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

func (r *ModelRegistry) GetModelInfo(modelID string) *ModelInfo {
	s := r.snapshot()

	reg := s.findModelRegistration(modelID)
	if reg != nil {
		return reg.Info
	}

	return nil
}

func (r *ModelRegistry) GetAvailableProviders() []string {
	s := r.snapshot()

	providerSet := make(map[string]bool)
	for _, reg := range s.models {
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

func (r *ModelRegistry) GetFirstAvailableModel(handlerType string) (string, error) {
	s := r.snapshot()

	models := r.getAvailableModelsFromState(s, handlerType)
	if len(models) == 0 {
		return "", fmt.Errorf("no models available for handler type: %s", handlerType)
	}

	sort.Slice(models, func(i, j int) bool {
		createdI, okI := models[i]["created"].(int64)
		createdJ, okJ := models[j]["created"].(int64)
		if !okI || !okJ {
			return false
		}
		return createdI > createdJ
	})

	for _, model := range models {
		if modelID, ok := model["id"].(string); ok {
			if count := r.getModelCountFromState(s, modelID); count > 0 {
				return modelID, nil
			}
		}
	}

	return "", fmt.Errorf("no available clients for any model in handler type: %s", handlerType)
}
