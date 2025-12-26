// Package main provides the entry point for the CLI Proxy API server.
// This server acts as a proxy that provides OpenAI/Gemini/Claude compatible API interfaces
// for CLI models, allowing CLI models to be used with tools and libraries designed for standard AI APIs.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	configaccess "github.com/nghyane/llm-mux/internal/access/config_access"
	"github.com/nghyane/llm-mux/internal/buildinfo"
	"github.com/nghyane/llm-mux/internal/cmd"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/embedded"
	"github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/store"
	"github.com/nghyane/llm-mux/internal/usage"
	"github.com/nghyane/llm-mux/internal/util"
	sdkAuth "github.com/nghyane/llm-mux/sdk/auth"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	flag "github.com/spf13/pflag"
)

var (
	Version           = "dev"
	Commit            = "none"
	BuildDate         = "unknown"
	DefaultConfigPath = "$XDG_CONFIG_HOME/llm-mux/config.yaml"
)

// init initializes the shared logger setup.
func init() {
	logging.SetupBaseLogger()
	buildinfo.Version = Version
	buildinfo.Commit = Commit
	buildinfo.BuildDate = BuildDate
}

// main is the entry point of the application.
// It parses command-line flags, loads configuration, and starts the appropriate
// service based on the provided flags (login, codex-login, or server mode).
func main() {
	fmt.Printf("llm-mux Version: %s, Commit: %s, BuiltAt: %s\n", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)

	var login bool
	var codexLogin bool
	var claudeLogin bool
	var qwenLogin bool
	var iflowLogin bool
	var iflowCookie bool
	var clineLogin bool
	var noBrowser bool
	var antigravityLogin bool
	var kiroLogin bool
	var copilotLogin bool
	var initConfig bool
	var forceInit bool
	var updateFlag bool
	var projectID string
	var vertexImport string
	var configPath string
	var password string

	flag.BoolVar(&login, "login", false, "Login Google Account")
	flag.BoolVar(&codexLogin, "codex-login", false, "Login to Codex using OAuth")
	flag.BoolVar(&claudeLogin, "claude-login", false, "Login to Claude using OAuth")
	flag.BoolVar(&qwenLogin, "qwen-login", false, "Login to Qwen using OAuth")
	flag.BoolVar(&iflowLogin, "iflow-login", false, "Login to iFlow using OAuth")
	flag.BoolVar(&iflowCookie, "iflow-cookie", false, "Login to iFlow using Cookie")
	flag.BoolVar(&clineLogin, "cline-login", false, "Login to Cline using refresh token")
	flag.BoolVar(&noBrowser, "no-browser", false, "Don't open browser automatically for OAuth")
	flag.BoolVar(&antigravityLogin, "antigravity-login", false, "Login to Antigravity using OAuth")
	flag.BoolVar(&kiroLogin, "kiro-login", false, "Login to Kiro (Amazon Q) using refresh token")
	flag.BoolVar(&copilotLogin, "copilot-login", false, "Login to GitHub Copilot using device flow")
	flag.BoolVar(&initConfig, "init", false, "Initialize config and generate management key")
	flag.BoolVar(&forceInit, "force", false, "Force regenerate management key (use with --init)")
	flag.BoolVar(&updateFlag, "update", false, "Check for updates and install if available")
	flag.StringVar(&projectID, "project-id", "", "Project ID (Gemini only, not required)")
	flag.StringVar(&configPath, "config", DefaultConfigPath, "Configure File Path")
	flag.StringVar(&vertexImport, "vertex-import", "", "Import Vertex service account key JSON file")
	flag.StringVar(&password, "password", "", "")
	_ = flag.CommandLine.MarkHidden("password")

	flag.Parse()

	if initConfig {
		doInitConfig(configPath, forceInit)
		return
	}

	if updateFlag {
		doUpdate()
		return
	}

	// Core application variables.
	var err error
	var cfg *config.Config
	var (
		usePostgresStore     bool
		pgStoreDSN           string
		pgStoreSchema        string
		pgStoreLocalPath     string
		pgStoreInst          *store.PostgresStore
		useGitStore          bool
		gitStoreRemoteURL    string
		gitStoreUser         string
		gitStorePassword     string
		gitStoreLocalPath    string
		gitStoreInst         *store.GitTokenStore
		gitStoreRoot         string
		useObjectStore       bool
		objectStoreEndpoint  string
		objectStoreAccess    string
		objectStoreSecret    string
		objectStoreBucket    string
		objectStoreLocalPath string
		objectStoreInst      *store.ObjectTokenStore
	)

	wd, err := os.Getwd()
	if err != nil {
		log.Fatalf("failed to get working directory: %v", err)
	}

	// Load environment variables from .env if present.
	if errLoad := godotenv.Load(filepath.Join(wd, ".env")); errLoad != nil {
		if !errors.Is(errLoad, os.ErrNotExist) {
			log.WithError(errLoad).Warn("failed to load .env file")
		}
	}

	lookupEnv := func(keys ...string) (string, bool) {
		for _, key := range keys {
			if value, ok := os.LookupEnv(key); ok {
				if trimmed := strings.TrimSpace(value); trimmed != "" {
					return trimmed, true
				}
			}
		}
		return "", false
	}
	writableBase := util.WritablePath()
	if value, ok := lookupEnv("PGSTORE_DSN", "pgstore_dsn"); ok {
		usePostgresStore = true
		pgStoreDSN = value
	}
	if usePostgresStore {
		if value, ok := lookupEnv("PGSTORE_SCHEMA", "pgstore_schema"); ok {
			pgStoreSchema = value
		}
		if value, ok := lookupEnv("PGSTORE_LOCAL_PATH", "pgstore_local_path"); ok {
			pgStoreLocalPath = value
		}
		if pgStoreLocalPath == "" {
			if writableBase != "" {
				pgStoreLocalPath = writableBase
			} else {
				pgStoreLocalPath = wd
			}
		}
		useGitStore = false
	}
	if value, ok := lookupEnv("GITSTORE_GIT_URL", "gitstore_git_url"); ok {
		useGitStore = true
		gitStoreRemoteURL = value
	}
	if value, ok := lookupEnv("GITSTORE_GIT_USERNAME", "gitstore_git_username"); ok {
		gitStoreUser = value
	}
	if value, ok := lookupEnv("GITSTORE_GIT_TOKEN", "gitstore_git_token"); ok {
		gitStorePassword = value
	}
	if value, ok := lookupEnv("GITSTORE_LOCAL_PATH", "gitstore_local_path"); ok {
		gitStoreLocalPath = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_ENDPOINT", "objectstore_endpoint"); ok {
		useObjectStore = true
		objectStoreEndpoint = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_ACCESS_KEY", "objectstore_access_key"); ok {
		objectStoreAccess = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_SECRET_KEY", "objectstore_secret_key"); ok {
		objectStoreSecret = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_BUCKET", "objectstore_bucket"); ok {
		objectStoreBucket = value
	}
	if value, ok := lookupEnv("OBJECTSTORE_LOCAL_PATH", "objectstore_local_path"); ok {
		objectStoreLocalPath = value
	}

	// Determine and load the configuration file.
	// Prefer the Postgres store when configured, otherwise fallback to git or local files.
	var configFilePath string
	if usePostgresStore {
		if pgStoreLocalPath == "" {
			pgStoreLocalPath = wd
		}
		pgStoreLocalPath = filepath.Join(pgStoreLocalPath, "pgstore")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		pgStoreInst, err = store.NewPostgresStore(ctx, store.PostgresStoreConfig{
			DSN:      pgStoreDSN,
			Schema:   pgStoreSchema,
			SpoolDir: pgStoreLocalPath,
		})
		cancel()
		if err != nil {
			log.Fatalf("failed to initialize postgres token store: %v", err)
		}
		ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
		if errBootstrap := pgStoreInst.Bootstrap(ctx); errBootstrap != nil {
			cancel()
			log.Fatalf("failed to bootstrap postgres-backed config: %v", errBootstrap)
		}
		cancel()
		configFilePath = pgStoreInst.ConfigPath()
		cfg, err = config.LoadConfigOptional(configFilePath, false)
		if err == nil {
			cfg.AuthDir = pgStoreInst.AuthDir()
			log.Infof("postgres-backed token store enabled, workspace path: %s", pgStoreInst.WorkDir())
		}
	} else if useObjectStore {
		if objectStoreLocalPath == "" {
			if writableBase != "" {
				objectStoreLocalPath = writableBase
			} else {
				objectStoreLocalPath = wd
			}
		}
		objectStoreRoot := filepath.Join(objectStoreLocalPath, "objectstore")
		resolvedEndpoint := strings.TrimSpace(objectStoreEndpoint)
		useSSL := true
		if strings.Contains(resolvedEndpoint, "://") {
			parsed, errParse := url.Parse(resolvedEndpoint)
			if errParse != nil {
				log.Fatalf("failed to parse object store endpoint %q: %v", objectStoreEndpoint, errParse)
			}
			switch strings.ToLower(parsed.Scheme) {
			case "http":
				useSSL = false
			case "https":
				useSSL = true
			default:
				log.Fatalf("unsupported object store scheme %q (only http and https are allowed)", parsed.Scheme)
			}
			if parsed.Host == "" {
				log.Fatalf("object store endpoint %q is missing host information", objectStoreEndpoint)
			}
			resolvedEndpoint = parsed.Host
			if parsed.Path != "" && parsed.Path != "/" {
				resolvedEndpoint = strings.TrimSuffix(parsed.Host+parsed.Path, "/")
			}
		}
		resolvedEndpoint = strings.TrimRight(resolvedEndpoint, "/")
		objCfg := store.ObjectStoreConfig{
			Endpoint:  resolvedEndpoint,
			Bucket:    objectStoreBucket,
			AccessKey: objectStoreAccess,
			SecretKey: objectStoreSecret,
			LocalRoot: objectStoreRoot,
			UseSSL:    useSSL,
			PathStyle: true,
		}
		objectStoreInst, err = store.NewObjectTokenStore(objCfg)
		if err != nil {
			log.Fatalf("failed to initialize object token store: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if errBootstrap := objectStoreInst.Bootstrap(ctx); errBootstrap != nil {
			cancel()
			log.Fatalf("failed to bootstrap object-backed config: %v", errBootstrap)
		}
		cancel()
		configFilePath = objectStoreInst.ConfigPath()
		cfg, err = config.LoadConfigOptional(configFilePath, false)
		if err == nil {
			if cfg == nil {
				cfg = &config.Config{}
			}
			cfg.AuthDir = objectStoreInst.AuthDir()
			log.Infof("object-backed token store enabled, bucket: %s", objectStoreBucket)
		}
	} else if useGitStore {
		if gitStoreLocalPath == "" {
			if writableBase != "" {
				gitStoreLocalPath = writableBase
			} else {
				gitStoreLocalPath = wd
			}
		}
		gitStoreRoot = filepath.Join(gitStoreLocalPath, "gitstore")
		authDir := filepath.Join(gitStoreRoot, "auths")
		gitStoreInst = store.NewGitTokenStore(gitStoreRemoteURL, gitStoreUser, gitStorePassword)
		gitStoreInst.SetBaseDir(authDir)
		if errRepo := gitStoreInst.EnsureRepository(); errRepo != nil {
			log.Fatalf("failed to prepare git token store: %v", errRepo)
		}
		configFilePath = gitStoreInst.ConfigPath()
		if configFilePath == "" {
			configFilePath = filepath.Join(gitStoreRoot, "config", "config.yaml")
		}
		if _, statErr := os.Stat(configFilePath); errors.Is(statErr, fs.ErrNotExist) {
			if errDir := os.MkdirAll(filepath.Dir(configFilePath), 0o700); errDir != nil {
				log.Fatalf("failed to create config directory: %v", errDir)
			}
			if errWrite := os.WriteFile(configFilePath, embedded.DefaultConfigTemplate(), 0o600); errWrite != nil {
				log.Fatalf("failed to write config from template: %v", errWrite)
			}
			if errCommit := gitStoreInst.PersistConfig(context.Background()); errCommit != nil {
				log.Fatalf("failed to commit initial git-backed config: %v", errCommit)
			}
			log.Infof("git-backed config initialized from template: %s", configFilePath)
		} else if statErr != nil {
			log.Fatalf("failed to inspect git-backed config: %v", statErr)
		}
		cfg, err = config.LoadConfigOptional(configFilePath, false)
		if err == nil {
			cfg.AuthDir = gitStoreInst.AuthDir()
			log.Infof("git-backed token store enabled, repository path: %s", gitStoreRoot)
		}
	} else if configPath != "" {
		// Expand ~ to home directory
		if strings.HasPrefix(configPath, "~/") {
			if home, errHome := os.UserHomeDir(); errHome == nil {
				configPath = filepath.Join(home, configPath[2:])
			}
		}
		configFilePath = configPath

		// Auto-init on first run: create config from template if using default path and doesn't exist
		if configPath == expandPath(DefaultConfigPath) {
			if _, statErr := os.Stat(configPath); os.IsNotExist(statErr) {
				autoInitConfig(configPath)
			}
		}

		// Always optional=true for file-based config to support zero-config startup
		cfg, err = config.LoadConfigOptional(configPath, true)
	} else {
		wd, err = os.Getwd()
		if err != nil {
			log.Fatalf("failed to get working directory: %v", err)
		}
		configFilePath = filepath.Join(wd, "config.yaml")
		// Always optional=true for file-based config to support zero-config startup
		cfg, err = config.LoadConfigOptional(configFilePath, true)
	}
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	if cfg == nil {
		cfg = config.NewDefaultConfig()
	}

	usage.SetStatisticsEnabled(cfg.UsageStatisticsEnabled)

	// Initialize usage persistence if enabled
	if cfg.UsagePersistence.Enabled {
		if err := usage.InitializePersistence(
			cfg.UsagePersistence.DBPath,
			cfg.UsagePersistence.BatchSize,
			cfg.UsagePersistence.FlushIntervalSecs,
			cfg.UsagePersistence.RetentionDays,
		); err != nil {
			log.Warnf("Failed to initialize usage persistence: %v", err)
		}
	}

	coreauth.SetQuotaCooldownDisabled(cfg.DisableCooling)

	if err = logging.ConfigureLogOutput(cfg.LoggingToFile); err != nil {
		log.Fatalf("failed to configure log output: %v", err)
	}

	log.Infof("llm-mux Version: %s, Commit: %s, BuiltAt: %s", buildinfo.Version, buildinfo.Commit, buildinfo.BuildDate)

	// Set the log level based on the configuration.
	util.SetLogLevel(cfg)

	if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
		log.Fatalf("failed to resolve auth directory: %v", errResolveAuthDir)
	} else {
		cfg.AuthDir = resolvedAuthDir
	}

	// Create login options to be used in authentication flows.
	options := &cmd.LoginOptions{
		NoBrowser: noBrowser,
	}

	// Register the shared token store once so all components use the same persistence backend.
	if usePostgresStore {
		sdkAuth.RegisterTokenStore(pgStoreInst)
	} else if useObjectStore {
		sdkAuth.RegisterTokenStore(objectStoreInst)
	} else if useGitStore {
		sdkAuth.RegisterTokenStore(gitStoreInst)
	} else {
		sdkAuth.RegisterTokenStore(sdkAuth.NewFileTokenStore())
	}

	// Register built-in access providers before constructing services.
	configaccess.Register()

	// Handle different command modes based on the provided flags.

	if vertexImport != "" {
		// Handle Vertex service account import
		cmd.DoVertexImport(cfg, vertexImport)
	} else if login {
		// Handle Google/Gemini login
		cmd.DoLogin(cfg, projectID, options)
	} else if antigravityLogin {
		// Handle Antigravity login
		cmd.DoAntigravityLogin(cfg, options)
	} else if codexLogin {
		// Handle Codex login
		cmd.DoCodexLogin(cfg, options)
	} else if claudeLogin {
		// Handle Claude login
		cmd.DoClaudeLogin(cfg, options)
	} else if qwenLogin {
		cmd.DoQwenLogin(cfg, options)
	} else if iflowLogin {
		cmd.DoIFlowLogin(cfg, options)
	} else if iflowCookie {
		cmd.DoIFlowCookieAuth(cfg, options)
	} else if clineLogin {
		cmd.DoClineLogin(cfg, options)
	} else if kiroLogin {
		cmd.DoKiroLogin(cfg, options)
	} else if copilotLogin {
		cmd.DoCopilotLogin(cfg, options)
	} else {
		cmd.StartService(cfg, configFilePath, password)
	}
}

