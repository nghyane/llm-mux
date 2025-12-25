// Package api provides the HTTP API server implementation for the CLI Proxy API.
// It includes the main server struct, routing setup, middleware for CORS and authentication,
// and integration with various AI API handlers (OpenAI, Claude, Gemini).
// The server supports hot-reloading of clients and configuration.
package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/access"
	managementHandlers "github.com/nghyane/llm-mux/internal/api/handlers/management"
	"github.com/nghyane/llm-mux/internal/api/middleware"
	"github.com/nghyane/llm-mux/internal/api/modules"
	ampmodule "github.com/nghyane/llm-mux/internal/api/modules/amp"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/usage"
	"github.com/nghyane/llm-mux/internal/util"
	sdkaccess "github.com/nghyane/llm-mux/sdk/access"
	"github.com/nghyane/llm-mux/sdk/api/handlers"
	"github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type serverOptionConfig struct {
	extraMiddleware      []gin.HandlerFunc
	engineConfigurator   func(*gin.Engine)
	routerConfigurator   func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)
	requestLoggerFactory func(*config.Config, string) logging.RequestLogger
	localPassword        string
	keepAliveEnabled     bool
	keepAliveTimeout     time.Duration
	keepAliveOnTimeout   func()
}

// ServerOption customises HTTP server construction.
type ServerOption func(*serverOptionConfig)

func defaultRequestLoggerFactory(cfg *config.Config, configPath string) logging.RequestLogger {
	configDir := filepath.Dir(configPath)
	if base := util.WritablePath(); base != "" {
		return logging.NewFileRequestLogger(cfg.RequestLog, filepath.Join(base, "logs"), configDir)
	}
	return logging.NewFileRequestLogger(cfg.RequestLog, "logs", configDir)
}

// WithMiddleware appends additional Gin middleware during server construction.
func WithMiddleware(mw ...gin.HandlerFunc) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.extraMiddleware = append(cfg.extraMiddleware, mw...)
	}
}

// WithEngineConfigurator allows callers to mutate the Gin engine prior to middleware setup.
func WithEngineConfigurator(fn func(*gin.Engine)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.engineConfigurator = fn
	}
}

// WithRouterConfigurator appends a callback after default routes are registered.
func WithRouterConfigurator(fn func(*gin.Engine, *handlers.BaseAPIHandler, *config.Config)) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.routerConfigurator = fn
	}
}

// WithLocalManagementPassword stores a runtime-only management password accepted for localhost requests.
func WithLocalManagementPassword(password string) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.localPassword = password
	}
}

// WithKeepAliveEndpoint enables a keep-alive endpoint with the provided timeout and callback.
func WithKeepAliveEndpoint(timeout time.Duration, onTimeout func()) ServerOption {
	return func(cfg *serverOptionConfig) {
		if timeout <= 0 || onTimeout == nil {
			return
		}
		cfg.keepAliveEnabled = true
		cfg.keepAliveTimeout = timeout
		cfg.keepAliveOnTimeout = onTimeout
	}
}

// WithRequestLoggerFactory customises request logger creation.
func WithRequestLoggerFactory(factory func(*config.Config, string) logging.RequestLogger) ServerOption {
	return func(cfg *serverOptionConfig) {
		cfg.requestLoggerFactory = factory
	}
}

// Server represents the main API server.
type Server struct {
	engine   *gin.Engine
	server   *http.Server
	handlers *handlers.BaseAPIHandler
	cfg      *config.Config

	// oldConfigYaml stores YAML snapshot for change detection (avoids in-place mutation issues).
	oldConfigYaml []byte

	accessManager  *sdkaccess.Manager
	requestLogger  logging.RequestLogger
	loggerToggle   func(bool)
	configFilePath string
	currentPath    string

	wsRouteMu     sync.Mutex
	wsRoutes      map[string]struct{}
	wsAuthChanged func(bool, bool)
	wsAuthEnabled atomic.Bool

	mgmt      *managementHandlers.Handler
	ampModule *ampmodule.AmpModule

	managementRoutesRegistered atomic.Bool
	managementRoutesEnabled    atomic.Bool

	localPassword      string
	keepAliveEnabled   bool
	keepAliveTimeout   time.Duration
	keepAliveOnTimeout func()
	keepAliveHeartbeat chan struct{}
	keepAliveStop      chan struct{}
}

