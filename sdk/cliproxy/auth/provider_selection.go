package auth

import (
	"strings"
	"time"
)

// normalizeProviders normalizes and deduplicates a list of provider names.
func (m *Manager) normalizeProviders(providers []string) []string {
	if len(providers) == 0 {
		return nil
	}
	result := make([]string, 0, len(providers))
	seen := make(map[string]struct{}, len(providers))
	for _, provider := range providers {
		p := strings.ToLower(strings.TrimSpace(provider))
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

// selectProviders returns providers ordered for execution.
// Input order is respected (priority-sorted from registry), with performance scoring as secondary factor.
func (m *Manager) selectProviders(model string, providers []string) []string {
	if len(providers) <= 1 {
		return providers
	}
	return m.providerStats.SortByScore(providers, model)
}

// recordProviderResult records success/failure for weighted selection.
func (m *Manager) recordProviderResult(provider, model string, success bool, latency time.Duration) {
	stats := m.providerStats
	if stats == nil {
		return
	}
	if success {
		stats.RecordSuccess(provider, model, latency)
	} else {
		stats.RecordFailure(provider, model)
	}
}
