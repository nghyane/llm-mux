package service

import (
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/runtime/executor/providers"
	"github.com/nghyane/llm-mux/internal/wsrelay"
)

// ensureExecutorsForAuth ensures the appropriate executor is registered for the given auth.
// It selects executors based on the provider type and registers them with the core manager.
func ensureExecutorsForAuth(a *provider.Auth, cfg *config.Config, coreManager *provider.Manager, wsGateway *wsrelay.Manager) {
	if a == nil || coreManager == nil {
		return
	}
	// Skip disabled auth entries when (re)binding executors.
	// Disabled auths can linger during config reloads (e.g., removed OpenAI-compat entries)
	// and must not override active provider executors (such as iFlow OAuth accounts).
	if a.Disabled {
		return
	}
	if compatProviderKey, _, isCompat := openAICompatInfoFromAuth(a); isCompat {
		if compatProviderKey == "" {
			compatProviderKey = strings.ToLower(strings.TrimSpace(a.Provider))
		}
		if compatProviderKey == "" {
			compatProviderKey = "openai-compatibility"
		}
		coreManager.RegisterExecutor(providers.NewOpenAICompatExecutor(compatProviderKey, cfg))
		return
	}
	registerProviderExecutor(a, cfg, coreManager, wsGateway)
}

// registerProviderExecutor registers the appropriate executor based on provider type.
func registerProviderExecutor(a *provider.Auth, cfg *config.Config, coreManager *provider.Manager, wsGateway *wsrelay.Manager) {
	providerName := strings.ToLower(strings.TrimSpace(a.Provider))
	switch providerName {
	case "gemini":
		coreManager.RegisterExecutor(providers.NewGeminiExecutor(cfg))
	case "vertex":
		coreManager.RegisterExecutor(providers.NewVertexExecutor(cfg))
	case "gemini-cli":
		coreManager.RegisterExecutor(providers.NewGeminiCLIExecutor(cfg))
	case "aistudio":
		if wsGateway != nil {
			coreManager.RegisterExecutor(providers.NewAIStudioExecutor(cfg, a.ID, wsGateway))
		}
		return
	case "antigravity":
		coreManager.RegisterExecutor(providers.NewAntigravityExecutor(cfg))
	case "claude":
		coreManager.RegisterExecutor(providers.NewClaudeExecutor(cfg))
	case "codex":
		coreManager.RegisterExecutor(providers.NewCodexExecutor(cfg))
	case "qwen":
		coreManager.RegisterExecutor(providers.NewQwenExecutor(cfg))
	case "iflow":
		coreManager.RegisterExecutor(providers.NewIFlowExecutor(cfg))
	case "cline":
		coreManager.RegisterExecutor(providers.NewClineExecutor(cfg))
	case "kiro":
		coreManager.RegisterExecutor(providers.NewKiroExecutor(cfg))
	case "github-copilot":
		coreManager.RegisterExecutor(providers.NewCopilotExecutor(cfg))
	default:
		providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		coreManager.RegisterExecutor(providers.NewOpenAICompatExecutor(providerKey, cfg))
	}
}

// rebindExecutors refreshes provider executors so they observe the latest configuration.
func rebindExecutors(coreManager *provider.Manager, cfg *config.Config, wsGateway *wsrelay.Manager) {
	if coreManager == nil {
		return
	}
	auths := coreManager.List()
	for _, auth := range auths {
		ensureExecutorsForAuth(auth, cfg, coreManager, wsGateway)
	}
}
