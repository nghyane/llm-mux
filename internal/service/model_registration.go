package service

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/runtime/executor/providers"
	"github.com/nghyane/llm-mux/internal/wsrelay"
)

var lastRegisteredVersion sync.Map // map[authID]int64

// registerModelsForAuth (re)binds provider models in the global registry using the core auth ID as client identifier.
func registerModelsForAuth(a *provider.Auth, cfg *config.Config, wsGateway *wsrelay.Manager) {
	if a == nil || a.ID == "" {
		log.Debugf("registerModelsForAuth: auth is nil or empty ID")
		return
	}
	if lastVer, ok := lastRegisteredVersion.Load(a.ID); ok {
		if a.MaterialVersion <= lastVer.(int64) {
			log.Debugf("registerModelsForAuth: skipping %s (version %d <= %d)", a.ID, a.MaterialVersion, lastVer)
			return
		}
	}
	authKind := strings.ToLower(strings.TrimSpace(a.Attributes["auth_kind"]))
	if a.Attributes != nil {
		if v := strings.TrimSpace(a.Attributes["gemini_virtual_primary"]); strings.EqualFold(v, "true") {
			GlobalModelRegistry().UnregisterClient(a.ID)
			return
		}
	}
	// Unregister previous client ID (if present) to avoid double counting
	if a.Runtime != nil {
		if idGetter, ok := a.Runtime.(interface{ GetClientID() string }); ok {
			if rid := idGetter.GetClientID(); rid != "" && rid != a.ID {
				GlobalModelRegistry().UnregisterClient(rid)
			}
		}
	}
	providerName := strings.ToLower(strings.TrimSpace(a.Provider))
	log.Debugf("registerModelsForAuth: normalized provider=%s", providerName)
	compatProviderKey, compatDisplayName, compatDetected := openAICompatInfoFromAuth(a)
	if compatDetected {
		providerName = "openai-compatibility"
		log.Debugf("registerModelsForAuth: detected compat provider key=%s, name=%s", compatProviderKey, compatDisplayName)
	}
	excluded := oauthExcludedModels(providerName, authKind, cfg)
	var models []*ModelInfo
	switch providerName {
	case "gemini":
		// Try dynamic fetch first, fallback to static
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		models = providers.FetchGeminiModels(ctx, a, cfg)
		cancel()
		if len(models) == 0 {
			models = registry.GetGeminiModelsForProvider("gemini")
		}
		if entry := resolveProvider(a, cfg, config.ProviderTypeGemini); entry != nil {
			if authKind == "apikey" {
				excluded = entry.ExcludedModels
			}
		}
		models = applyExcludedModels(models, excluded)
	case "vertex":
		// Try dynamic fetch first (API key mode only), fallback to static
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		models = providers.FetchVertexModels(ctx, a, cfg)
		cancel()
		if len(models) == 0 {
			models = registry.GetGeminiModelsForProvider("vertex")
		}
		if authKind == "apikey" {
			if entry := resolveProvider(a, cfg, config.ProviderTypeVertexCompat); entry != nil && len(entry.Models) > 0 {
				models = buildVertexCompatConfigModels(entry)
			}
		}
		models = applyExcludedModels(models, excluded)
	case "gemini-cli":
		// Try dynamic fetch first, fallback to static
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		models = providers.FetchGeminiCLIModels(ctx, a, cfg)
		cancel()
		if len(models) == 0 {
			models = registry.GetGeminiModelsForProvider("gemini-cli")
		}
		models = applyExcludedModels(models, excluded)
	case "aistudio":
		// Try dynamic fetch via wsrelay, fallback to static
		if wsGateway != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			models = providers.FetchAIStudioModels(ctx, a, wsGateway)
			cancel()
		}
		if len(models) == 0 {
			models = registry.GetGeminiModelsForProvider("aistudio")
		}
		models = applyExcludedModels(models, excluded)
	case "antigravity":
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		models = providers.FetchAntigravityModels(ctx, a, cfg)
		cancel()
		models = applyExcludedModels(models, excluded)
	case "claude":
		models = registry.GetClaudeModels()
		if entry := resolveProvider(a, cfg, config.ProviderTypeAnthropic); entry != nil {
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
		if entry := resolveProvider(a, cfg, config.ProviderTypeOpenAI); entry != nil {
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
		key := providerName
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		models = applyProviderPriority(models, key, cfg)
		log.Debugf("registerModelsForAuth: registering %d models for client=%s, key=%s", len(models), a.ID, key)
		GlobalModelRegistry().RegisterClient(a.ID, key, models)
		lastRegisteredVersion.Store(a.ID, a.MaterialVersion)
		return
	}

	GlobalModelRegistry().UnregisterClient(a.ID)
}

// handleOpenAICompatProvider handles OpenAI-compatible provider registration.
func handleOpenAICompatProvider(a *provider.Auth, compatProviderKey, compatDisplayName string, compatDetected bool, cfg *config.Config) {
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
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Type == config.ProviderTypeOpenAI && strings.EqualFold(p.Name, compatName) {
			isCompatAuth = true
			ms := make([]*ModelInfo, 0, len(p.Models))
			for j := range p.Models {
				m := p.Models[j]
				modelID := m.Alias
				if modelID == "" {
					modelID = m.Name
				}
				ms = append(ms, &ModelInfo{
					ID:          modelID,
					Object:      "model",
					Created:     time.Now().Unix(),
					OwnedBy:     p.Name,
					Type:        "openai-compatibility",
					DisplayName: m.Name,
				})
			}
			if len(ms) > 0 {
				if providerKey == "" {
					providerKey = "openai-compatibility"
				}
				ms = applyProviderPriority(ms, providerKey, cfg)
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

func resolveProvider(auth *provider.Auth, cfg *config.Config, providerType config.ProviderType) *config.Provider {
	if auth == nil || cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range cfg.Providers {
		p := &cfg.Providers[i]
		if p.Type != providerType {
			continue
		}
		// Match by API key
		for _, k := range p.GetAPIKeys() {
			if strings.TrimSpace(k.Key) == attrKey {
				if attrBase == "" || strings.TrimSpace(p.BaseURL) == attrBase {
					return p
				}
			}
		}
	}
	return nil
}

// oauthExcludedModels returns the list of models excluded for OAuth authentication.
func oauthExcludedModels(providerName, authKind string, cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	authKindKey := strings.ToLower(strings.TrimSpace(authKind))
	providerKey := strings.ToLower(strings.TrimSpace(providerName))
	if authKindKey == "apikey" {
		return nil
	}
	return cfg.OAuthExcludedModels[providerKey]
}

func applyProviderPriority(models []*ModelInfo, providerName string, cfg *config.Config) []*ModelInfo {
	if cfg == nil || !cfg.Routing.HasProviderPriority() || len(models) == 0 {
		return models
	}
	priority, ok := cfg.Routing.ProviderPriority[providerName]
	if !ok || priority <= 0 {
		return models
	}
	for _, m := range models {
		if m.Priority == 0 {
			m.Priority = priority
		}
	}
	return models
}
