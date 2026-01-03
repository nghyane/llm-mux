package management

import (
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/usage"
	"github.com/nghyane/llm-mux/internal/util"
	"gopkg.in/yaml.v3"
)

const (
	latestReleaseURL       = "https://api.github.com/repos/nghyane/llm-mux/releases/latest"
	latestReleaseUserAgent = "llm-mux"
	latestVersionCacheTTL  = 15 * time.Minute
)

// Cache for latest version to avoid hitting GitHub rate limits
var (
	latestVersionCache   string
	latestVersionCacheAt time.Time
	latestVersionMu      sync.RWMutex
)

func (h *Handler) GetConfig(c *gin.Context) {
	cfg := h.getConfig()
	if cfg == nil {
		respondOK(c, gin.H{})
		return
	}
	h.cfgMu.RLock()
	cfgCopy := *cfg
	h.cfgMu.RUnlock()
	respondOK(c, &cfgCopy)
}

type releaseInfo struct {
	TagName string `json:"tag_name"`
	Name    string `json:"name"`
}

// GetLatestVersion returns the latest release version from GitHub without downloading assets.
// Results are cached for 15 minutes to avoid hitting GitHub rate limits.
func (h *Handler) GetLatestVersion(c *gin.Context) {
	// Check cache first
	latestVersionMu.RLock()
	if latestVersionCache != "" && time.Since(latestVersionCacheAt) < latestVersionCacheTTL {
		cached := latestVersionCache
		latestVersionMu.RUnlock()
		respondOK(c, gin.H{"latest-version": cached, "cached": true})
		return
	}
	latestVersionMu.RUnlock()

	// Fetch from GitHub
	version, err := h.fetchLatestVersion(c)
	if err != nil {
		// Return cached version if available, even if expired
		latestVersionMu.RLock()
		if latestVersionCache != "" {
			cached := latestVersionCache
			latestVersionMu.RUnlock()
			respondOK(c, gin.H{"latest-version": cached, "cached": true, "stale": true})
			return
		}
		latestVersionMu.RUnlock()
		respondError(c, http.StatusBadGateway, ErrCodeInternalError, err.Error())
		return
	}

	// Update cache
	latestVersionMu.Lock()
	latestVersionCache = version
	latestVersionCacheAt = time.Now()
	latestVersionMu.Unlock()

	respondOK(c, gin.H{"latest-version": version})
}

func (h *Handler) fetchLatestVersion(c *gin.Context) (string, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	proxyURL := ""
	if cfg := h.getConfig(); cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	if proxyURL != "" {
		sdkCfg := &config.SDKConfig{ProxyURL: proxyURL}
		util.SetProxy(sdkCfg, client)
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, latestReleaseURL, nil)
	if err != nil {
		return "", fmt.Errorf("request_create_failed: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", latestReleaseUserAgent)
	if token := os.Getenv("GITHUB_TOKEN"); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("request_failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var info releaseInfo
	if errDecode := json.NewDecoder(resp.Body).Decode(&info); errDecode != nil {
		return "", fmt.Errorf("decode_failed: %w", errDecode)
	}

	version := strings.TrimSpace(info.TagName)
	if version == "" {
		version = strings.TrimSpace(info.Name)
	}
	if version == "" {
		return "", fmt.Errorf("missing release version")
	}

	return version, nil
}

func WriteConfig(path string, data []byte) error {
	data = config.NormalizeCommentIndentation(data)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if _, errWrite := f.Write(data); errWrite != nil {
		_ = f.Close()
		return errWrite
	}
	if errSync := f.Sync(); errSync != nil {
		_ = f.Close()
		return errSync
	}
	return f.Close()
}

func (h *Handler) PutConfigYAML(c *gin.Context) {
	// Limit request body to 10MB to prevent DoS attacks
	const maxConfigSize = 10 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxConfigSize))
	if err != nil {
		respondError(c, http.StatusBadRequest, ErrCodeInvalidRequest, "cannot read request body")
		return
	}
	var cfg config.Config
	if err = yaml.Unmarshal(body, &cfg); err != nil {
		respondError(c, http.StatusBadRequest, ErrCodeInvalidRequest, err.Error())
		return
	}
	// Validate config using LoadConfigOptional with optional=false to enforce parsing
	tmpDir := filepath.Dir(h.configFilePath)
	tmpFile, err := os.CreateTemp(tmpDir, "config-validate-*.yaml")
	if err != nil {
		respondError(c, http.StatusInternalServerError, ErrCodeWriteFailed, err.Error())
		return
	}
	tempFile := tmpFile.Name()
	if _, errWrite := tmpFile.Write(body); errWrite != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tempFile)
		respondError(c, http.StatusInternalServerError, ErrCodeWriteFailed, errWrite.Error())
		return
	}
	if errClose := tmpFile.Close(); errClose != nil {
		_ = os.Remove(tempFile)
		respondError(c, http.StatusInternalServerError, ErrCodeWriteFailed, errClose.Error())
		return
	}
	defer func() {
		_ = os.Remove(tempFile)
	}()
	_, err = config.LoadConfigOptional(tempFile, false)
	if err != nil {
		respondError(c, http.StatusUnprocessableEntity, ErrCodeInvalidConfig, err.Error())
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if WriteConfig(h.configFilePath, body) != nil {
		respondError(c, http.StatusInternalServerError, ErrCodeWriteFailed, "failed to write config")
		return
	}
	// Reload into handler to keep memory in sync
	newCfg, err := config.LoadConfig(h.configFilePath)
	if err != nil {
		respondError(c, http.StatusInternalServerError, ErrCodeReloadFailed, err.Error())
		return
	}
	h.cfgMu.Lock()
	h.cfg = newCfg
	h.cfgMu.Unlock()
	respondOK(c, gin.H{"ok": true, "changed": []string{"config"}})
}