// NewServer creates and initializes a new API server instance.
// It sets up the Gin engine, middleware, routes, and handlers.
// Parameters:
//   - cfg: The server configuration
//   - authManager: core runtime auth manager
//   - accessManager: request authentication manager
//
// Returns:
//   - *Server: A new server instance
func NewServer(cfg *config.Config, authManager *auth.Manager, accessManager *sdkaccess.Manager, configFilePath string, opts ...ServerOption) *Server {
	optionState := &serverOptionConfig{
		requestLoggerFactory: defaultRequestLoggerFactory,
	}
	for i := range opts {
		opts[i](optionState)
	}
	if cfg.Debug {
		gin.SetMode(gin.DebugMode)
	}

	engine := gin.New()
	if optionState.engineConfigurator != nil {
		optionState.engineConfigurator(engine)
	}

	engine.Use(logging.GinLogrusLogger())
	engine.Use(logging.GinLogrusRecovery())
	for _, mw := range optionState.extraMiddleware {
		engine.Use(mw)
	}

	// Add request logging middleware (positioned after recovery, before auth)
	// Resolve logs directory relative to the configuration file directory.
	var requestLogger logging.RequestLogger
	var toggle func(bool)
	if optionState.requestLoggerFactory != nil {
		requestLogger = optionState.requestLoggerFactory(cfg, configFilePath)
	}
	if requestLogger != nil {
		engine.Use(middleware.RequestLoggingMiddleware(requestLogger))
		if setter, ok := requestLogger.(interface{ SetEnabled(bool) }); ok {
			toggle = setter.SetEnabled
		}
	}

	engine.Use(corsMiddleware())
	wd, err := os.Getwd()
	if err != nil {
		wd = configFilePath
	}

	providerNames := make([]string, 0, len(cfg.OpenAICompatibility))
	for _, p := range cfg.OpenAICompatibility {
		providerNames = append(providerNames, p.Name)
	}
	s := &Server{
		engine:         engine,
		handlers:       handlers.NewBaseAPIHandlers(&cfg.SDKConfig, authManager, providerNames),
		cfg:            cfg,
		accessManager:  accessManager,
		requestLogger:  requestLogger,
		loggerToggle:   toggle,
		configFilePath: configFilePath,
		currentPath:    wd,
		wsRoutes:       make(map[string]struct{}),
	}
	s.wsAuthEnabled.Store(cfg.WebsocketAuth)
	// Save initial YAML snapshot
	s.oldConfigYaml, _ = yaml.Marshal(cfg)
	s.applyAccessConfig(nil, cfg)
	if authManager != nil {
		authManager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second)
	}
	auth.SetQuotaCooldownDisabled(cfg.DisableCooling)

	// Initialize provider prefix display setting in model registry
	registry.GetGlobalRegistry().SetShowProviderPrefixes(cfg.ShowProviderPrefixes)
	// Initialize management handler
	s.mgmt = managementHandlers.NewHandler(cfg, configFilePath, authManager)
	if optionState.localPassword != "" {
		s.mgmt.SetLocalPassword(optionState.localPassword)
	}
	logDir := filepath.Join(s.currentPath, "logs")
	if base := util.WritablePath(); base != "" {
		logDir = filepath.Join(base, "logs")
	}
	s.mgmt.SetLogDirectory(logDir)
	s.localPassword = optionState.localPassword

	// Setup routes
	s.setupRoutes()

	// Register Amp module using V2 interface with Context
	s.ampModule = ampmodule.New(
		ampmodule.WithAccessManager(accessManager),
		ampmodule.WithAuthMiddleware(AuthMiddleware(accessManager)),
	)
	ctx := modules.Context{
		Engine:         engine,
		BaseHandler:    s.handlers,
		Config:         cfg,
		AuthMiddleware: AuthMiddleware(accessManager),
	}
	if err := modules.RegisterModule(ctx, s.ampModule); err != nil {
		log.Errorf("Failed to register Amp module: %v", err)
	}

	// Apply additional router configurators from options
	if optionState.routerConfigurator != nil {
		optionState.routerConfigurator(engine, s.handlers, cfg)
	}

	// Register management routes when configuration or environment secrets are available.
	hasManagementSecret := config.HasManagementKey()
	s.managementRoutesEnabled.Store(hasManagementSecret)
	if hasManagementSecret {
		s.registerManagementRoutes()
	}

	if optionState.keepAliveEnabled {
		s.enableKeepAlive(optionState.keepAliveTimeout, optionState.keepAliveOnTimeout)
	}

	// Create HTTP server
	s.server = &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.Port),
		Handler: engine,
	}

	return s
}

