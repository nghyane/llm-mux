package cliproxy

import (
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/runtime/executor"
	"github.com/nghyane/llm-mux/internal/wsrelay"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
)

// ensureExecutorsForAuth ensures the appropriate executor is registered for the given auth.
// It selects executors based on the provider type and registers them with the core manager.
func ensureExecutorsForAuth(a *coreauth.Auth, cfg *config.Config, coreManager *coreauth.Manager, wsGateway *wsrelay.Manager) {
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
		coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(compatProviderKey, cfg))
		return
	}
	registerProviderExecutor(a, cfg, coreManager, wsGateway)
}

// registerProviderExecutor registers the appropriate executor based on provider type.
func registerProviderExecutor(a *coreauth.Auth, cfg *config.Config, coreManager *coreauth.Manager, wsGateway *wsrelay.Manager) {
	provider := strings.ToLower(strings.TrimSpace(a.Provider))
	switch provider {
	case "gemini":
		coreManager.RegisterExecutor(executor.NewGeminiExecutor(cfg))
	case "vertex":
		coreManager.RegisterExecutor(executor.NewGeminiVertexExecutor(cfg))
	case "gemini-cli":
		coreManager.RegisterExecutor(executor.NewGeminiCLIExecutor(cfg))
	case "aistudio":
		if wsGateway != nil {
			coreManager.RegisterExecutor(executor.NewAIStudioExecutor(cfg, a.ID, wsGateway))
		}
		return
	case "antigravity":
		coreManager.RegisterExecutor(executor.NewAntigravityExecutor(cfg))
	case "claude":
		coreManager.RegisterExecutor(executor.NewClaudeExecutor(cfg))
	case "codex":
		coreManager.RegisterExecutor(executor.NewCodexExecutor(cfg))
	case "qwen":
		coreManager.RegisterExecutor(executor.NewQwenExecutor(cfg))
	case "iflow":
		coreManager.RegisterExecutor(executor.NewIFlowExecutor(cfg))
	case "cline":
		coreManager.RegisterExecutor(executor.NewClineExecutor(cfg))
	case "kiro":
		coreManager.RegisterExecutor(executor.NewKiroExecutor(cfg))
	case "github-copilot":
		coreManager.RegisterExecutor(executor.NewGitHubCopilotExecutor(cfg))
	default:
		providerKey := strings.ToLower(strings.TrimSpace(a.Provider))
		if providerKey == "" {
			providerKey = "openai-compatibility"
		}
		coreManager.RegisterExecutor(executor.NewOpenAICompatExecutor(providerKey, cfg))
	}
}

// rebindExecutors refreshes provider executors so they observe the latest configuration.
func rebindExecutors(coreManager *coreauth.Manager, cfg *config.Config, wsGateway *wsrelay.Manager) {
	if coreManager == nil {
		return
	}
	auths := coreManager.List()
	for _, auth := range auths {
		ensureExecutorsForAuth(auth, cfg, coreManager, wsGateway)
	}
}
