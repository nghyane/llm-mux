// Package management provides the management API handlers and middleware
// for configuring the server and managing auth files.
package management

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/auth/login"
	"github.com/nghyane/llm-mux/internal/buildinfo"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/usage"
	"github.com/nghyane/llm-mux/internal/util"
)

type attemptInfo struct {
	count        int
	blockedUntil time.Time
}

// Handler aggregates config reference, persistence path and helpers.
type Handler struct {
	cfg                 *config.Config
	cfgMu               sync.RWMutex // protects cfg for concurrent read/write during hot-reload
	configFilePath      string
	mu                  sync.Mutex
	attemptsMu          sync.Mutex
	failedAttempts      map[string]*attemptInfo // keyed by client IP
	authManager         *provider.Manager
	usagePlugin         *usage.LoggerPlugin
	tokenStore          provider.Store
	localPassword       string
	allowRemoteOverride bool
	logDir              string
	httpClient          *http.Client
	httpClientOnce      sync.Once
}

// NewHandler creates a new management handler instance.
func NewHandler(cfg *config.Config, configFilePath string, manager *provider.Manager) *Handler {
	// Check if MANAGEMENT_PASSWORD env is set to enable remote override
	envSecret, _ := os.LookupEnv("MANAGEMENT_PASSWORD")
	envSecret = strings.TrimSpace(envSecret)

	return &Handler{
		cfg:                 cfg,
		configFilePath:      configFilePath,
		failedAttempts:      make(map[string]*attemptInfo),
		authManager:         manager,
		usagePlugin:         usage.GetLoggerPlugin(),
		tokenStore:          login.GetTokenStore(),
		allowRemoteOverride: envSecret != "",
	}
}

// SetConfig updates the in-memory config reference when the server hot-reloads.
func (h *Handler) SetConfig(cfg *config.Config) {
	h.cfgMu.Lock()
	h.cfg = cfg
	h.cfgMu.Unlock()
}

// getConfig returns the current config with proper synchronization.
func (h *Handler) getConfig() *config.Config {
	h.cfgMu.RLock()
	defer h.cfgMu.RUnlock()
	return h.cfg
}

func (h *Handler) getHTTPClient() *http.Client {
	h.httpClientOnce.Do(func() {
		h.httpClient = util.SetProxy(&h.cfg.SDKConfig, &http.Client{})
	})
	return h.httpClient
}

// SetAuthManager updates the auth manager reference used by management endpoints.
func (h *Handler) SetAuthManager(manager *provider.Manager) { h.authManager = manager }

// SetUsagePlugin allows replacing the usage plugin reference.
func (h *Handler) SetUsagePlugin(plugin *usage.LoggerPlugin) { h.usagePlugin = plugin }

// SetLocalPassword configures the runtime-local password accepted for localhost requests.
func (h *Handler) SetLocalPassword(password string) { h.localPassword = password }

