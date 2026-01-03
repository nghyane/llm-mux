// Package service provides the core service implementation for the CLI Proxy API.
// It includes service lifecycle management, authentication handling, file watching,
// and integration with various AI service providers through a unified interface.
package service

import (
	"fmt"

	"github.com/nghyane/llm-mux/internal/access"
	"github.com/nghyane/llm-mux/internal/api"
	"github.com/nghyane/llm-mux/internal/auth/login"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/usage"
)

// Builder constructs a Service instance with customizable providers.
type Builder struct {
	cfg            *config.Config
	configPath     string
	tokenProvider  TokenClientProvider
	apiKeyProvider APIKeyClientProvider
	watcherFactory WatcherFactory
	hooks          Hooks
	authManager    *login.Manager
	accessManager  *access.Manager
	coreManager    *provider.Manager
	serverOptions  []api.ServerOption
}

// Hooks allows callers to plug into service lifecycle stages.
// These callbacks provide opportunities to perform custom initialization
// and cleanup operations during service startup and shutdown.
type Hooks struct {
	// OnBeforeStart is called before the service starts, allowing configuration
	// modifications or additional setup.
	OnBeforeStart func(*config.Config)

	// OnAfterStart is called after the service has started successfully,
	// providing access to the service instance for additional operations.
	OnAfterStart func(*Service)
}

// NewBuilder creates a Builder with default dependencies left unset.
// Use the fluent interface methods to configure the service before calling Build().
//
// Returns:
//   - *Builder: A new builder instance ready for configuration
func NewBuilder() *Builder {
	return &Builder{}
}

// WithConfig sets the configuration instance used by the service.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *Builder: The builder instance for method chaining
func (b *Builder) WithConfig(cfg *config.Config) *Builder {
	b.cfg = cfg
	return b
}

// WithConfigPath sets the absolute configuration file path used for reload watching.
//
// Parameters:
//   - path: The absolute path to the configuration file
//
// Returns:
//   - *Builder: The builder instance for method chaining
func (b *Builder) WithConfigPath(path string) *Builder {
	b.configPath = path
	return b
}

// WithTokenClientProvider overrides the provider responsible for token-backed clients.
func (b *Builder) WithTokenClientProvider(provider TokenClientProvider) *Builder {
	b.tokenProvider = provider
	return b
}

// WithAPIKeyClientProvider overrides the provider responsible for API key-backed clients.
func (b *Builder) WithAPIKeyClientProvider(provider APIKeyClientProvider) *Builder {
	b.apiKeyProvider = provider
	return b
}

// WithWatcherFactory allows customizing the watcher factory that handles reloads.
func (b *Builder) WithWatcherFactory(factory WatcherFactory) *Builder {
	b.watcherFactory = factory
	return b
}

// WithHooks registers lifecycle hooks executed around service startup.
func (b *Builder) WithHooks(h Hooks) *Builder {
	b.hooks = h
	return b
}

// WithAuthManager overrides the authentication manager used for token lifecycle operations.
func (b *Builder) WithAuthManager(mgr *login.Manager) *Builder {
	b.authManager = mgr
	return b
}

// WithRequestAccessManager overrides the request authentication manager.
func (b *Builder) WithRequestAccessManager(mgr *access.Manager) *Builder {
	b.accessManager = mgr
	return b
}

// WithCoreAuthManager overrides the runtime auth manager responsible for request execution.
func (b *Builder) WithCoreAuthManager(mgr *provider.Manager) *Builder {
	b.coreManager = mgr
	return b
}

// WithServerOptions appends server configuration options used during construction.
func (b *Builder) WithServerOptions(opts ...api.ServerOption) *Builder {
	b.serverOptions = append(b.serverOptions, opts...)
	return b
}

// WithLocalManagementPassword configures a password that is only accepted from localhost management requests.
func (b *Builder) WithLocalManagementPassword(password string) *Builder {
	if password == "" {
		return b
	}
	b.serverOptions = append(b.serverOptions, api.WithLocalManagementPassword(password))
	return b
}

// Build validates inputs, applies defaults, and returns a ready-to-run service.
func (b *Builder) Build() (*Service, error) {
	if b.cfg == nil {
		return nil, fmt.Errorf("cliproxy: configuration is required")
	}
	if b.configPath == "" {
		return nil, fmt.Errorf("cliproxy: configuration path is required")
	}

	tokenProvider := b.tokenProvider
	if tokenProvider == nil {
		tokenProvider = NewFileTokenClientProvider()
	}

	apiKeyProvider := b.apiKeyProvider
	if apiKeyProvider == nil {
		apiKeyProvider = NewAPIKeyClientProvider()
	}

	watcherFactory := b.watcherFactory
	if watcherFactory == nil {
		watcherFactory = defaultWatcherFactory
	}

	authManager := b.authManager
	if authManager == nil {
		authManager = newDefaultAuthManager()
	}

	accessManager := b.accessManager
	if accessManager == nil {
		accessManager = access.NewManager()
	}

	providers, err := access.BuildProviders(&b.cfg.SDKConfig)
	if err != nil {
		return nil, err
	}
	accessManager.SetProviders(providers)

	coreManager := b.coreManager
	if coreManager == nil {
		tokenStore := login.GetTokenStore()
		if dirSetter, ok := tokenStore.(interface{ SetBaseDir(string) }); ok && b.cfg != nil {
			dirSetter.SetBaseDir(b.cfg.AuthDir)
		}
		coreManager = provider.NewManager(tokenStore, nil, nil)
	}
	// Attach a default RoundTripper provider so providers can opt-in per-auth transports.
	coreManager.SetRoundTripperProvider(newDefaultRoundTripperProvider())

	// Register quota sync plugin if QuotaManager is active
	if qm := coreManager.GetQuotaManager(); qm != nil {
		plugin := provider.NewQuotaSyncPlugin(qm)
		usage.RegisterPlugin(plugin)
	}

	service := &Service{
		cfg:            b.cfg,
		configPath:     b.configPath,
		tokenProvider:  tokenProvider,
		apiKeyProvider: apiKeyProvider,
		watcherFactory: watcherFactory,
		hooks:          b.hooks,
		authManager:    authManager,
		accessManager:  accessManager,
		coreManager:    coreManager,
		serverOptions:  append([]api.ServerOption(nil), b.serverOptions...),
	}
	return service, nil
}
