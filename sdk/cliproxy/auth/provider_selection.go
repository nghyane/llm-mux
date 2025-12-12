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

// selectProviders returns providers ordered by performance score + round-robin.
// Uses weighted selection based on success rate, with atomic round-robin fallback.
func (m *Manager) selectProviders(model string, providers []string) []string {
	if len(providers) <= 1 {
		return providers
	}

	// First: sort by performance score (best first)
	sorted := m.providerStats.SortByScore(providers, model)

	// Then: apply round-robin rotation within same-score tiers
	// This ensures fair distribution when providers have similar scores
	counter := m.providerCounter.Add(1)
	offset := int(counter % uint64(len(sorted)))

	if offset == 0 {
		return sorted
	}

	// Rotate to distribute load
	rotated := make([]string, len(sorted))
	copy(rotated, sorted[offset:])
	copy(rotated[len(sorted)-offset:], sorted[:offset])
	return rotated
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