// Start begins listening for and serving HTTP or HTTPS requests.
// It's a blocking call and will only return on an unrecoverable error.
// Returns:
//   - error: An error if the server fails to start
func (s *Server) Start() error {
	if s == nil || s.server == nil {
		return fmt.Errorf("failed to start HTTP server: server not initialized")
	}

	useTLS := s.cfg != nil && s.cfg.TLS.Enable
	if useTLS {
		cert := strings.TrimSpace(s.cfg.TLS.Cert)
		key := strings.TrimSpace(s.cfg.TLS.Key)
		if cert == "" || key == "" {
			return fmt.Errorf("failed to start HTTPS server: tls.cert or tls.key is empty")
		}
		log.Debugf("Starting API server on %s with TLS", s.server.Addr)
		if errServeTLS := s.server.ListenAndServeTLS(cert, key); errServeTLS != nil && !errors.Is(errServeTLS, http.ErrServerClosed) {
			return fmt.Errorf("failed to start HTTPS server: %v", errServeTLS)
		}
		return nil
	}

	log.Debugf("Starting API server on %s", s.server.Addr)
	if errServe := s.server.ListenAndServe(); errServe != nil && !errors.Is(errServe, http.ErrServerClosed) {
		return fmt.Errorf("failed to start HTTP server: %v", errServe)
	}

	return nil
}

// Stop gracefully shuts down the API server without interrupting any
// active connections.
// Parameters:
//   - ctx: The context for graceful shutdown
//
// Returns:
//   - error: An error if the server fails to stop
func (s *Server) Stop(ctx context.Context) error {
	log.Debug("Stopping API server...")

	if s.keepAliveEnabled {
		select {
		case s.keepAliveStop <- struct{}{}:
		default:
		}
	}

	// Shutdown the HTTP server.
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown HTTP server: %v", err)
	}

	// Stop usage persistence and flush pending writes
	if err := usage.StopPersistence(); err != nil {
		log.Warnf("Failed to stop usage persistence: %v", err)
	}

	log.Debug("API server stopped")
	return nil
}

func (s *Server) applyAccessConfig(oldCfg, newCfg *config.Config) {
	if s == nil || s.accessManager == nil || newCfg == nil {
		return
	}
	if _, err := access.ApplyAccessProviders(s.accessManager, oldCfg, newCfg); err != nil {
		return
	}
}

