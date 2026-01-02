package cli

import (
	"fmt"

	"github.com/nghyane/llm-mux/internal/bootstrap"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/spf13/cobra"
)

var (
	legacyLogin            bool
	legacyCodexLogin       bool
	legacyClaudeLogin      bool
	legacyQwenLogin        bool
	legacyIFlowLogin       bool
	legacyIFlowCookie      bool
	legacyClineLogin       bool
	legacyAntigravityLogin bool
	legacyKiroLogin        bool
	legacyCopilotLogin     bool
	legacyInit             bool
	legacyForce            bool
	legacyUpdate           bool
	legacyProjectID        string
	legacyVertexImport     string
)

func initLegacyFlags() {
	f := rootCmd.Flags()
	f.BoolVar(&legacyLogin, "login", false, "Login Google Account (deprecated: use 'login gemini')")
	f.BoolVar(&legacyCodexLogin, "codex-login", false, "Login to Codex (deprecated: use 'login codex')")
	f.BoolVar(&legacyClaudeLogin, "claude-login", false, "Login to Claude (deprecated: use 'login claude')")
	f.BoolVar(&legacyQwenLogin, "qwen-login", false, "Login to Qwen (deprecated: use 'login qwen')")
	f.BoolVar(&legacyIFlowLogin, "iflow-login", false, "Login to iFlow (deprecated: use 'login iflow')")
	f.BoolVar(&legacyIFlowCookie, "iflow-cookie", false, "Login to iFlow using Cookie (deprecated)")
	f.BoolVar(&legacyClineLogin, "cline-login", false, "Login to Cline (deprecated: use 'login cline')")
	f.BoolVar(&legacyAntigravityLogin, "antigravity-login", false, "Login to Antigravity (deprecated: use 'login antigravity')")
	f.BoolVar(&legacyKiroLogin, "kiro-login", false, "Login to Kiro (deprecated: use 'login kiro')")
	f.BoolVar(&legacyCopilotLogin, "copilot-login", false, "Login to Copilot (deprecated: use 'login copilot')")

	f.BoolVar(&legacyInit, "init", false, "Initialize config (deprecated: use 'init')")
	f.BoolVar(&legacyForce, "force", false, "Force regenerate key (deprecated: use 'init --force')")
	f.BoolVar(&legacyUpdate, "update", false, "Check for updates (deprecated: use 'update')")

	f.StringVar(&legacyProjectID, "project-id", "", "Project ID (deprecated: use 'login gemini --project')")
	f.StringVar(&legacyVertexImport, "vertex-import", "", "Import Vertex key (deprecated: use 'import vertex')")

	// Hide them
	_ = f.MarkHidden("login")
	_ = f.MarkHidden("codex-login")
	_ = f.MarkHidden("claude-login")
	_ = f.MarkHidden("qwen-login")
	_ = f.MarkHidden("iflow-login")
	_ = f.MarkHidden("iflow-cookie")
	_ = f.MarkHidden("cline-login")
	_ = f.MarkHidden("antigravity-login")
	_ = f.MarkHidden("kiro-login")
	_ = f.MarkHidden("copilot-login")
	_ = f.MarkHidden("init")
	_ = f.MarkHidden("force")
	_ = f.MarkHidden("update")
	_ = f.MarkHidden("project-id")
	_ = f.MarkHidden("vertex-import")
}

func handleLegacyFlags(c *cobra.Command, args []string) bool {
	// If any legacy flag is set, run the logic and return true.
	// Otherwise return false to proceed with default behavior (serve).

	// Helper to load config for legacy commands
	loadCfg := func() *config.Config {
		path := cfgFile
		if path == "" {
			path = "$XDG_CONFIG_HOME/llm-mux/config.yaml"
		}
		result, err := bootstrap.Bootstrap(path)
		if err != nil {
			return config.NewDefaultConfig()
		}
		return result.Config
	}

	getOpts := func() *cmd.LoginOptions {
		return &cmd.LoginOptions{NoBrowser: noBrowser}
	}

	if legacyVertexImport != "" {
		fmt.Println("Warning: --vertex-import is deprecated, use 'llm-mux import vertex'")
		cfg := loadCfg()
		cmd.DoVertexImport(cfg, legacyVertexImport)
		return true
	}

	if legacyInit {
		fmt.Println("Warning: --init flag is deprecated, use 'llm-mux init'")
		path := cfgFile
		if path == "" {
			path = "$XDG_CONFIG_HOME/llm-mux/config.yaml"
		}
		DoInitConfig(path, legacyForce)
		return true
	}

	if legacyUpdate {
		fmt.Println("Warning: --update flag is deprecated, use 'llm-mux update'")
		DoUpdate(false)
		return true
	}

	if legacyLogin {
		fmt.Println("Warning: --login is deprecated, use 'llm-mux login gemini'")
		cmd.DoLogin(loadCfg(), legacyProjectID, getOpts())
		return true
	}

	if legacyAntigravityLogin {
		fmt.Println("Warning: --antigravity-login is deprecated, use 'llm-mux login antigravity'")
		cmd.DoAntigravityLogin(loadCfg(), getOpts())
		return true
	}

	if legacyCodexLogin {
		fmt.Println("Warning: --codex-login is deprecated, use 'llm-mux login codex'")
		cmd.DoCodexLogin(loadCfg(), getOpts())
		return true
	}

	if legacyClaudeLogin {
		fmt.Println("Warning: --claude-login is deprecated, use 'llm-mux login claude'")
		cmd.DoClaudeLogin(loadCfg(), getOpts())
		return true
	}

	if legacyQwenLogin {
		fmt.Println("Warning: --qwen-login is deprecated, use 'llm-mux login qwen'")
		cmd.DoQwenLogin(loadCfg(), getOpts())
		return true
	}

	if legacyIFlowLogin {
		fmt.Println("Warning: --iflow-login is deprecated, use 'llm-mux login iflow'")
		cmd.DoIFlowLogin(loadCfg(), getOpts())
		return true
	}

	if legacyIFlowCookie {
		fmt.Println("Warning: --iflow-cookie is deprecated")
		cmd.DoIFlowCookieAuth(loadCfg(), getOpts())
		return true
	}

	if legacyClineLogin {
		fmt.Println("Warning: --cline-login is deprecated, use 'llm-mux login cline'")
		cmd.DoClineLogin(loadCfg(), getOpts())
		return true
	}

	if legacyKiroLogin {
		fmt.Println("Warning: --kiro-login is deprecated, use 'llm-mux login kiro'")
		cmd.DoKiroLogin(loadCfg(), getOpts())
		return true
	}

	if legacyCopilotLogin {
		fmt.Println("Warning: --copilot-login is deprecated, use 'llm-mux login copilot'")
		cmd.DoCopilotLogin(loadCfg(), getOpts())
		return true
	}

	return false
}
