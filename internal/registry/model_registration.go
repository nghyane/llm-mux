package registry

import (
	"strings"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
	misc "github.com/nghyane/llm-mux/internal/misc"
)

func (r *ModelRegistry) RegisterClient(clientID, clientProvider string, models []*ModelInfo) {
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

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

	newState := r.snapshot().clone()

	if len(uniqueModelIDs) == 0 {
		newState.unregisterClientInternal(clientID)
		delete(newState.clientModels, clientID)
		delete(newState.clientProviders, clientID)
		r.state.Store(newState)
		misc.LogCredentialSeparator()
		return
	}

	now := time.Now()

	oldModels, hadExisting := newState.clientModels[clientID]
	oldProvider := newState.clientProviders[clientID]
	providerChanged := oldProvider != provider
	if !hadExisting {
		for _, modelID := range rawModelIDs {
			model := newModels[modelID]
			newState.addModelRegistration(modelID, provider, model, now)
		}
		newState.clientModels[clientID] = append([]string(nil), rawModelIDs...)
		if provider != "" {
			newState.clientProviders[clientID] = provider
		} else {
			delete(newState.clientProviders, clientID)
		}
		r.state.Store(newState)
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
			if reg, ok := newState.models[id]; ok && reg.Providers != nil {
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

	for _, id := range removed {
		oldCount := oldCounts[id]
		for range oldCount {
			newState.removeModelRegistration(clientID, id, oldProvider, now)
		}
	}

	for id, oldCount := range oldCounts {
		newCount := newCounts[id]
		if newCount == 0 || oldCount <= newCount {
			continue
		}
		overage := oldCount - newCount
		for range overage {
			newState.removeModelRegistration(clientID, id, oldProvider, now)
		}
	}

	for id, newCount := range newCounts {
		oldCount := oldCounts[id]
		if newCount <= oldCount {
			continue
		}
		model := newModels[id]
		diff := newCount - oldCount
		for range diff {
			newState.addModelRegistration(id, provider, model, now)
		}
	}

	addedSet := make(map[string]struct{}, len(added))
	for _, id := range added {
		addedSet[id] = struct{}{}
	}
	for _, id := range uniqueModelIDs {
		model := newModels[id]
		if reg, ok := newState.models[id]; ok {
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

	if len(rawModelIDs) > 0 {
		newState.clientModels[clientID] = append([]string(nil), rawModelIDs...)
	}
	if provider != "" {
		newState.clientProviders[clientID] = provider
	} else {
		delete(newState.clientProviders, clientID)
	}

	r.state.Store(newState)

	if len(added) == 0 && len(removed) == 0 && !providerChanged {
		return
	}

	log.Debugf("Reconciled client %s (provider %s) models: +%d, -%d", clientID, provider, len(added), len(removed))
	misc.LogCredentialSeparator()
}

func (s *registryState) addModelRegistration(modelID, provider string, model *ModelInfo, now time.Time) {
	if model == nil || modelID == "" {
		return
	}

	providerModelKey := modelID
	if provider != "" {
		providerModelKey = provider + ":" + modelID
	}

	if existing, exists := s.models[providerModelKey]; exists {
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
	s.models[providerModelKey] = registration

	if provider != "" {
		s.addToModelIDIndex(modelID, providerModelKey)
	}

	canonicalID := model.CanonicalID
	if canonicalID == "" {
		canonicalID = modelID
	}
	priority := model.Priority
	if priority == 0 {
		priority = 1
	}
	s.addToCanonicalIndex(canonicalID, provider, modelID, priority)

	log.Debugf("Registered new model %s from provider %s (canonical: %s)", providerModelKey, provider, canonicalID)
}

func (s *registryState) removeModelRegistration(clientID, modelID, provider string, now time.Time) {
	providerModelKey := modelID
	if provider != "" {
		providerModelKey = provider + ":" + modelID
	}

	registration, exists := s.models[providerModelKey]
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
		if registration.Info != nil {
			canonicalID := registration.Info.CanonicalID
			if canonicalID == "" {
				canonicalID = modelID
			}
			s.removeFromCanonicalIndex(canonicalID, provider, modelID)
		}
		if provider != "" {
			s.removeFromModelIDIndex(modelID, providerModelKey)
		}
		delete(s.models, providerModelKey)
		log.Debugf("Removed model %s as no clients remain", providerModelKey)
	}
}

func (r *ModelRegistry) UnregisterClient(clientID string) {
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

	newState := r.snapshot().clone()
	newState.unregisterClientInternal(clientID)
	r.state.Store(newState)
}

func (s *registryState) unregisterClientInternal(clientID string) {
	models, exists := s.clientModels[clientID]
	provider, hasProvider := s.clientProviders[clientID]
	if !exists {
		if hasProvider {
			delete(s.clientProviders, clientID)
		}
		return
	}

	now := time.Now()
	for _, modelID := range models {
		providerModelKey := modelID
		if hasProvider && provider != "" {
			providerModelKey = provider + ":" + modelID
		}

		if registration, isExists := s.models[providerModelKey]; isExists {
			registration.Count--
			registration.LastUpdated = now

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

			if registration.Count <= 0 {
				if registration.Info != nil {
					canonicalID := registration.Info.CanonicalID
					if canonicalID == "" {
						canonicalID = registration.Info.ID
					}
					s.removeFromCanonicalIndex(canonicalID, provider, registration.Info.ID)
				}
				if hasProvider && provider != "" {
					s.removeFromModelIDIndex(modelID, providerModelKey)
				}
				delete(s.models, providerModelKey)
				log.Debugf("Removed model %s as no clients remain", providerModelKey)
			}
		}
	}

	delete(s.clientModels, clientID)
	if hasProvider {
		delete(s.clientProviders, clientID)
	}
	log.Debugf("Unregistered client %s", clientID)
	misc.LogCredentialSeparator()
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
