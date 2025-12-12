// Package api provides the HTTP API server implementation for the CLI Proxy API.
package api

import (
	"context"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/managementasset"
	log "github.com/sirupsen/logrus"
)

// registerManagementRoutes sets up all management API routes.
// These routes are registered lazily when a management secret is configured.
// This method can be safely called multiple times - subsequent calls are idempotent.
func (s *Server) registerManagementRoutes() {
	if s == nil || s.engine == nil || s.mgmt == nil {
		return
	}
	if !s.managementRoutesRegistered.CompareAndSwap(false, true) {
		return
	}

	log.Info("management routes registered after secret key configuration")

	mgmt := s.engine.Group("/v0/management")
	mgmt.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	{
		mgmt.GET("/usage", s.mgmt.GetUsageStatistics)
		mgmt.GET("/config", s.mgmt.GetConfig)
		mgmt.GET("/config.yaml", s.mgmt.GetConfigYAML)
		mgmt.PUT("/config.yaml", s.mgmt.PutConfigYAML)
		mgmt.GET("/latest-version", s.mgmt.GetLatestVersion)

		mgmt.GET("/debug", s.mgmt.GetDebug)
		mgmt.PUT("/debug", s.mgmt.PutDebug)
		mgmt.PATCH("/debug", s.mgmt.PutDebug)

		mgmt.GET("/logging-to-file", s.mgmt.GetLoggingToFile)
		mgmt.PUT("/logging-to-file", s.mgmt.PutLoggingToFile)
		mgmt.PATCH("/logging-to-file", s.mgmt.PutLoggingToFile)

		mgmt.GET("/usage-statistics-enabled", s.mgmt.GetUsageStatisticsEnabled)
		mgmt.PUT("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)
		mgmt.PATCH("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)

		mgmt.GET("/proxy-url", s.mgmt.GetProxyURL)
		mgmt.PUT("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.PATCH("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.DELETE("/proxy-url", s.mgmt.DeleteProxyURL)

		mgmt.GET("/quota-exceeded/switch-project", s.mgmt.GetSwitchProject)
		mgmt.PUT("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)
		mgmt.PATCH("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)

		mgmt.GET("/quota-exceeded/switch-preview-model", s.mgmt.GetSwitchPreviewModel)
		mgmt.PUT("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.PATCH("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)

		mgmt.GET("/api-keys", s.mgmt.GetAPIKeys)
		mgmt.PUT("/api-keys", s.mgmt.PutAPIKeys)
		mgmt.PATCH("/api-keys", s.mgmt.PatchAPIKeys)
		mgmt.DELETE("/api-keys", s.mgmt.DeleteAPIKeys)

		mgmt.GET("/gemini-api-key", s.mgmt.GetGeminiKeys)
		mgmt.PUT("/gemini-api-key", s.mgmt.PutGeminiKeys)
		mgmt.PATCH("/gemini-api-key", s.mgmt.PatchGeminiKey)
		mgmt.DELETE("/gemini-api-key", s.mgmt.DeleteGeminiKey)

		mgmt.GET("/logs", s.mgmt.GetLogs)
		mgmt.DELETE("/logs", s.mgmt.DeleteLogs)
		mgmt.GET("/request-error-logs", s.mgmt.GetRequestErrorLogs)
		mgmt.GET("/request-error-logs/:name", s.mgmt.DownloadRequestErrorLog)
		mgmt.GET("/request-log", s.mgmt.GetRequestLog)
		mgmt.PUT("/request-log", s.mgmt.PutRequestLog)
		mgmt.PATCH("/request-log", s.mgmt.PutRequestLog)
		mgmt.GET("/ws-auth", s.mgmt.GetWebsocketAuth)
		mgmt.PUT("/ws-auth", s.mgmt.PutWebsocketAuth)
		mgmt.PATCH("/ws-auth", s.mgmt.PutWebsocketAuth)

		mgmt.GET("/request-retry", s.mgmt.GetRequestRetry)
		mgmt.PUT("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.PATCH("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.GET("/max-retry-interval", s.mgmt.GetMaxRetryInterval)
		mgmt.PUT("/max-retry-interval", s.mgmt.PutMaxRetryInterval)
		mgmt.PATCH("/max-retry-interval", s.mgmt.PutMaxRetryInterval)

		mgmt.GET("/claude-api-key", s.mgmt.GetClaudeKeys)
		mgmt.PUT("/claude-api-key", s.mgmt.PutClaudeKeys)
		mgmt.PATCH("/claude-api-key", s.mgmt.PatchClaudeKey)
		mgmt.DELETE("/claude-api-key", s.mgmt.DeleteClaudeKey)

		mgmt.GET("/codex-api-key", s.mgmt.GetCodexKeys)
		mgmt.PUT("/codex-api-key", s.mgmt.PutCodexKeys)
		mgmt.PATCH("/codex-api-key", s.mgmt.PatchCodexKey)
		mgmt.DELETE("/codex-api-key", s.mgmt.DeleteCodexKey)

		mgmt.GET("/openai-compatibility", s.mgmt.GetOpenAICompat)
		mgmt.PUT("/openai-compatibility", s.mgmt.PutOpenAICompat)
		mgmt.PATCH("/openai-compatibility", s.mgmt.PatchOpenAICompat)
		mgmt.DELETE("/openai-compatibility", s.mgmt.DeleteOpenAICompat)

		mgmt.GET("/oauth-excluded-models", s.mgmt.GetOAuthExcludedModels)
		mgmt.PUT("/oauth-excluded-models", s.mgmt.PutOAuthExcludedModels)
		mgmt.PATCH("/oauth-excluded-models", s.mgmt.PatchOAuthExcludedModels)
		mgmt.DELETE("/oauth-excluded-models", s.mgmt.DeleteOAuthExcludedModels)

		mgmt.GET("/auth-files", s.mgmt.ListAuthFiles)
		mgmt.GET("/auth-files/download", s.mgmt.DownloadAuthFile)
		mgmt.POST("/auth-files", s.mgmt.UploadAuthFile)
		mgmt.DELETE("/auth-files", s.mgmt.DeleteAuthFile)
		mgmt.POST("/vertex/import", s.mgmt.ImportVertexCredential)

		// Unified OAuth API endpoints
		mgmt.POST("/oauth/start", s.mgmt.OAuthStart)
		mgmt.GET("/oauth/status/:state", s.mgmt.OAuthStatus)
		mgmt.POST("/oauth/cancel/:state", s.mgmt.OAuthCancel)
	}
}

// serveManagementControlPanel serves the management control panel HTML.
// It attempts to serve from disk first, then falls back to embedded HTML.
// If neither is available, it attempts to download the latest version.
func (s *Server) serveManagementControlPanel(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	// Try to serve from disk first (allows updates without rebuilding)
	filePath := managementasset.FilePath(s.configFilePath)
	if filePath != "" {
		if _, err := os.Stat(filePath); err == nil {
			c.File(filePath)
			return
		}
	}

	// Fallback to embedded HTML
	if managementasset.HasEmbeddedHTML() {
		c.Data(http.StatusOK, "text/html; charset=utf-8", managementasset.GetEmbeddedHTML())
		return
	}

	// Try to download if neither disk nor embedded available
	if filePath != "" {
		go managementasset.EnsureLatestManagementHTML(context.Background(), managementasset.StaticDir(s.configFilePath), cfg.ProxyURL)
	}
	c.AbortWithStatus(http.StatusNotFound)
}
