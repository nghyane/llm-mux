package oauth

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
)

// ProviderConfig defines the callback configuration for an OAuth provider.
type ProviderConfig struct {
	Name         string // Provider identifier (claude, gemini, codex, etc.)
	Port         int    // Fixed callback port registered with OAuth provider
	CallbackPath string // Path component of callback URL
}

// Pre-defined provider configurations with fixed ports.
// These CANNOT be changed - they are registered with OAuth providers.
var ProviderConfigs = map[string]ProviderConfig{
	"claude": {
		Name:         "claude",
		Port:         54545,
		CallbackPath: "/callback",
	},
	"anthropic": {
		Name:         "anthropic",
		Port:         54545,
		CallbackPath: "/callback",
	},
	"codex": {
		Name:         "codex",
		Port:         1455,
		CallbackPath: "/auth/callback",
	},
	"gemini": {
		Name:         "gemini",
		Port:         8085,
		CallbackPath: "/oauth2callback",
	},
	"gemini-cli": {
		Name:         "gemini-cli",
		Port:         8085,
		CallbackPath: "/oauth2callback",
	},
	"iflow": {
		Name:         "iflow",
		Port:         11451,
		CallbackPath: "/oauth2callback",
	},
	"antigravity": {
		Name:         "antigravity",
		Port:         51121,
		CallbackPath: "/oauth-callback",
	},
}

// CallbackServer represents a persistent HTTP server listening for OAuth callbacks.
type CallbackServer struct {
	port     int
	server   *http.Server
	listener net.Listener
	done     chan struct{}
	running  bool
}

// CallbackServersManager manages multiple callback servers for different providers.
type CallbackServersManager struct {
	mu       sync.RWMutex
	servers  map[int]*CallbackServer // keyed by port
	registry *Registry
	handler  CallbackHandler
}

// CallbackHandler is called when an OAuth callback is received.
type CallbackHandler func(provider, code, state, errStr string) string

// NewCallbackServersManager creates a new manager for callback servers.
// If no registry or handler provided, uses defaults suitable for forwarder mode.
func NewCallbackServersManager(args ...any) *CallbackServersManager {
	mgr := &CallbackServersManager{
		servers: make(map[int]*CallbackServer),
	}
	// Parse optional arguments for backward compatibility
	for _, arg := range args {
		switch v := arg.(type) {
		case *Registry:
			mgr.registry = v
		case CallbackHandler:
			mgr.handler = v
		}
	}
	return mgr
}

// EnsureRunning ensures callback servers are running for the specified providers.
// Servers are started on-demand and stay running until explicitly stopped.
func (m *CallbackServersManager) EnsureRunning(providers ...string) error {
	portsNeeded := make(map[int][]string) // port -> provider names

	for _, provider := range providers {
		config, ok := ProviderConfigs[provider]
		if !ok {
			continue
		}
		portsNeeded[config.Port] = append(portsNeeded[config.Port], provider)
	}

	for port, providerNames := range portsNeeded {
		if err := m.ensureServerRunning(port, providerNames); err != nil {
			return fmt.Errorf("failed to start callback server on port %d: %w", port, err)
		}
	}

	return nil
}

// ensureServerRunning starts a callback server on the specified port if not already running.
func (m *CallbackServersManager) ensureServerRunning(port int, providers []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if already running
	if server, exists := m.servers[port]; exists && server.running {
		return nil
	}

	// Create listener
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	// Create HTTP handler that routes all paths to our callback handler
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("code")
		state := r.URL.Query().Get("state")
		errStr := r.URL.Query().Get("error")

		// Determine provider from port
		provider := m.getProviderForPort(port)

		// Call the handler and get HTML response
		html := m.handler(provider, code, state, errStr)

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte(html))
	})

	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	done := make(chan struct{})

	// Start server in background
	go func() {
		log.Infof("OAuth callback server started on %s for providers: %v", addr, providers)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Errorf("OAuth callback server on port %d error: %v", port, err)
		}
		close(done)
	}()

	m.servers[port] = &CallbackServer{
		port:     port,
		server:   srv,
		listener: ln,
		done:     done,
		running:  true,
	}

	return nil
}

// getProviderForPort returns the primary provider name for a given port.
func (m *CallbackServersManager) getProviderForPort(port int) string {
	for name, config := range ProviderConfigs {
		if config.Port == port {
			return name
		}
	}
	return "unknown"
}