// expandPath expands $XDG_CONFIG_HOME and ~ to actual paths (XDG-compliant)
func expandPath(path string) string {
	// Handle $XDG_CONFIG_HOME prefix
	if strings.HasPrefix(path, "$XDG_CONFIG_HOME") {
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			// Fallback to ~/.config if XDG_CONFIG_HOME not set
			if home, err := os.UserHomeDir(); err == nil {
				xdg = filepath.Join(home, ".config")
			}
		}
		if xdg != "" {
			remainder := strings.TrimPrefix(path, "$XDG_CONFIG_HOME")
			remainder = strings.TrimLeft(remainder, "/\\")
			if remainder == "" {
				return filepath.Clean(xdg)
			}
			normalized := strings.ReplaceAll(remainder, "\\", "/")
			return filepath.Clean(filepath.Join(xdg, filepath.FromSlash(normalized)))
		}
	}

	// Handle ~ prefix (legacy support)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

// autoInitConfig silently creates config on first run
func autoInitConfig(configPath string) {
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	authDir := filepath.Join(dir, "auth")
	_ = os.MkdirAll(authDir, 0o700)
	if err := os.WriteFile(configPath, embedded.DefaultConfigTemplate(), 0o600); err != nil {
		return
	}
	fmt.Printf("First run: created config at %s\n", configPath)
}

