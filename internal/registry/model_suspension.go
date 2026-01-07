package registry

import (
	"strings"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
)

func (r *ModelRegistry) SetModelQuotaExceeded(clientID, modelID string) {
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

	newState := r.snapshot().clone()
	if registration, exists := newState.models[modelID]; exists {
		now := time.Now()
		if registration.QuotaExceededClients == nil {
			registration.QuotaExceededClients = make(map[string]*time.Time)
		}
		registration.QuotaExceededClients[clientID] = &now
		log.Debugf("Marked model %s as quota exceeded for client %s", modelID, clientID)
	}
	r.state.Store(newState)
}

func (r *ModelRegistry) ClearModelQuotaExceeded(clientID, modelID string) {
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

	newState := r.snapshot().clone()
	if registration, exists := newState.models[modelID]; exists {
		delete(registration.QuotaExceededClients, clientID)
	}
	r.state.Store(newState)
}

func (r *ModelRegistry) SuspendClientModel(clientID, modelID, reason string) {
	if clientID == "" || modelID == "" {
		return
	}
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

	newState := r.snapshot().clone()
	registration, exists := newState.models[modelID]
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
	r.state.Store(newState)
	if reason != "" {
		log.Debugf("Suspended client %s for model %s: %s", clientID, modelID, reason)
	} else {
		log.Debugf("Suspended client %s for model %s", clientID, modelID)
	}
}

func (r *ModelRegistry) ResumeClientModel(clientID, modelID string) {
	if clientID == "" || modelID == "" {
		return
	}
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

	newState := r.snapshot().clone()
	registration, exists := newState.models[modelID]
	if !exists || registration == nil || registration.SuspendedClients == nil {
		return
	}
	if _, ok := registration.SuspendedClients[clientID]; !ok {
		return
	}
	delete(registration.SuspendedClients, clientID)
	registration.LastUpdated = time.Now()
	r.state.Store(newState)
	log.Debugf("Resumed client %s for model %s", clientID, modelID)
}

func (r *ModelRegistry) ClientSupportsModel(clientID, modelID string) bool {
	clientID = strings.TrimSpace(clientID)
	modelID = strings.TrimSpace(modelID)
	if clientID == "" || modelID == "" {
		return false
	}

	normalizer := NewModelIDNormalizer()
	cleanModelID := normalizer.NormalizeModelID(modelID)

	s := r.snapshot()

	models, exists := s.clientModels[clientID]
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

func (r *ModelRegistry) CleanupExpiredQuotas() {
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

	now := time.Now()
	quotaExpiredDuration := 5 * time.Minute

	newState := r.snapshot().clone()
	for modelID, registration := range newState.models {
		for clientID, quotaTime := range registration.QuotaExceededClients {
			if quotaTime != nil && now.Sub(*quotaTime) >= quotaExpiredDuration {
				delete(registration.QuotaExceededClients, clientID)
				log.Debugf("Cleaned up expired quota tracking for model %s, client %s", modelID, clientID)
			}
		}
	}
	r.state.Store(newState)
}
