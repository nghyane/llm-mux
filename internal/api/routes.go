// Package api provides the HTTP API server implementation for the CLI Proxy API.
package api

import (
	"errors"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/access"
	"github.com/nghyane/llm-mux/internal/api/handlers/format/claude"
	"github.com/nghyane/llm-mux/internal/api/handlers/format/gemini"
	"github.com/nghyane/llm-mux/internal/api/handlers/format/ollama"
	"github.com/nghyane/llm-mux/internal/api/handlers/format/openai"
	"github.com/nghyane/llm-mux/internal/api/middleware"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/oauth"
)

// setupRoutes configures the API routes for the server.
// It defines the endpoints and associates them with their respective handlers.
func (s *Server) setupRoutes() {
	openaiHandlers := openai.NewOpenAIAPIHandler(s.handlers)
	geminiHandlers := gemini.NewGeminiAPIHandler(s.handlers)
	geminiCLIHandlers := gemini.NewGeminiCLIAPIHandler(s.handlers)
	claudeCodeHandlers := claude.NewClaudeCodeAPIHandler(s.handlers)
	openaiResponsesHandlers := openai.NewOpenAIResponsesAPIHandler(s.handlers)
	ollamaHandlers := ollama.NewOllamaAPIHandler(s.handlers)

	// OpenAI compatible API routes
	v1 := s.engine.Group("/v1")
	v1.Use(middleware.RequestSizeLimitMiddleware(s.cfg.MaxRequestSize))
	v1.Use(s.conditionalAuthMiddleware())
	{
		v1.GET("/models", s.unifiedModelsHandler(openaiHandlers, claudeCodeHandlers))
		v1.POST("/chat/completions", openaiHandlers.ChatCompletions)
		v1.POST("/completions", openaiHandlers.Completions)
		v1.POST("/messages", claudeCodeHandlers.ClaudeMessages)
		v1.POST("/messages/count_tokens", claudeCodeHandlers.ClaudeCountTokens)
		v1.POST("/responses", openaiResponsesHandlers.Responses)
	}

	// Gemini compatible API routes
	v1beta := s.engine.Group("/v1beta")
	v1beta.Use(middleware.RequestSizeLimitMiddleware(s.cfg.MaxRequestSize))
	v1beta.Use(s.conditionalAuthMiddleware())
	{
		v1beta.GET("/models", geminiHandlers.GeminiModels)
		v1beta.POST("/models/:action", geminiHandlers.GeminiHandler)
		v1beta.GET("/models/:action", geminiHandlers.GeminiGetHandler)
	}

	// Root endpoint
	s.engine.GET("/", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"message": "CLI Proxy API Server",
			"endpoints": []string{
				"POST /v1/chat/completions",
				"POST /v1/completions",
				"GET /v1/models",
			},
		})
	})
	s.engine.POST("/v1internal:method", geminiCLIHandlers.CLIHandler)

	// Ollama compatible API routes (no authentication required, like in the example)
	// Handle /api/version without auth (before auth check)
	s.engine.GET("/api/version", ollamaHandlers.Version)
	s.engine.GET("/ollama/api/version", ollamaHandlers.Version)

	// Handle other Ollama endpoints (with optional auth - can work without API key)
	apiGroup := s.engine.Group("/api")
	apiGroup.Use(middleware.RequestSizeLimitMiddleware(s.cfg.MaxRequestSize))
	{
		apiGroup.GET("/tags", ollamaHandlers.Tags)
		apiGroup.POST("/chat", ollamaHandlers.Chat)
		apiGroup.POST("/generate", ollamaHandlers.Generate)
		apiGroup.POST("/show", ollamaHandlers.Show)
	}

	// Also support /ollama/api/* paths
	ollamaGroup := s.engine.Group("/ollama/api")
	ollamaGroup.Use(middleware.RequestSizeLimitMiddleware(s.cfg.MaxRequestSize))
	{
		ollamaGroup.GET("/tags", ollamaHandlers.Tags)
		ollamaGroup.POST("/chat", ollamaHandlers.Chat)
		ollamaGroup.POST("/generate", ollamaHandlers.Generate)
		ollamaGroup.POST("/show", ollamaHandlers.Show)
	}

	// OAuth callback endpoints (reuse main server port)
	// These endpoints receive provider redirects and persist
	// the short-lived code/state for the waiting goroutine.
	// Uses unified oauth package for HTML generation and file persistence.
	oauthCallbackHandler := func(provider string) gin.HandlerFunc {
		return func(c *gin.Context) {
			code := c.Query("code")
			state := c.Query("state")
			errStr := c.Query("error")
			// Persist to a temporary file keyed by state
			if state != "" {
				file := fmt.Sprintf("%s/.oauth-%s-%s.oauth", s.cfg.AuthDir, provider, state)
				payload := map[string]string{"code": code, "state": state, "error": errStr}
				data, _ := json.Marshal(payload)
				if err := os.WriteFile(file, data, 0o600); err != nil {
					log.Errorf("Failed to persist OAuth callback data to %s: %v", file, err)
				}
			}
			c.Header("Content-Type", "text/html; charset=utf-8")
			if errStr != "" {
				c.String(http.StatusOK, oauth.HTMLError(errStr))
			} else {
				c.String(http.StatusOK, oauth.HTMLSuccess())
			}
		}
	}

	s.engine.GET("/anthropic/callback", oauthCallbackHandler("anthropic"))
	s.engine.GET("/codex/callback", oauthCallbackHandler("codex"))
	s.engine.GET("/google/callback", oauthCallbackHandler("gemini"))
	s.engine.GET("/gemini/callback", oauthCallbackHandler("gemini")) // alias
	s.engine.GET("/iflow/callback", oauthCallbackHandler("iflow"))
	s.engine.GET("/antigravity/callback", oauthCallbackHandler("antigravity"))

	// Management routes are registered lazily by registerManagementRoutes when a secret is configured.
}

