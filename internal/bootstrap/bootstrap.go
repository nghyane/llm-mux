// Package bootstrap provides application initialization for llm-mux CLI commands.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	configaccess "github.com/nghyane/llm-mux/internal/access/config_access"
	authlogin "github.com/nghyane/llm-mux/internal/auth/login"
	"github.com/nghyane/llm-mux/internal/cli/env"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/store"
	"github.com/nghyane/llm-mux/internal/util"
)

// Result contains the result of bootstrapping the application.
type Result struct {
	Config         *config.Config
	ConfigFilePath string
}

// Bootstrap initializes the application configuration and stores.
// It should be called before any command that needs access to config or auth stores.
func Bootstrap(configPath string) (*Result, error) {
	wd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get working directory: %w", err)
	}

	// Load environment variables from .env if present.
	if errLoad := godotenv.Load(filepath.Join(wd, ".env")); errLoad != nil {
		if !errors.Is(errLoad, os.ErrNotExist) {
			log.WithError(errLoad).Warn("failed to load .env file")
		}
	}

	storeCfg := store.ParseFromEnv(env.LookupEnv)

	xdgConfigDir, _ := util.ResolveAuthDir("$XDG_CONFIG_HOME/llm-mux")
	defaultConfigPath := filepath.Join(xdgConfigDir, "config.yaml")
	defaultAuthDir := filepath.Join(xdgConfigDir, "auth")

	var cfg *config.Config
	var configFilePath string
	var storeResult *store.StoreResult

	if storeCfg.IsConfigured() {
		storeCfg.TargetConfigPath = defaultConfigPath
		storeCfg.TargetAuthDir = defaultAuthDir

		ctx := context.Background()
		storeResult, err = store.NewStore(ctx, storeCfg)
		if err != nil {
			return nil, fmt.Errorf("failed to initialize store: %w", err)
		}

		configFilePath = storeResult.ConfigPath
		cfg, err = config.LoadConfigOptional(configFilePath, true)
		if err == nil && cfg != nil {
			cfg.AuthDir = storeResult.AuthDir
		}

		switch storeCfg.Type {
		case store.TypePostgres:
			log.Infof("postgres-backed token store enabled")
		case store.TypeObject:
			log.Infof("object-backed token store enabled, bucket: %s", storeCfg.Object.Bucket)
		case store.TypeGit:
			log.Infof("git-backed token store enabled")
		}
	} else if configPath != "" {
		if resolved, errResolve := util.ResolveAuthDir(configPath); errResolve == nil {
			configPath = resolved
		}
		configFilePath = configPath

		if configPath == defaultConfigPath {
			if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
				autoInitConfig(configPath)
			}
		}

		cfg, err = config.LoadConfigOptional(configPath, true)
	} else {
		configFilePath = filepath.Join(wd, "config.yaml")
		cfg, err = config.LoadConfigOptional(configFilePath, true)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	if cfg == nil {
		cfg = config.NewDefaultConfig()
	}

	ApplyEnvOverrides(cfg)

	provider.SetQuotaCooldownDisabled(cfg.DisableCooling)

	if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
		return nil, fmt.Errorf("failed to resolve auth directory: %w", errResolveAuthDir)
	} else {
		cfg.AuthDir = resolvedAuthDir
	}

	// Register the shared token store
	if storeResult != nil && storeResult.Store != nil {
		authlogin.RegisterTokenStore(storeResult.Store)
	} else {
		authlogin.RegisterTokenStore(authlogin.NewFileTokenStore())
	}

	// Register built-in access providers
	configaccess.Register()

	return &Result{
		Config:         cfg,
		ConfigFilePath: configFilePath,
	}, nil
}

// ApplyEnvOverrides applies environment variable overrides for cloud deployment.
func ApplyEnvOverrides(cfg *config.Config) {
	if port, ok := env.LookupEnvInt("LLM_MUX_PORT"); ok {
		cfg.Port = port
		log.Infof("Port overridden by env: %d", port)
	}

	if debug, ok := env.LookupEnvBool("LLM_MUX_DEBUG"); ok {
		cfg.Debug = debug
		log.Infof("Debug overridden by env: %v", debug)
	}

	if disableAuth, ok := env.LookupEnvBool("LLM_MUX_DISABLE_AUTH"); ok {
		cfg.DisableAuth = disableAuth
		log.Infof("DisableAuth overridden by env: %v", disableAuth)
	}

	if keys, ok := env.LookupEnv("LLM_MUX_API_KEYS"); ok {
		cfg.APIKeys = nil
		for _, k := range strings.Split(keys, ",") {
			if trimmed := strings.TrimSpace(k); trimmed != "" {
				cfg.APIKeys = append(cfg.APIKeys, trimmed)
			}
		}
		log.Infof("API keys overridden by env: %d keys", len(cfg.APIKeys))
	}

	if dsn, ok := env.LookupEnv("LLM_MUX_USAGE_DSN"); ok {
		cfg.Usage.DSN = dsn
		log.Infof("Usage DSN overridden by env")
	}

	if days, ok := env.LookupEnvInt("LLM_MUX_USAGE_RETENTION_DAYS"); ok {
		cfg.Usage.RetentionDays = days
		log.Infof("Usage retention days overridden by env: %d", days)
	}

	if proxyURL, ok := env.LookupEnv("LLM_MUX_PROXY_URL"); ok {
		cfg.ProxyURL = proxyURL
		log.Infof("Proxy URL overridden by env")
	}

	if authDir, ok := env.LookupEnv("LLM_MUX_AUTH_DIR"); ok {
		cfg.AuthDir = authDir
		log.Infof("Auth dir overridden by env: %s", authDir)
	}

	if loggingToFile, ok := env.LookupEnvBool("LLM_MUX_LOGGING_TO_FILE"); ok {
		cfg.LoggingToFile = loggingToFile
		log.Infof("Logging to file overridden by env: %v", loggingToFile)
	}

	if retry, ok := env.LookupEnvInt("LLM_MUX_REQUEST_RETRY"); ok {
		cfg.RequestRetry = retry
		log.Infof("Request retry overridden by env: %d", retry)
	}

	if maxRetryInterval, ok := env.LookupEnvInt("LLM_MUX_MAX_RETRY_INTERVAL"); ok {
		cfg.MaxRetryInterval = maxRetryInterval
		log.Infof("Max retry interval overridden by env: %d", maxRetryInterval)
	}
}

// autoInitConfig silently creates config on first run
func autoInitConfig(configPath string) {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	authDir := filepath.Join(dir, "auth")
	_ = os.MkdirAll(authDir, 0o700)
	if err := os.WriteFile(configPath, config.GenerateDefaultConfigYAML(), 0o600); err != nil {
		return
	}
	fmt.Printf("First run: created config at %s\n", configPath)
}