// doInitConfig handles --init with smart behavior:
// - Config missing → create config
// - Credentials missing → create credentials (uses XDG_CONFIG_HOME or ~/.config/llm-mux/)
// - Both exist → show current key (use --force to regenerate)
func doInitConfig(configPath string, force bool) {
	configPath = expandPath(configPath)
	dir := filepath.Dir(configPath)
	credPath := config.CredentialsFilePath()

	configExists := fileExists(configPath)
	credExists := fileExists(credPath)

	// Ensure config directory exists
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Fatalf("Failed to create directory: %v", err)
	}
	_ = os.MkdirAll(filepath.Join(dir, "auth"), 0o700)

	// Create config if missing
	if !configExists {
		if err := os.WriteFile(configPath, embedded.DefaultConfigTemplate(), 0o600); err != nil {
			log.Fatalf("Failed to write config: %v", err)
		}
		fmt.Printf("Created: %s\n", configPath)
	}

	// Handle credentials (always at fixed path)
	if credExists && !force {
		key := config.GetManagementKey()
		if key != "" {
			fmt.Printf("Management key: %s\n", key)
			fmt.Printf("Location: %s\n", credPath)
			fmt.Println("Use --init --force to regenerate")
			return
		}
	}

	// Generate new key
	key, err := config.CreateCredentials()
	if err != nil {
		log.Fatalf("Failed to create credentials: %v", err)
	}

	if credExists && force {
		fmt.Println("Regenerated management key:")
	} else {
		fmt.Println("Generated management key:")
	}
	fmt.Printf("  %s\n", key)
	fmt.Printf("Location: %s\n", credPath)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// doUpdate checks for updates and runs the install script if a newer version is available.