// SetLogDirectory updates the directory where main.log should be looked up.
func (h *Handler) SetLogDirectory(dir string) {
	if dir == "" {
		return
	}
	if !filepath.IsAbs(dir) {
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	h.logDir = dir
}

// Middleware enforces access control for management endpoints.
// All requests (local and remote) require a valid management key.
// Additionally, remote access requires allow-remote-management=true.
// Key priority: MANAGEMENT_PASSWORD env > $XDG_CONFIG_HOME/llm-mux/credentials.json
func (h *Handler) Middleware() gin.HandlerFunc {
	const maxFailures = 5
	const banDuration = 30 * time.Minute

	return func(c *gin.Context) {
		c.Header("X-CPA-VERSION", buildinfo.Version)
		c.Header("X-CPA-COMMIT", buildinfo.Commit)
		c.Header("X-CPA-BUILD-DATE", buildinfo.BuildDate)

		clientIP := c.ClientIP()
		localClient := clientIP == "127.0.0.1" || clientIP == "::1"
		cfg := h.getConfig()
		var allowRemote bool
		if cfg != nil {
			allowRemote = cfg.RemoteManagement.AllowRemote
		}
		if h.allowRemoteOverride {
			allowRemote = true
		}

		// Get management key (XDG-compliant: $XDG_CONFIG_HOME/llm-mux/credentials.json)
		managementKey := config.GetManagementKey()

		fail := func() {}
		if !localClient {
			h.attemptsMu.Lock()
			ai := h.failedAttempts[clientIP]
			if ai != nil {
				if !ai.blockedUntil.IsZero() {
					if time.Now().Before(ai.blockedUntil) {
						remaining := time.Until(ai.blockedUntil).Round(time.Second)
						h.attemptsMu.Unlock()
						respondError(c, http.StatusForbidden, ErrCodeForbidden, fmt.Sprintf("IP banned due to too many failed attempts. Try again in %s", remaining))
						c.Abort()
						return
					}
					// Ban expired, reset state
					ai.blockedUntil = time.Time{}
					ai.count = 0
				}
			}
			h.attemptsMu.Unlock()

			if !allowRemote {
				respondError(c, http.StatusForbidden, ErrCodeForbidden, "remote management disabled")
				c.Abort()
				return
			}

			fail = func() {
				h.attemptsMu.Lock()
				aip := h.failedAttempts[clientIP]
				if aip == nil {
					aip = &attemptInfo{}
					h.failedAttempts[clientIP] = aip
				}
				aip.count++
				if aip.count >= maxFailures {
					aip.blockedUntil = time.Now().Add(banDuration)
					aip.count = 0
				}
				h.attemptsMu.Unlock()
			}
		}

		// Check if management key is configured
		if managementKey == "" {
			respondError(c, http.StatusForbidden, ErrCodeForbidden, "management key not configured")
			c.Abort()
			return
		}

		// Accept either Authorization: Bearer <key> or X-Management-Key
		var provided string
		if ah := c.GetHeader("Authorization"); ah != "" {
			parts := strings.SplitN(ah, " ", 2)
			if len(parts) == 2 && strings.ToLower(parts[0]) == "bearer" {
				provided = parts[1]
			} else {
				provided = ah
			}
		}
		if provided == "" {
			provided = c.GetHeader("X-Management-Key")
		}

		if provided == "" {
			if !localClient {
				fail()
			}
			respondUnauthorized(c, "missing management key")
			c.Abort()
			return
		}

		// For localhost, also accept the runtime local password
		if localClient {
			if lp := h.localPassword; lp != "" {
				if subtle.ConstantTimeCompare([]byte(provided), []byte(lp)) == 1 {
					c.Next()
					return
				}
			}
		}

		// Validate against management key using constant-time comparison
		if subtle.ConstantTimeCompare([]byte(provided), []byte(managementKey)) != 1 {
			if !localClient {
				fail()
			}
			respondUnauthorized(c, "invalid management key")
			c.Abort()
			return
		}

		// Reset failed attempts on success
		if !localClient {
			h.attemptsMu.Lock()
			if ai := h.failedAttempts[clientIP]; ai != nil {
				ai.count = 0
				ai.blockedUntil = time.Time{}
			}
			h.attemptsMu.Unlock()
		}

		c.Next()
	}
}

// persist saves the current in-memory config to disk and sends JSON response.
func (h *Handler) persist(c *gin.Context) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	// Preserve comments when writing
	cfg := h.getConfig()
	if err := config.SaveConfigPreserveComments(h.configFilePath, cfg); err != nil {
		respondError(c, http.StatusInternalServerError, ErrCodeWriteFailed, fmt.Sprintf("failed to save config: %v", err))
		return false
	}
	respondOK(c, gin.H{"status": "ok"})
	return true
}

// persistSilent saves the current in-memory config to disk without sending response.
// Returns true on success, false on failure.
func (h *Handler) persistSilent() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	cfg := h.getConfig()
	return config.SaveConfigPreserveComments(h.configFilePath, cfg) == nil
}

func (h *Handler) bindBoolValue(c *gin.Context) (bool, bool) {
	var body struct {
		Value *bool `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		var m map[string]any
		if err2 := c.ShouldBindJSON(&m); err2 == nil {
			for _, v := range m {
				if b, ok := v.(bool); ok {
					return b, true
				}
			}
		}
		respondBadRequest(c, "invalid body: expected {\"value\": bool}")
		return false, false
	}
	return *body.Value, true
}

func (h *Handler) bindIntValue(c *gin.Context) (int, bool) {
	var body struct {
		Value *int `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		respondBadRequest(c, "invalid body: expected {\"value\": int}")
		return 0, false
	}
	return *body.Value, true
}

func (h *Handler) bindStringValue(c *gin.Context) (string, bool) {
	var body struct {
		Value *string `json:"value"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.Value == nil {
		respondBadRequest(c, "invalid body: expected {\"value\": string}")
		return "", false
	}
	return *body.Value, true
}
