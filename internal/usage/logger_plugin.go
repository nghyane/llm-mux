// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

var (
	statisticsEnabled   atomic.Bool
	defaultLoggerPlugin *LoggerPlugin
	activeBackend       Backend
)

func init() {
	statisticsEnabled.Store(true)
}

// LoggerPlugin collects usage statistics using lock-free counters
// and delegates persistence to a Backend implementation.
type LoggerPlugin struct {
	counters *Counters
	backend  Backend
}

// NewLoggerPlugin constructs a new logger plugin with the given backend.
func NewLoggerPlugin(backend Backend) *LoggerPlugin {
	return &LoggerPlugin{
		counters: NewCounters(),
		backend:  backend,
	}
}

// HandleUsage implements coreusage.Plugin.
// It updates lock-free counters and enqueues records to the backend.
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	tokens := normaliseUsage(record.Usage)
	statsKey := record.APIKey
	if statsKey == "" {
		statsKey = resolveAPIIdentifier(ctx, record)
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}

	// Update fast counters (lock-free)
	if p.counters != nil {
		p.counters.Record(failed, tokens.TotalTokens)
	}

	// Enqueue to backend for persistence
	if p.backend != nil {
		p.backend.Enqueue(UsageRecord{
			Provider:                 record.Provider,
			Model:                    modelName,
			APIKey:                   statsKey,
			AuthID:                   record.AuthID,
			AuthIndex:                record.AuthIndex,
			Source:                   record.Source,
			RequestedAt:              timestamp,
			Failed:                   failed,
			InputTokens:              tokens.PromptTokens,
			OutputTokens:             tokens.CompletionTokens,
			ReasoningTokens:          tokens.ReasoningTokens,
			CachedTokens:             tokens.CachedTokens,
			TotalTokens:              tokens.TotalTokens,
			AudioTokens:              tokens.AudioTokens,
			CacheCreationInputTokens: tokens.CacheCreationInputTokens,
			CacheReadInputTokens:     tokens.CacheReadInputTokens,
			ToolUsePromptTokens:      tokens.ToolUsePromptTokens,
		})
	}
}

// GetCounters returns the current counter snapshot.
func (p *LoggerPlugin) GetCounters() CounterSnapshot {
	if p == nil || p.counters == nil {
		return CounterSnapshot{}
	}
	return p.counters.Snapshot()
}

// GetBackend returns the backend for query operations.
func (p *LoggerPlugin) GetBackend() Backend {
	if p == nil {
		return nil
	}
	return p.backend
}

// SetStatisticsEnabled toggles whether statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// Initialize creates and starts the backend and logger plugin.
func Initialize(cfg BackendConfig) error {
	backend, err := NewBackend(cfg)
	if err != nil {
		return err
	}
	if err := backend.Start(); err != nil {
		return err
	}
	activeBackend = backend

	plugin := NewLoggerPlugin(backend)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stats, err := backend.QueryGlobalStats(ctx, time.Time{})
	if err != nil {
		log.Warnf("Failed to bootstrap usage counters from history: %v", err)
	} else if stats != nil {
		plugin.counters.Bootstrap(
			stats.TotalRequests,
			stats.SuccessCount,
			stats.FailureCount,
			stats.TotalTokens,
		)
		log.Infof("Bootstrapped usage counters: %d requests, %d tokens", stats.TotalRequests, stats.TotalTokens)
	}

	defaultLoggerPlugin = plugin
	RegisterPlugin(defaultLoggerPlugin)
	return nil
}

// Stop gracefully shuts down the usage system.
func Stop() error {
	if activeBackend != nil {
		return activeBackend.Stop()
	}
	return nil
}

// GetLoggerPlugin returns the shared logger plugin instance.
func GetLoggerPlugin() *LoggerPlugin { return defaultLoggerPlugin }

// TokenStats captures the token usage breakdown for a request.
type TokenStats struct {
	PromptTokens             int64 `json:"prompt_tokens"`
	CompletionTokens         int64 `json:"completion_tokens"`
	ReasoningTokens          int64 `json:"reasoning_tokens"`
	CachedTokens             int64 `json:"cached_tokens"`
	TotalTokens              int64 `json:"total_tokens"`
	AudioTokens              int64 `json:"audio_tokens,omitempty"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens,omitempty"`
	ToolUsePromptTokens      int64 `json:"tool_use_prompt_tokens,omitempty"`
}

// resolveAPIIdentifier extracts an API identifier from context or record.
func resolveAPIIdentifier(ctx context.Context, record Record) string {
	if ctx != nil {
		if ginCtx, ok := ctx.Value("gin").(*gin.Context); ok && ginCtx != nil {
			path := ginCtx.FullPath()
			if path == "" && ginCtx.Request != nil {
				path = ginCtx.Request.URL.Path
			}
			method := ""
			if ginCtx.Request != nil {
				method = ginCtx.Request.Method
			}
			if path != "" {
				if method != "" {
					return method + " " + path
				}
				return path
			}
		}
	}
	if record.Provider != "" {
		return record.Provider
	}
	return "unknown"
}

const httpStatusBadRequest = 400

// resolveSuccess determines if a request was successful based on context.
func resolveSuccess(ctx context.Context) bool {
	if ctx == nil {
		return true
	}
	ginCtx, ok := ctx.Value("gin").(*gin.Context)
	if !ok || ginCtx == nil {
		return true
	}
	status := ginCtx.Writer.Status()
	if status == 0 {
		return true
	}
	// 400 Bad Request is a user error, not a provider failure
	// Only count 401, 429, 5xx etc. as failures
	if status == httpStatusBadRequest {
		return true // user error, not provider failure
	}
	// Success: 2xx-3xx only
	return status < httpStatusBadRequest
}

// normaliseUsage converts IR usage to TokenStats.
func normaliseUsage(u *ir.Usage) TokenStats {
	if u == nil {
		return TokenStats{}
	}
	tokens := TokenStats{
		PromptTokens:             u.PromptTokens,
		CompletionTokens:         u.CompletionTokens,
		ReasoningTokens:          int64(u.ThoughtsTokenCount),
		CachedTokens:             u.CachedTokens,
		TotalTokens:              u.TotalTokens,
		AudioTokens:              u.AudioTokens,
		CacheCreationInputTokens: u.CacheCreationInputTokens,
		CacheReadInputTokens:     u.CacheReadInputTokens,
		ToolUsePromptTokens:      u.ToolUsePromptTokens,
	}
	// Fallback reasoning tokens from CompletionTokensDetails
	if tokens.ReasoningTokens == 0 && u.CompletionTokensDetails != nil {
		tokens.ReasoningTokens = u.CompletionTokensDetails.ReasoningTokens
	}
	// Compute total if not provided
	if tokens.TotalTokens == 0 {
		tokens.TotalTokens = tokens.PromptTokens + tokens.CompletionTokens
	}
	return tokens
}
