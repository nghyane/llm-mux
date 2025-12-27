package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
)

func computeVertexCompatModelsHash(models []config.VertexCompatModel) string {
	if len(models) == 0 {
		return ""
	}
	data, err := json.Marshal(models)
	if err != nil || len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// computeClaudeModelsHash returns a stable hash for Claude model aliases.
func computeClaudeModelsHash(models []config.ProviderModel) string {
	if len(models) == 0 {
		return ""
	}
	data, err := json.Marshal(models)
	if err != nil || len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func computeExcludedModelsHash(excluded []string) string {
	if len(excluded) == 0 {
		return ""
	}
	normalized := make([]string, 0, len(excluded))
	for _, entry := range excluded {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			normalized = append(normalized, strings.ToLower(trimmed))
		}
	}
	if len(normalized) == 0 {
		return ""
	}
	sort.Strings(normalized)
	data, err := json.Marshal(normalized)
	if err != nil || len(data) == 0 {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type excludedModelsSummary struct {
	hash  string
	count int
}

func summarizeExcludedModels(list []string) excludedModelsSummary {
	if len(list) == 0 {
		return excludedModelsSummary{}
	}
	seen := make(map[string]struct{}, len(list))
	normalized := make([]string, 0, len(list))
	for _, entry := range list {
		if trimmed := strings.ToLower(strings.TrimSpace(entry)); trimmed != "" {
			if _, exists := seen[trimmed]; exists {
				continue
			}
			seen[trimmed] = struct{}{}
			normalized = append(normalized, trimmed)
		}
	}
	sort.Strings(normalized)
	return excludedModelsSummary{
		hash:  computeExcludedModelsHash(normalized),
		count: len(normalized),
	}
}

type ampModelMappingsSummary struct {
	hash  string
	count int
}

func summarizeAmpModelMappings(mappings []config.AmpModelMapping) ampModelMappingsSummary {
	if len(mappings) == 0 {
		return ampModelMappingsSummary{}
	}
	entries := make([]string, 0, len(mappings))
	for _, mapping := range mappings {
		from := strings.TrimSpace(mapping.From)
		to := strings.TrimSpace(mapping.To)
		if from == "" && to == "" {
			continue
		}
		entries = append(entries, from+"->"+to)
	}
	if len(entries) == 0 {
		return ampModelMappingsSummary{}
	}
	sort.Strings(entries)
	sum := sha256.Sum256([]byte(strings.Join(entries, "|")))
	return ampModelMappingsSummary{
		hash:  hex.EncodeToString(sum[:]),
		count: len(entries),
	}
}

func summarizeOAuthExcludedModels(entries map[string][]string) map[string]excludedModelsSummary {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string]excludedModelsSummary, len(entries))
	for k, v := range entries {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" {
			continue
		}
		out[key] = summarizeExcludedModels(v)
	}
	return out
}

func diffOAuthExcludedModelChanges(oldMap, newMap map[string][]string) ([]string, []string) {
	oldSummary := summarizeOAuthExcludedModels(oldMap)
	newSummary := summarizeOAuthExcludedModels(newMap)
	keys := make(map[string]struct{}, len(oldSummary)+len(newSummary))
	for k := range oldSummary {
		keys[k] = struct{}{}
	}
	for k := range newSummary {
		keys[k] = struct{}{}
	}
	changes := make([]string, 0, len(keys))
	affected := make([]string, 0, len(keys))
	for key := range keys {
		oldInfo, okOld := oldSummary[key]
		newInfo, okNew := newSummary[key]
		switch {
		case okOld && !okNew:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: removed", key))
			affected = append(affected, key)
		case !okOld && okNew:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: added (%d entries)", key, newInfo.count))
			affected = append(affected, key)
		case okOld && okNew && oldInfo.hash != newInfo.hash:
			changes = append(changes, fmt.Sprintf("oauth-excluded-models[%s]: updated (%d -> %d entries)", key, oldInfo.count, newInfo.count))
			affected = append(affected, key)
		}
	}
	sort.Strings(changes)
	sort.Strings(affected)
	return changes, affected
}

func authEqual(a, b *coreauth.Auth) bool {
	return reflect.DeepEqual(normalizeAuth(a), normalizeAuth(b))
}

func normalizeAuth(a *coreauth.Auth) *coreauth.Auth {
	if a == nil {
		return nil
	}
	clone := a.Clone()
	clone.CreatedAt = time.Time{}
	clone.UpdatedAt = time.Time{}
	clone.LastRefreshedAt = time.Time{}
	clone.NextRefreshAfter = time.Time{}
	clone.Runtime = nil
	clone.Quota.NextRecoverAt = time.Time{}
	return clone
}