// Stop gracefully stops a callback server on the specified port.
// Copies server reference under lock to prevent TOCTOU race.
func (m *CallbackServersManager) Stop(port int) error {
	m.mu.Lock()
	server, exists := m.servers[port]
	if !exists || !server.running {
		m.mu.Unlock()
		return nil
	}
	server.running = false
	// Copy reference before releasing lock
	serverToStop := server
	m.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := serverToStop.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("failed to shutdown callback server on port %d: %w", port, err)
	}

	<-serverToStop.done
	log.Infof("OAuth callback server on port %d stopped", port)

	m.mu.Lock()
	delete(m.servers, port)
	m.mu.Unlock()

	return nil
}

// StopAll gracefully stops all running callback servers.
func (m *CallbackServersManager) StopAll() {
	m.mu.RLock()
	ports := make([]int, 0, len(m.servers))
	for port := range m.servers {
		ports = append(ports, port)
	}
	m.mu.RUnlock()

	for _, port := range ports {
		if err := m.Stop(port); err != nil {
			log.Warnf("Error stopping callback server on port %d: %v", port, err)
		}
	}
}

// IsRunning checks if a callback server is running on the specified port.
func (m *CallbackServersManager) IsRunning(port int) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	server, exists := m.servers[port]
	return exists && server.running
}

// RunningPorts returns a list of ports with running callback servers.
func (m *CallbackServersManager) RunningPorts() []int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	ports := make([]int, 0, len(m.servers))
	for port, server := range m.servers {
		if server.running {
			ports = append(ports, port)
		}
	}
	return ports
}

// GetRedirectURI returns the full redirect URI for a provider.
func GetRedirectURI(provider string) string {
	config, ok := ProviderConfigs[provider]
	if !ok {
		return ""
	}
	return fmt.Sprintf("http://localhost:%d%s", config.Port, config.CallbackPath)
}

// GetCallbackPort returns the callback port for a provider.
func GetCallbackPort(provider string) int {
	config, ok := ProviderConfigs[provider]
	if !ok {
		return 0
	}
	return config.Port
}

// ForwarderServer represents a temporary HTTP server that forwards OAuth callbacks.
type ForwarderServer struct {
	port     int
	provider string
	server   *http.Server
	done     chan struct{}
}

// forwarders holds active forwarder servers (separate from persistent callback servers).
var (
	forwardersMu sync.Mutex
	forwarders   = make(map[int]*ForwarderServer)
)

// StartForwarder starts a temporary HTTP server that redirects OAuth callbacks to the target URL.
// This is used for WebUI mode where callbacks need to be forwarded to the main server port.
// Returns the forwarder instance and any error.
func (m *CallbackServersManager) StartForwarder(port int, provider, targetBase string) (*ForwarderServer, error) {
	forwardersMu.Lock()
	prev := forwarders[port]
	if prev != nil {
		delete(forwarders, port)
	}
	forwardersMu.Unlock()

	if prev != nil {
		m.stopForwarderInstance(prev)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("failed to listen on %s: %w", addr, err)
	}

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target := targetBase
		if raw := r.URL.RawQuery; raw != "" {
			if len(target) > 0 && target[len(target)-1] == '?' {
				target = target + raw
			} else if strings.ContainsRune(target, '?') {
				target = target + "&" + raw
			} else {
				target = target + "?" + raw
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		http.Redirect(w, r, target, http.StatusFound)
	})

	srv := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      5 * time.Second,
	}
	done := make(chan struct{})

	go func() {
		if errServe := srv.Serve(ln); errServe != nil && errServe != http.ErrServerClosed {
			log.WithError(errServe).Warnf("callback forwarder for %s stopped unexpectedly", provider)
		}
		close(done)
	}()

	forwarder := &ForwarderServer{
		port:     port,
		provider: provider,
		server:   srv,
		done:     done,
	}

	forwardersMu.Lock()
	forwarders[port] = forwarder
	forwardersMu.Unlock()

	log.Infof("callback forwarder for %s listening on %s", provider, addr)

	return forwarder, nil
}

// StopForwarder stops a callback forwarder on the given port.
func (m *CallbackServersManager) StopForwarder(port int) {
	forwardersMu.Lock()
	forwarder := forwarders[port]
	if forwarder != nil {
		delete(forwarders, port)
	}
	forwardersMu.Unlock()

	m.stopForwarderInstance(forwarder)
}

// stopForwarderInstance gracefully stops a forwarder server.
func (m *CallbackServersManager) stopForwarderInstance(forwarder *ForwarderServer) {
	if forwarder == nil || forwarder.server == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := forwarder.server.Shutdown(ctx); err != nil && err != http.ErrServerClosed {
		log.WithError(err).Warnf("failed to shut down callback forwarder on port %d", forwarder.port)
	}

	select {
	case <-forwarder.done:
	case <-time.After(2 * time.Second):
	}

	log.Infof("callback forwarder on port %d stopped", forwarder.port)
}