func doUpdate() {
	fmt.Println("Checking for updates...")

	// Fetch latest release version from GitHub API
	latestVersion, err := fetchLatestVersion()
	if err != nil {
		log.Fatalf("Failed to check for updates: %v", err)
	}

	currentVersion := strings.TrimPrefix(buildinfo.Version, "v")
	latestVersion = strings.TrimPrefix(latestVersion, "v")

	if currentVersion == "dev" || currentVersion == "" {
		fmt.Println("Running development version, updating to latest release...")
	} else if compareVersions(currentVersion, latestVersion) >= 0 {
		fmt.Printf("Already up to date (current: v%s, latest: v%s)\n", currentVersion, latestVersion)
		return
	} else {
		fmt.Printf("Update available: v%s -> v%s\n", currentVersion, latestVersion)
	}

	fmt.Println("Downloading and installing update...")
	if err := runInstallScript(); err != nil {
		log.Fatalf("Failed to install update: %v", err)
	}
	fmt.Println("Update complete! Please restart llm-mux.")
}

// fetchLatestVersion fetches the latest release version from GitHub.
func fetchLatestVersion() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/nghyane/llm-mux/releases/latest", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return "", err
	}
	return release.TagName, nil
}

// compareVersions compares two semantic versions.
// Returns: -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
func compareVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &n1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &n2)
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}
	return 0
}

// runInstallScript downloads and runs the install script.
func runInstallScript() error {
	// Download install script
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://raw.githubusercontent.com/nghyane/llm-mux/main/install.sh", nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("failed to download install script: status %d", resp.StatusCode)
	}

	scriptContent, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Create temp file for script
	tmpFile, err := os.CreateTemp("", "llm-mux-install-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	if _, err := tmpFile.Write(scriptContent); err != nil {
		tmpFile.Close()
		return err
	}
	tmpFile.Close()

	// Make executable and run
	if err := os.Chmod(tmpFile.Name(), 0755); err != nil {
		return err
	}

	cmd := exec.Command("bash", tmpFile.Name())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
