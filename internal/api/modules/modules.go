// Package modules provides a pluggable routing module system for extending
// the API server with optional features without modifying core routing logic.
package modules

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/sdk/api/handlers"
)

// Context encapsulates the dependencies exposed to routing modules during
// registration. Modules can use the Gin engine to attach routes, the shared
// BaseAPIHandler for constructing SDK-specific handlers, and the resolved
// authentication middleware for protecting routes that require API keys.
type Context struct {
	Engine         *gin.Engine
	BaseHandler    *handlers.BaseAPIHandler
	Config         *config.Config
	AuthMiddleware gin.HandlerFunc
}

// RouteModuleV2 represents a pluggable bundle of routes that can integrate with
// the API server without modifying its core routing logic. Implementations can
// attach routes during Register and react to configuration updates via
// OnConfigUpdated.
// This is the preferred interface for new modules. It uses Context for cleaner
// dependency injection and supports idempotent registration.
type RouteModuleV2 interface {
	// Name returns a unique identifier for logging and diagnostics.
	Name() string

	// Register wires the module's routes into the provided Gin engine. Modules
	// should treat multiple calls as idempotent and avoid duplicate route
	// registration when invoked more than once.
	Register(ctx Context) error

	// OnConfigUpdated notifies the module when the server configuration changes
	// via hot reload. Implementations can refresh cached state or emit warnings.
	OnConfigUpdated(cfg *config.Config) error
}

// RegisterModule registers a module using the V2 interface.
// Example usage:
//	ctx := modules.Context{
//	    Engine:         engine,
//	    BaseHandler:    baseHandler,
//	    Config:         cfg,
//	    AuthMiddleware: authMiddleware,
//	}
//	if err := modules.RegisterModule(ctx, ampModule); err != nil {
//	    log.Errorf("Failed to register module: %v", err)
//	}
func RegisterModule(ctx Context, mod any) error {
	// Register using V2 interface
	if v2, ok := mod.(RouteModuleV2); ok {
		return v2.Register(ctx)
	}

	return fmt.Errorf("unsupported module type %T (must implement RouteModuleV2)", mod)
}
