package cliproxy

import (
	"context"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// registerModelsForAuth (re)binds provider models in the global registry using the core auth ID as client identifier.
func registerModelsForAuth(a *coreauth.Auth, cfg *config.Config) {
	if a == nil || a.ID == "" {
		log.Debugf("registerModelsForAuth: auth is nil or empty ID")
		return
	}
	authKind := strings.ToLower(strings.TrimSpace(a.Attributes["auth_kind"]))
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["gemini_virtual_primary"]); strings.EqualFold(v, "true") {
			GlobalModelRegistry().UnregisterClient(a.ID)
			return
		}
	}
	// Unregister legacy client ID (if present) to avoid double counting
	if a.Runtime != nil {
		if idGetter, ok := a.Runtime.(interface{ GetClientID() string }); ok {
			if rid := idGetter.GetClientID(); rid != "" && rid != a.ID {
				GlobalModelRegistry().UnregisterClient(rid)
			}
		}
	}
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	log.Debugf("registerModelsForAuth: normalized provider=%s", provider)
	compatProviderKey, compatDisplayName, compatDetected := openAICompatInfoFromAuth(a)
	if compatDetected {
		provider = "openai-compatibility"
		log.Debugf("registerModelsForAuth: detected compat provider key=%s, name=%s", compatProviderKey, compatDisplayName)
	}
	excluded := oauthExcludedModels(provider, authKind, cfg)
	var models []*ModelInfo
	switch provider {
	case "gemini":
		models = registry.GetGeminiModels()
		if entry := resolveConfigGeminiKey(a, cfg); entry != nil {
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "vertex":
		models = registry.GetGeminiVertexModels()
		if authKind == "apikey" {
			if entry := resolveConfigVertexCompatKey(a, cfg); entry != nil && len(entry.Models) > 0 {
				models = buildVertexCompatConfigModels(entry)
			}
		}
		models = applyExcludedModels(models, excluded)
	case "gemini-cli":
		models = registry.GetGeminiCLIModels()
		models = applyExcludedModels(models, excluded)
	case "aistudio":
		models = registry.GetAIStudioModels()
		models = applyExcludedModels(models, excluded)
	case "antigravity":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		models = executor.FetchAntigravityModels(ctx, a, cfg)
		cancel()
		models = applyExcludedModels(models, excluded)
	case "claude":
		models = registry.GetClaudeModels()
		if entry := resolveConfigClaudeKey(a, cfg); entry != nil {
			if len(entry.Models) > 0 {
				models = buildClaudeConfigModels(entry)
			}
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "codex":
		models = registry.GetOpenAIModels()
		if entry := resolveConfigCodexKey(a, cfg); entry != nil {
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "qwen":
		models = registry.GetQwenModels()
		models = applyExcludedModels(models, excluded)
	case "iflow":
		models = registry.GetIFlowModels()
		models = applyExcludedModels(models, excluded)
	case "cline":
		models = registry.GetClineModels()
		models = applyExcludedModels(models, excluded)
	case "kiro":
		models = registry.GetKiroModels()
		models = applyExcludedModels(models, excluded)
	case "github-copilot":
		models = registry.GetGitHubCopilotModels()
		models = applyExcludedModels(models, excluded)
	default:
		handleOpenAICompatProvider(a, compatProviderKey, compatDisplayName, compatDetected, cfg)
		return
	}
	if len(models) > 0 {
		key := provider
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		log.Debugf("registerModelsForAuth: registering %d models for client=%s, key=%s", len(models), a.ID, key)
		GlobalModelRegistry().RegisterClient(a.ID, key, models)
		return
	}

	GlobalModelRegistry().UnregisterClient(a.ID)
}

// handleOpenAICompatProvider handles OpenAI-compatible provider registration.
func handleOpenAICompatProvider(a *coreauth.Auth, compatProviderKey, compatDisplayName string, compatDetected bool, cfg *config.Config) {
	if cfg == nil {
		return
	}

	providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
	compatName := strings.TrimSpace(a.Provider)
	isCompatAuth := false
	if compatDetected {
		if compatProviderKey != "" {
			providerKey = compatProviderKey
		}
		if compatDisplayName != "" {
			compatName = compatDisplayName
		}
		isCompatAuth = true
	}
	if strings.EqualFold(providerKey, "openai-compatibility") {
		isCompatAuth = true
		if a.Attributes != nil {
			if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
				compatName = v
			}
			if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
				providerKey = strings.ToLower(v)
				isCompatAuth = true
			}
		}
		if providerKey == "openai-compatibility" && compatName != "" {
			providerKey = strings.ToLower(compatName)
		}
	} else if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["compat_name"]); v != "" {
			compatName = v
			isCompatAuth = true
		}
		if v := strings.TrimSpace(a.Attributes["provider_key"]); v != "" {
			providerKey = strings.ToLower(v)
			isCompatAuth = true
		}
	}
	for i := range cfg.OpenAICompatibility {
		compat := &cfg.OpenAICompatibility[i]
		if strings.EqualFold(compat.Name, compatName) {
			isCompatAuth = true
			ms := make([]*ModelInfo, 0, len(compat.Models))
			for j := range compat.Models {
				m := compat.Models[j]
				modelID := m.Alias
				if modelID == "" {
					modelID = m.Name
				}
				ms = append(ms, &ModelInfo{
					ID:          modelID,
					Object:      "model",
					Created:     time.Now().Unix(),
					OwnedBy:     compat.Name,
					Type:        "openai-compatibility",
					DisplayName: m.Name,
				})
			}
			if len(ms) > 0 {
				if providerKey == "" {
					providerKey = "openai-compatibility"
				}
				GlobalModelRegistry().RegisterClient(a.ID, providerKey, ms)
			} else {
				GlobalModelRegistry().UnregisterClient(a.ID)
			}
			return
		}
	}
	if isCompatAuth {
		GlobalModelRegistry().UnregisterClient(a.ID)
		return
	}
}

// resolveConfigClaudeKey finds the matching Claude configuration for the given auth.
func resolveConfigClaudeKey(auth *coreauth.Auth, cfg *config.Config) *config.ClaudeKey {
	if auth == nil || cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range cfg.ClaudeKey {
		entry := &cfg.ClaudeKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range cfg.ClaudeKey {
			entry := &cfg.ClaudeKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

// resolveConfigGeminiKey finds the matching Gemini configuration for the given auth.
func resolveConfigGeminiKey(auth *coreauth.Auth, cfg *config.Config) *config.GeminiKey {
	if auth == nil || cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range cfg.GeminiKey {
		entry := &cfg.GeminiKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

// resolveConfigVertexCompatKey finds the matching Vertex compatibility configuration for the given auth.
func resolveConfigVertexCompatKey(auth *coreauth.Auth, cfg *config.Config) *config.VertexCompatKey {
	if auth == nil || cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range cfg.VertexCompatAPIKey {
		entry := &cfg.VertexCompatAPIKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range cfg.VertexCompatAPIKey {
			entry := &cfg.VertexCompatAPIKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

// resolveConfigCodexKey finds the matching Codex configuration for the given auth.
func resolveConfigCodexKey(auth *coreauth.Auth, cfg *config.Config) *config.CodexKey {
	if auth == nil || cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range cfg.CodexKey {
		entry := &cfg.CodexKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	return nil
}

// oauthExcludedModels returns the list of models excluded for OAuth authentication.
func oauthExcludedModels(provider, authKind string, cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	providerKey := strings.ToLower(strings.TrimSpace(provider))
	if authKindKey == "apikey" {
		return nil
	}
	return cfg.OAuthExcludedModels[providerKey]
}
