// Package api provides the HTTP API server implementation for the CLI Proxy API.
package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/api/middleware"
	"github.com/nghyane/llm-mux/internal/logging"
)

// corsMiddleware returns a Gin middleware handler that adds CORS headers
// to every response, allowing cross-origin requests.
//
// Returns:
//   - gin.HandlerFunc: The CORS middleware handler
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "*")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// setupMiddleware configures all global middleware for the API server.
// It applies middleware in the proper order: logging, recovery, request logging, CORS.
func (s *Server) setupMiddleware(
	requestLogger logging.RequestLogger,
	extraMiddleware []gin.HandlerFunc,
) func(bool) {
	// Add core middleware
	s.engine.Use(logging.GinLogrusLogger())
	s.engine.Use(logging.GinLogrusRecovery())
	for _, mw := range extraMiddleware {
		s.engine.Use(mw)
	}

	// Add request logging middleware (positioned after recovery, before auth)
	var toggle func(bool)
	if requestLogger != nil {
		s.engine.Use(middleware.RequestLoggingMiddleware(requestLogger))
		if setter, ok := requestLogger.(interface{ SetEnabled(bool) }); ok {
			toggle = setter.SetEnabled
		}
	}

	s.engine.Use(corsMiddleware())

	return toggle
}

// managementAvailabilityMiddleware returns middleware that checks if management routes are enabled.
// If management routes are disabled, it returns a 404 status.
func (s *Server) managementAvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.managementRoutesEnabled.Load() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		c.Next()
	}
}
