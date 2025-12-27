package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/runtime/geminicli"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
)

func computeProviderModelsHash(models []config.ProviderModel) string {
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

func createProviderAuth(idGen *stableIDGenerator, providerName, label, key, baseURL, proxyURL string, headers map[string]string, models []config.ProviderModel, excludedModels []string, cfg *config.Config, now time.Time) *coreauth.Auth {
	idKind := fmt.Sprintf("%s:apikey", providerName)
	id, token := idGen.next(idKind, key, baseURL, proxyURL)
	attrs := map[string]string{
		"source":  fmt.Sprintf("config:%s[%s]", providerName, token),
		"api_key": key,
	}
	if baseURL != "" {
		attrs["base_url"] = baseURL
	}
	if hash := computeProviderModelsHash(models); hash != "" {
		attrs["models_hash"] = hash
	}
	addConfigHeadersToAttrs(headers, attrs)
	a := &coreauth.Auth{
		ID:         id,
		Provider:   providerName,
		Label:      label,
		Status:     coreauth.StatusActive,
		ProxyURL:   proxyURL,
		Attributes: attrs,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	applyAuthExcludedModelsMeta(a, cfg, excludedModels, "apikey")
	return a
}

// SnapshotCoreAuths converts current clients snapshot into core auth entries.
func (w *Watcher) SnapshotCoreAuths() []*coreauth.Auth {
	out := make([]*coreauth.Auth, 0, 32)
	now := time.Now()
	idGen := newStableIDGenerator()
	// Synthesize auth entries from cfg.Providers
	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()
	if cfg != nil {
		for _, provider := range cfg.Providers {
			var pName, lbl string
			switch provider.Type {
			case config.ProviderTypeGemini:
				pName = "gemini"
				lbl = "gemini-apikey"
			case config.ProviderTypeAnthropic:
				pName = "claude"
				lbl = "claude-apikey"
			case config.ProviderTypeOpenAI:
				displayName := provider.GetDisplayName()
				pName = strings.ToLower(displayName)
				lbl = displayName
			case config.ProviderTypeVertexCompat:
				pName = "vertex"
				lbl = "vertex-apikey"
			default:
				continue
			}
			for _, apiKey := range provider.GetAPIKeys() {
				key := strings.TrimSpace(apiKey.Key)
				if key == "" {
					continue
				}
				proxy := strings.TrimSpace(apiKey.ProxyURL)
				if proxy == "" {
					proxy = strings.TrimSpace(provider.ProxyURL)
				}
				auth := createProviderAuth(idGen, pName, lbl, key, strings.TrimSpace(provider.BaseURL), proxy, provider.Headers, provider.Models, provider.ExcludedModels, cfg, now)
				out = append(out, auth)
			}
		}
	}

	// Also synthesize auth entries directly from auth files (for OAuth/file-backed providers)
	entries, _ := os.ReadDir(w.authDir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		full := filepath.Join(w.authDir, name)
		data, err := os.ReadFile(full)
		if err != nil || len(data) == 0 {
			continue
		}
		var metadata map[string]any
		if err = json.Unmarshal(data, &metadata); err != nil {
			continue
		}
		t, _ := metadata["type"].(string)
		if t == "" {
			continue
		}
		provider := strings.ToLower(t)
		if provider == "gemini" {
			provider = "gemini-cli"
		}
		label := provider
		if email, _ := metadata["email"].(string); email != "" {
			label = email
		}
		// Use relative path under authDir as ID to stay consistent with the file-based token store
		id := full
		if rel, errRel := filepath.Rel(w.authDir, full); errRel == nil && rel != "" {
			id = rel
		}

		proxyURL := ""
		if p, ok := metadata["proxy_url"].(string); ok {
			proxyURL = p
		}

		a := &coreauth.Auth{
			ID:       id,
			Provider: provider,
			Label:    label,
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"source": full,
				"path":   full,
			},
			ProxyURL:  proxyURL,
			Metadata:  metadata,
			CreatedAt: now,
			UpdatedAt: now,
		}
		applyAuthExcludedModelsMeta(a, cfg, nil, "oauth")
		if provider == "gemini-cli" {
			if virtuals := synthesizeGeminiVirtualAuths(a, metadata, now); len(virtuals) > 0 {
				for _, v := range virtuals {
					applyAuthExcludedModelsMeta(v, cfg, nil, "oauth")
				}
				out = append(out, a)
				out = append(out, virtuals...)
				continue
			}
		}
		out = append(out, a)
	}
	return out
}

func synthesizeGeminiVirtualAuths(primary *coreauth.Auth, metadata map[string]any, now time.Time) []*coreauth.Auth {
	if primary == nil || metadata == nil {
		return nil
	}
	projects := splitGeminiProjectIDs(metadata)
	if len(projects) <= 1 {
		return nil
	}
	email, _ := metadata["email"].(string)
	shared := geminicli.NewSharedCredential(primary.ID, email, metadata, projects)
	primary.Disabled = true
	primary.Status = coreauth.StatusDisabled
	primary.Runtime = shared
	if primary.Attributes == nil {
		primary.Attributes = make(map[string]string)
	}
	primary.Attributes["gemini_virtual_primary"] = "true"
	primary.Attributes["virtual_children"] = strings.Join(projects, ",")
	source := primary.Attributes["source"]
	authPath := primary.Attributes["path"]
	originalProvider := primary.Provider
	if originalProvider == "" {
		originalProvider = "gemini-cli"
	}
	label := primary.Label
	if label == "" {
		label = originalProvider
	}
	virtuals := make([]*coreauth.Auth, 0, len(projects))
	for _, projectID := range projects {
		attrs := map[string]string{
			"runtime_only":           "true",
			"gemini_virtual_parent":  primary.ID,
			"gemini_virtual_project": projectID,
		}
		if source != "" {
			attrs["source"] = source
		}
		if authPath != "" {
			attrs["path"] = authPath
		}
		metadataCopy := map[string]any{
			"email":             email,
			"project_id":        projectID,
			"virtual":           true,
			"virtual_parent_id": primary.ID,
			"type":              metadata["type"],
		}
		proxy := strings.TrimSpace(primary.ProxyURL)
		if proxy != "" {
			metadataCopy["proxy_url"] = proxy
		}
		virtual := &coreauth.Auth{
			ID:         buildGeminiVirtualID(primary.ID, projectID),
			Provider:   originalProvider,
			Label:      fmt.Sprintf("%s [%s]", label, projectID),
			Status:     coreauth.StatusActive,
			Attributes: attrs,
			Metadata:   metadataCopy,
			ProxyURL:   primary.ProxyURL,
			CreatedAt:  now,
			UpdatedAt:  now,
			Runtime:    geminicli.NewVirtualCredential(projectID, shared),
		}
		virtuals = append(virtuals, virtual)
	}
	return virtuals
}

func splitGeminiProjectIDs(metadata map[string]any) []string {
	raw, _ := metadata["project_id"].(string)
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result
}

func buildGeminiVirtualID(baseID, projectID string) string {
	project := strings.TrimSpace(projectID)
	if project == "" {
		project = "project"
	}
	replacer := strings.NewReplacer("/", "_", "\\", "_", " ", "_")
	return fmt.Sprintf("%s::%s", baseID, replacer.Replace(project))
}
