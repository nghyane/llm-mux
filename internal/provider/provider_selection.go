package provider

import (
	"strings"
	"time"

	"github.com/sony/gobreaker"
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
// It filters out providers with open circuit breakers (unavailable) and applies
// performance-based scoring to the remaining candidates.
// If all breakers are open, returns original list to allow fallback probes.
func (m *Manager) selectProviders(model string, providers []string) []string {
	if len(providers) <= 1 {
		return providers
	}

	// Filter out providers with open circuit breakers
	available := make([]string, 0, len(providers))
	for _, p := range providers {
		breaker := m.getOrCreateBreaker(p)
		if breaker.State() != gobreaker.StateOpen {
			available = append(available, p)
		}
	}

	// If all breakers are open, allow fallback to original list for probe traffic
	if len(available) == 0 {
		return providers
	}

	return m.providerStats.SortByScore(available, model)
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