// GetConfigYAML returns the raw config.yaml file bytes without re-encoding.
// It preserves comments and original formatting/styles.
func (h *Handler) GetConfigYAML(c *gin.Context) {
	data, err := os.ReadFile(h.configFilePath)
	if err != nil {
		if os.IsNotExist(err) {
			respondNotFound(c, "config file not found")
			return
		}
		respondInternalError(c, err.Error())
		return
	}
	c.Header("Content-Type", "application/yaml; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	// Write raw bytes as-is
	_, _ = c.Writer.Write(data)
}

func (h *Handler) GetDebug(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"debug": cfg.Debug})
}
func (h *Handler) PutDebug(c *gin.Context) {
	value, ok := h.bindBoolValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	h.cfg.Debug = value
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"debug": cfg.Debug})
}

// UsageStatisticsEnabled (mapped to Usage.DSN != "")
func (h *Handler) GetUsageStatisticsEnabled(c *gin.Context) {
	cfg := h.getConfig()
	enabled := cfg.Usage.DSN != ""
	respondOK(c, gin.H{"usage-statistics-enabled": enabled})
}
func (h *Handler) PutUsageStatisticsEnabled(c *gin.Context) {
	value, ok := h.bindBoolValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	if value {
		if h.cfg.Usage.DSN == "" {
			h.cfg.Usage.DSN = "sqlite://~/.config/llm-mux/usage.db"
		}
	} else {
		h.cfg.Usage.DSN = ""
	}
	h.cfgMu.Unlock()
	usage.SetStatisticsEnabled(value)
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"usage-statistics-enabled": cfg.Usage.DSN != ""})
}

// LoggingToFile
func (h *Handler) GetLoggingToFile(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"logging-to-file": cfg.LoggingToFile})
}
func (h *Handler) PutLoggingToFile(c *gin.Context) {
	value, ok := h.bindBoolValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	h.cfg.LoggingToFile = value
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"logging-to-file": cfg.LoggingToFile})
}

// Request log
func (h *Handler) GetRequestLog(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"request-log": cfg.RequestLog})
}
func (h *Handler) PutRequestLog(c *gin.Context) {
	value, ok := h.bindBoolValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	h.cfg.RequestLog = value
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"request-log": cfg.RequestLog})
}

// Websocket auth
func (h *Handler) GetWebsocketAuth(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"ws-auth": cfg.WebsocketAuth})
}
func (h *Handler) PutWebsocketAuth(c *gin.Context) {
	value, ok := h.bindBoolValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	h.cfg.WebsocketAuth = value
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"ws-auth": cfg.WebsocketAuth})
}

// Request retry
func (h *Handler) GetRequestRetry(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"request-retry": cfg.RequestRetry})
}
func (h *Handler) PutRequestRetry(c *gin.Context) {
	value, ok := h.bindIntValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	h.cfg.RequestRetry = value
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"request-retry": cfg.RequestRetry})
}

// Max retry interval
func (h *Handler) GetMaxRetryInterval(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"max-retry-interval": cfg.MaxRetryInterval})
}
func (h *Handler) PutMaxRetryInterval(c *gin.Context) {
	value, ok := h.bindIntValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	h.cfg.MaxRetryInterval = value
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"max-retry-interval": cfg.MaxRetryInterval})
}

// Proxy URL
func (h *Handler) GetProxyURL(c *gin.Context) {
	cfg := h.getConfig()
	respondOK(c, gin.H{"proxy-url": cfg.ProxyURL})
}
func (h *Handler) PutProxyURL(c *gin.Context) {
	value, ok := h.bindStringValue(c)
	if !ok {
		return
	}
	h.cfgMu.Lock()
	h.cfg.ProxyURL = value
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"proxy-url": cfg.ProxyURL})
}
func (h *Handler) DeleteProxyURL(c *gin.Context) {
	h.cfgMu.Lock()
	h.cfg.ProxyURL = ""
	h.cfgMu.Unlock()
	if !h.persistSilent() {
		respondInternalError(c, "failed to save config")
		return
	}
	cfg := h.getConfig()
	respondOK(c, gin.H{"proxy-url": cfg.ProxyURL})
}