// unifiedModelsHandler creates a unified handler for the /v1/models endpoint
// that routes to different handlers based on the User-Agent header.
// If User-Agent starts with "claude-cli", it routes to Claude handler,
// otherwise it routes to OpenAI handler.
func (s *Server) unifiedModelsHandler(openaiHandler *openai.OpenAIAPIHandler, claudeHandler *claude.ClaudeCodeAPIHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		userAgent := c.GetHeader("User-Agent")

		// Route to Claude handler if User-Agent starts with "claude-cli"
		if strings.HasPrefix(userAgent, "claude-cli") {
			claudeHandler.ClaudeModels(c)
		} else {
			openaiHandler.OpenAIModels(c)
		}
	}
}

// AttachWebsocketRoute registers a websocket upgrade handler on the primary Gin engine.
// The handler is served as-is without additional middleware beyond the standard stack already configured.
func (s *Server) AttachWebsocketRoute(path string, handler http.Handler) {
	if s == nil || s.engine == nil || handler == nil {
		return
	}
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		trimmed = "/v1/ws"
	}
	if !strings.HasPrefix(trimmed, "/") {
		trimmed = "/" + trimmed
	}
	s.wsRouteMu.Lock()
	if _, exists := s.wsRoutes[trimmed]; exists {
		s.wsRouteMu.Unlock()
		return
	}
	s.wsRoutes[trimmed] = struct{}{}
	s.wsRouteMu.Unlock()

	authMiddleware := AuthMiddleware(s.accessManager)
	conditionalAuth := func(c *gin.Context) {
		if !s.wsAuthEnabled.Load() {
			c.Next()
			return
		}
		authMiddleware(c)
	}
	finalHandler := func(c *gin.Context) {
		handler.ServeHTTP(c.Writer, c.Request)
		c.Abort()
	}

	s.engine.GET(trimmed, conditionalAuth, finalHandler)
}

// conditionalAuthMiddleware returns middleware that checks disable-auth config flag.
// If disable-auth is true, all requests are allowed without authentication.
// Otherwise, standard authentication is applied.
func (s *Server) conditionalAuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.cfg != nil && s.cfg.DisableAuth {
			c.Next()
			return
		}
		AuthMiddleware(s.accessManager)(c)
	}
}

// isLocalhost checks if an IP address is localhost or a private network address.
func isLocalhost(ip string) bool {
	// Remove port if present
	if idx := strings.LastIndex(ip, ":"); idx != -1 {
		ip = ip[:idx]
	}
	// Remove brackets for IPv6
	ip = strings.Trim(ip, "[]")

	// Check common localhost addresses
	switch ip {
	case "localhost", "127.0.0.1", "::1", "::ffff:127.0.0.1":
		return true
	}

	// Check if it starts with 127. (any 127.x.x.x is localhost)
	if strings.HasPrefix(ip, "127.") {
		return true
	}

	return false
}

// AuthMiddleware returns a Gin middleware handler that authenticates requests
// using the configured authentication providers. When no providers are available,
// it allows all requests (legacy behaviour).
func AuthMiddleware(manager *access.Manager) gin.HandlerFunc {
	return func(c *gin.Context) {
		if manager == nil {
			c.Next()
			return
		}

		result, err := manager.Authenticate(c.Request.Context(), c.Request)
		if err == nil {
			if result != nil {
				c.Set("apiKey", result.Principal)
				c.Set("accessProvider", result.Provider)
				if len(result.Metadata) > 0 {
					c.Set("accessMetadata", result.Metadata)
				}
			}
			c.Next()
			return
		}

		// Allow requests without credentials only from localhost (Ollama compatibility)
		// External requests must provide valid authentication for security
		if errors.Is(err, access.ErrNoCredentials) {
			clientIP := c.ClientIP()
			if isLocalhost(clientIP) {
				c.Next()
				return
			}
			// External request without credentials - require authentication
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "API key required"})
			return
		}

		switch {
		case errors.Is(err, access.ErrInvalidCredential):
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Invalid API key"})
		default:
			log.Errorf("authentication middleware error: %v", err)
			c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Authentication service error"})
		}
	}
}