func applyAuthExcludedModelsMeta(auth *coreauth.Auth, cfg *config.Config, perKey []string, authKind string) {
	if auth == nil || cfg == nil {
		return
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	seen := make(map[string]struct{})
	add := func(list []string) {
		for _, entry := range list {
			if trimmed := strings.TrimSpace(entry); trimmed != "" {
				key := strings.ToLower(trimmed)
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
			}
		}
	}
	if authKindKey == "apikey" {
		add(perKey)
	} else if cfg.OAuthExcludedModels != nil {
		providerKey := strings.ToLower(strings.TrimSpace(auth.Provider))
		add(cfg.OAuthExcludedModels[providerKey])
	}
	combined := make([]string, 0, len(seen))
	for k := range seen {
		combined = append(combined, k)
	}
	sort.Strings(combined)
	hash := computeExcludedModelsHash(combined)
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if hash != "" {
		auth.Attributes["excluded_models_hash"] = hash
	}
	if authKind != "" {
		auth.Attributes["auth_kind"] = authKind
	}
}

func diffProviders(oldList, newList []config.Provider) []string {
	changes := make([]string, 0)
	oldMap := make(map[string]config.Provider, len(oldList))
	oldLabels := make(map[string]string, len(oldList))
	for idx, entry := range oldList {
		key, label := providerKey(entry, idx)
		oldMap[key] = entry
		oldLabels[key] = label
	}
	newMap := make(map[string]config.Provider, len(newList))
	newLabels := make(map[string]string, len(newList))
	for idx, entry := range newList {
		key, label := providerKey(entry, idx)
		newMap[key] = entry
		newLabels[key] = label
	}
	keySet := make(map[string]struct{}, len(oldMap)+len(newMap))
	for key := range oldMap {
		keySet[key] = struct{}{}
	}
	for key := range newMap {
		keySet[key] = struct{}{}
	}
	orderedKeys := make([]string, 0, len(keySet))
	for key := range keySet {
		orderedKeys = append(orderedKeys, key)
	}
	sort.Strings(orderedKeys)
	for _, key := range orderedKeys {
		oldEntry, oldOk := oldMap[key]
		newEntry, newOk := newMap[key]
		label := oldLabels[key]
		if label == "" {
			label = newLabels[key]
		}
		switch {
		case !oldOk:
			changes = append(changes, fmt.Sprintf("provider added: %s (api-keys=%d, models=%d)", label, countAPIKeys(newEntry), countProviderModels(newEntry.Models)))
		case !newOk:
			changes = append(changes, fmt.Sprintf("provider removed: %s (api-keys=%d, models=%d)", label, countAPIKeys(oldEntry), countProviderModels(oldEntry.Models)))
		default:
			if detail := describeProviderUpdate(oldEntry, newEntry); detail != "" {
				changes = append(changes, fmt.Sprintf("provider updated: %s %s", label, detail))
			}
		}
	}
	return changes
}

func describeProviderUpdate(oldEntry, newEntry config.Provider) string {
	oldKeyCount := countAPIKeys(oldEntry)
	newKeyCount := countAPIKeys(newEntry)
	oldModelCount := countProviderModels(oldEntry.Models)
	newModelCount := countProviderModels(newEntry.Models)
	details := make([]string, 0, 3)
	if oldKeyCount != newKeyCount {
		details = append(details, fmt.Sprintf("api-keys %d -> %d", oldKeyCount, newKeyCount))
	}
	if oldModelCount != newModelCount {
		details = append(details, fmt.Sprintf("models %d -> %d", oldModelCount, newModelCount))
	}
	if !equalStringMap(oldEntry.Headers, newEntry.Headers) {
		details = append(details, "headers updated")
	}
	if len(details) == 0 {
		return ""
	}
	return "(" + strings.Join(details, ", ") + ")"
}

func countAPIKeys(entry config.Provider) int {
	count := 0
	for _, keyEntry := range entry.GetAPIKeys() {
		if strings.TrimSpace(keyEntry.Key) != "" {
			count++
		}
	}
	return count
}

func countProviderModels(models []config.ProviderModel) int {
	count := 0
	for _, model := range models {
		name := strings.TrimSpace(model.Name)
		alias := strings.TrimSpace(model.Alias)
		if name == "" && alias == "" {
			continue
		}
		count++
	}
	return count
}

func providerKey(entry config.Provider, index int) (string, string) {
	name := strings.TrimSpace(entry.Name)
	if name != "" {
		return "name:" + name, name
	}
	base := strings.TrimSpace(entry.BaseURL)
	if base != "" {
		return "base:" + base, base
	}
	for _, model := range entry.Models {
		alias := strings.TrimSpace(model.Alias)
		if alias == "" {
			alias = strings.TrimSpace(model.Name)
		}
		if alias != "" {
			return "alias:" + alias, alias
		}
	}
	return fmt.Sprintf("index:%d", index), fmt.Sprintf("entry-%d", index+1)
}

func equalStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func trimStrings(in []string) []string {
	out := make([]string, len(in))
	for i := range in {
		out[i] = strings.TrimSpace(in[i])
	}
	return out
}

func addConfigHeadersToAttrs(headers map[string]string, attrs map[string]string) {
	if len(headers) == 0 || attrs == nil {
		return
	}
	for hk, hv := range headers {
		key := strings.TrimSpace(hk)
		val := strings.TrimSpace(hv)
		if key == "" || val == "" {
			continue
		}
		attrs["header:"+key] = val
	}
}