// UpdateClients updates the server's client list and configuration.
// This method is called when the configuration or authentication tokens change.
// Parameters:
//   - clients: The new slice of AI service clients
//   - cfg: The new application configuration
func (s *Server) UpdateClients(cfg *config.Config) {
	// Reconstruct old config from YAML snapshot to avoid reference sharing issues
	var oldCfg *config.Config
	if len(s.oldConfigYaml) > 0 {
		_ = yaml.Unmarshal(s.oldConfigYaml, &oldCfg)
	}

	// Update request logger enabled state if it has changed
	previousRequestLog := false
	if oldCfg != nil {
		previousRequestLog = oldCfg.RequestLog
	}
	if s.requestLogger != nil && (oldCfg == nil || previousRequestLog != cfg.RequestLog) {
		if s.loggerToggle != nil {
			s.loggerToggle(cfg.RequestLog)
		} else if toggler, ok := s.requestLogger.(interface{ SetEnabled(bool) }); ok {
			toggler.SetEnabled(cfg.RequestLog)
		}
		if oldCfg != nil {
			log.Debugf("request logging updated from %t to %t", previousRequestLog, cfg.RequestLog)
		} else {
			log.Debugf("request logging toggled to %t", cfg.RequestLog)
		}
	}

	if oldCfg != nil && oldCfg.LoggingToFile != cfg.LoggingToFile {
		if err := logging.ConfigureLogOutput(cfg.LoggingToFile); err != nil {
			log.Errorf("failed to reconfigure log output: %v", err)
		} else {
			log.Debugf("logging_to_file updated from %t to %t", oldCfg.LoggingToFile, cfg.LoggingToFile)
		}
	}

	if oldCfg == nil || oldCfg.UsageStatisticsEnabled != cfg.UsageStatisticsEnabled {
		usage.SetStatisticsEnabled(cfg.UsageStatisticsEnabled)
		if oldCfg != nil {
			log.Debugf("usage_statistics_enabled updated from %t to %t", oldCfg.UsageStatisticsEnabled, cfg.UsageStatisticsEnabled)
		} else {
			log.Debugf("usage_statistics_enabled toggled to %t", cfg.UsageStatisticsEnabled)
		}
	}

	if oldCfg == nil || oldCfg.DisableCooling != cfg.DisableCooling {
		auth.SetQuotaCooldownDisabled(cfg.DisableCooling)
		if oldCfg != nil {
			log.Debugf("disable_cooling updated from %t to %t", oldCfg.DisableCooling, cfg.DisableCooling)
		} else {
			log.Debugf("disable_cooling toggled to %t", cfg.DisableCooling)
		}
	}
	if s.handlers != nil && s.handlers.AuthManager != nil {
		s.handlers.AuthManager.SetRetryConfig(cfg.RequestRetry, time.Duration(cfg.MaxRetryInterval)*time.Second)
	}

	// Update log level dynamically when debug flag changes
	if oldCfg == nil || oldCfg.Debug != cfg.Debug {
		util.SetLogLevel(cfg)
		if oldCfg != nil {
			log.Debugf("debug mode updated from %t to %t", oldCfg.Debug, cfg.Debug)
		} else {
			log.Debugf("debug mode toggled to %t", cfg.Debug)
		}
	}

	// Management routes are controlled by credentials.json (fixed path).
	// Check if management key is available and enable/disable routes accordingly.
	hasManagementKey := config.HasManagementKey()
	if hasManagementKey {
		s.registerManagementRoutes()
		if s.managementRoutesEnabled.CompareAndSwap(false, true) {
			log.Info("management routes enabled")
		}
	} else {
		if s.managementRoutesEnabled.CompareAndSwap(true, false) {
			log.Info("management routes disabled")
		}
	}

	s.applyAccessConfig(oldCfg, cfg)
	s.cfg = cfg
	s.wsAuthEnabled.Store(cfg.WebsocketAuth)
	if oldCfg != nil && s.wsAuthChanged != nil && oldCfg.WebsocketAuth != cfg.WebsocketAuth {
		s.wsAuthChanged(oldCfg.WebsocketAuth, cfg.WebsocketAuth)
	}

	// Update provider prefix display setting in model registry
	if oldCfg == nil || oldCfg.ShowProviderPrefixes != cfg.ShowProviderPrefixes {
		registry.GetGlobalRegistry().SetShowProviderPrefixes(cfg.ShowProviderPrefixes)
		if oldCfg != nil {
			log.Debugf("show_provider_prefixes updated from %t to %t", oldCfg.ShowProviderPrefixes, cfg.ShowProviderPrefixes)
		} else {
			log.Debugf("show_provider_prefixes toggled to %t", cfg.ShowProviderPrefixes)
		}
	}

	// Save YAML snapshot for next comparison
	s.oldConfigYaml, _ = yaml.Marshal(cfg)

	providerNames := make([]string, 0, len(cfg.OpenAICompatibility))
	for _, p := range cfg.OpenAICompatibility {
		providerNames = append(providerNames, p.Name)
	}
	s.handlers.OpenAICompatProviders = providerNames

	s.handlers.UpdateClients(&cfg.SDKConfig)

	if s.mgmt != nil {
		s.mgmt.SetConfig(cfg)
		s.mgmt.SetAuthManager(s.handlers.AuthManager)
	}

	// Notify Amp module of config changes (for model mapping hot-reload)
	if s.ampModule != nil {
		log.Debugf("triggering amp module config update")
		if err := s.ampModule.OnConfigUpdated(cfg); err != nil {
			log.Errorf("failed to update Amp module config: %v", err)
		}
	} else {
		log.Warnf("amp module is nil, skipping config update")
	}

	// Count client sources from configuration and auth directory
	authFiles := util.CountAuthFiles(cfg.AuthDir)
	geminiAPIKeyCount := len(cfg.GeminiKey)
	claudeAPIKeyCount := len(cfg.ClaudeKey)
	codexAPIKeyCount := len(cfg.CodexKey)
	vertexAICompatCount := len(cfg.VertexCompatAPIKey)
	openAICompatCount := 0
	for i := range cfg.OpenAICompatibility {
		entry := cfg.OpenAICompatibility[i]
		openAICompatCount += len(entry.APIKeyEntries)
	}

	total := authFiles + geminiAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + vertexAICompatCount + openAICompatCount
	log.Infof("server clients and configuration updated: %d clients (%d auth files + %d Gemini API keys + %d Claude API keys + %d Codex keys + %d Vertex-compat + %d OpenAI-compat)",
		total,
		authFiles,
		geminiAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		vertexAICompatCount,
		openAICompatCount,
	)
}

func (s *Server) SetWebsocketAuthChangeHandler(fn func(bool, bool)) {
	if s == nil {
		return
	}
	s.wsAuthChanged = fn
}
