// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// It includes plugins for monitoring API usage, token consumption, and other metrics
// to help with observability and billing purposes.
package usage

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	coreusage "github.com/nghyane/llm-mux/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var statisticsEnabled atomic.Bool

func init() {
	statisticsEnabled.Store(true)
	defaultLoggerPlugin = NewLoggerPlugin()
	coreusage.RegisterPlugin(defaultLoggerPlugin)
}

// LoggerPlugin collects in-memory request statistics for usage analysis.
// It implements coreusage.Plugin to receive usage records emitted by the runtime.
type LoggerPlugin struct {
	stats     *RequestStatistics
	persister *Persister
}

// NewLoggerPlugin constructs a new logger plugin instance.
// Returns:
//   - *LoggerPlugin: A new logger plugin instance wired to the shared statistics store.
func NewLoggerPlugin() *LoggerPlugin { return &LoggerPlugin{stats: defaultRequestStatistics} }

// HandleUsage implements coreusage.Plugin.
// It updates the in-memory statistics store whenever a usage record is received.
// Parameters:
//   - ctx: The context for the usage record
//   - record: The usage record to aggregate
func (p *LoggerPlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if !statisticsEnabled.Load() {
		return
	}
	if p == nil || p.stats == nil {
		return
	}
	p.stats.Record(ctx, record)

	// Enqueue to persister if enabled
	if p.persister != nil {
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

		p.persister.Enqueue(UsageRecord{
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

// SetStatisticsEnabled toggles whether in-memory statistics are recorded.
func SetStatisticsEnabled(enabled bool) { statisticsEnabled.Store(enabled) }

// StatisticsEnabled reports the current recording state.
func StatisticsEnabled() bool { return statisticsEnabled.Load() }

// InitializePersistence initializes SQLite persistence for usage records.
// It loads historical records from the database to rebuild in-memory stats.
// Returns an error if persistence fails to initialize, but this should not stop the server.
func InitializePersistence(dbPath string, batchSize, flushIntervalSecs, retentionDays int) error {
	if dbPath == "" {
		return nil // Persistence disabled
	}

	persister, err := NewPersister(dbPath, batchSize, flushIntervalSecs, retentionDays)
	if err != nil {
		return fmt.Errorf("failed to initialize persister: %w", err)
	}

	// Load historical records from database
	if persister.db != nil {
		if err := LoadRecordsFromDB(persister.db, retentionDays, defaultRequestStatistics); err != nil {
			// Log warning but don't fail - we can continue with empty stats
			log.Warnf("Failed to load historical usage records: %v", err)
		}
	}

	// Attach persister to the default logger plugin
	if defaultLoggerPlugin != nil {
		defaultLoggerPlugin.persister = persister
	}

	return nil
}

// StopPersistence gracefully shuts down the persister, flushing pending writes.
func StopPersistence() error {
	if defaultLoggerPlugin != nil && defaultLoggerPlugin.persister != nil {
		return defaultLoggerPlugin.persister.Stop()
	}
	return nil
}

// RequestStatistics maintains aggregated request metrics in memory.
type RequestStatistics struct {
	mu sync.RWMutex

	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64

	apis map[string]*apiStats

	requestsByDay  map[string]int64
	requestsByHour map[int]int64
	tokensByDay    map[string]int64
	tokensByHour   map[int]int64
}

// apiStats holds aggregated metrics for a single API key.
type apiStats struct {
	TotalRequests int64
	TotalTokens   int64
	Models        map[string]*modelStats
}

// Memory limits to prevent unbounded growth in statistics collection.
const (
	maxDetailsPerModel = 1000 // Max request details per model
	maxTrackedAPIs     = 500  // Max unique API keys
	maxModelsPerAPI    = 50   // Max models per API key
	maxDaysRetention   = 30   // Keep last 30 days only
)

// modelStats holds aggregated metrics for a specific model within an API.
type modelStats struct {
	TotalRequests int64
	TotalTokens   int64
	Details       []RequestDetail
}

// RequestDetail stores the timestamp and token usage for a single request.
type RequestDetail struct {
	Timestamp time.Time  `json:"timestamp"`
	Source    string     `json:"source"`
	AuthIndex uint64     `json:"auth_index"`
	Tokens    TokenStats `json:"tokens"`
	Failed    bool       `json:"failed"`
}

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

// StatisticsSnapshot represents an immutable view of the aggregated metrics.
type StatisticsSnapshot struct {
	TotalRequests int64 `json:"total_requests"`
	SuccessCount  int64 `json:"success_count"`
	FailureCount  int64 `json:"failure_count"`
	TotalTokens   int64 `json:"total_tokens"`

	APIs map[string]APISnapshot `json:"apis"`

	RequestsByDay  map[string]int64 `json:"requests_by_day"`
	RequestsByHour map[string]int64 `json:"requests_by_hour"`
	TokensByDay    map[string]int64 `json:"tokens_by_day"`
	TokensByHour   map[string]int64 `json:"tokens_by_hour"`
}

// APISnapshot summarises metrics for a single API key.
type APISnapshot struct {
	TotalRequests int64                    `json:"total_requests"`
	TotalTokens   int64                    `json:"total_tokens"`
	Models        map[string]ModelSnapshot `json:"models"`
}

// ModelSnapshot summarises metrics for a specific model.
type ModelSnapshot struct {
	TotalRequests int64           `json:"total_requests"`
	TotalTokens   int64           `json:"total_tokens"`
	Details       []RequestDetail `json:"details"`
}

var defaultRequestStatistics = NewRequestStatistics()
var defaultLoggerPlugin *LoggerPlugin

// GetRequestStatistics returns the shared statistics store.
func GetRequestStatistics() *RequestStatistics { return defaultRequestStatistics }

// GetLoggerPlugin returns the shared logger plugin instance.
func GetLoggerPlugin() *LoggerPlugin { return defaultLoggerPlugin }

// NewRequestStatistics constructs an empty statistics store.
func NewRequestStatistics() *RequestStatistics {
	return &RequestStatistics{
		apis:           make(map[string]*apiStats),
		requestsByDay:  make(map[string]int64),
		requestsByHour: make(map[int]int64),
		tokensByDay:    make(map[string]int64),
		tokensByHour:   make(map[int]int64),
	}
}

// Record ingests a new usage record and updates the aggregates.
func (s *RequestStatistics) Record(ctx context.Context, record coreusage.Record) {
	if s == nil {
		return
	}
	if !statisticsEnabled.Load() {
		return
	}
	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}
	tokens := normaliseUsage(record.Usage)
	totalTokens := tokens.TotalTokens
	statsKey := record.APIKey
	if statsKey == "" {
		statsKey = resolveAPIIdentifier(ctx, record)
	}
	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	success := !failed
	modelName := record.Model
	if modelName == "" {
		modelName = "unknown"
	}
	dayKey := timestamp.Format("2006-01-02")
	hourKey := timestamp.Hour()

	s.mu.Lock()
	defer s.mu.Unlock()

	s.totalRequests++
	if success {
		s.successCount++
	} else {
		s.failureCount++
	}
	s.totalTokens += totalTokens

	stats, ok := s.apis[statsKey]
	if !ok {
		// Only track new API if under limit; skip if over capacity
		if len(s.apis) >= maxTrackedAPIs {
			return
		}
		stats = &apiStats{Models: make(map[string]*modelStats)}
		s.apis[statsKey] = stats
	}
	s.updateAPIStats(stats, modelName, RequestDetail{
		Timestamp: timestamp,
		Source:    record.Source,
		AuthIndex: record.AuthIndex,
		Tokens:    tokens,
		Failed:    failed,
	})

	s.requestsByDay[dayKey]++
	s.requestsByHour[hourKey]++
	s.tokensByDay[dayKey] += totalTokens
	s.tokensByHour[hourKey] += totalTokens

	// Enforce retention policy: remove entries older than maxDaysRetention
	s.enforceRetentionPolicy(timestamp)
}

// enforceRetentionPolicy removes day entries older than maxDaysRetention.
// Called during Record() with the current request timestamp. Lock must be held.
func (s *RequestStatistics) enforceRetentionPolicy(currentTime time.Time) {
	cutoffTime := currentTime.AddDate(0, 0, -maxDaysRetention)
	cutoffKey := cutoffTime.Format("2006-01-02")

	for dayKey := range s.requestsByDay {
		if dayKey < cutoffKey {
			delete(s.requestsByDay, dayKey)
			delete(s.tokensByDay, dayKey)
		}
	}
}

func (s *RequestStatistics) updateAPIStats(stats *apiStats, model string, detail RequestDetail) {
	stats.TotalRequests++
	stats.TotalTokens += detail.Tokens.TotalTokens
	modelStatsValue, ok := stats.Models[model]
	if !ok {
		// Only track new model if under limit; skip model details if over capacity
		if len(stats.Models) >= maxModelsPerAPI {
			return
		}
		modelStatsValue = &modelStats{}
		stats.Models[model] = modelStatsValue
	}
	modelStatsValue.TotalRequests++
	modelStatsValue.TotalTokens += detail.Tokens.TotalTokens

	// Enforce max capacity with FIFO eviction to prevent unbounded memory growth
	if len(modelStatsValue.Details) >= maxDetailsPerModel {
		// Shift slice to remove oldest entry (FIFO)
		copy(modelStatsValue.Details, modelStatsValue.Details[1:])
		modelStatsValue.Details = modelStatsValue.Details[:len(modelStatsValue.Details)-1]
	}
	modelStatsValue.Details = append(modelStatsValue.Details, detail)
}

// Snapshot returns a copy of the aggregated metrics for external consumption.
func (s *RequestStatistics) Snapshot() StatisticsSnapshot {
	result := StatisticsSnapshot{}
	if s == nil {
		return result
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	result.TotalRequests = s.totalRequests
	result.SuccessCount = s.successCount
	result.FailureCount = s.failureCount
	result.TotalTokens = s.totalTokens

	result.APIs = make(map[string]APISnapshot, len(s.apis))
	for apiName, stats := range s.apis {
		apiSnapshot := APISnapshot{
			TotalRequests: stats.TotalRequests,
			TotalTokens:   stats.TotalTokens,
			Models:        make(map[string]ModelSnapshot, len(stats.Models)),
		}
		for modelName, modelStatsValue := range stats.Models {
			requestDetails := make([]RequestDetail, len(modelStatsValue.Details))
			copy(requestDetails, modelStatsValue.Details)
			apiSnapshot.Models[modelName] = ModelSnapshot{
				TotalRequests: modelStatsValue.TotalRequests,
				TotalTokens:   modelStatsValue.TotalTokens,
				Details:       requestDetails,
			}
		}
		result.APIs[apiName] = apiSnapshot
	}

	result.RequestsByDay = make(map[string]int64, len(s.requestsByDay))
	for k, v := range s.requestsByDay {
		result.RequestsByDay[k] = v
	}

	result.RequestsByHour = make(map[string]int64, len(s.requestsByHour))
	for hour, v := range s.requestsByHour {
		key := formatHour(hour)
		result.RequestsByHour[key] = v
	}

	result.TokensByDay = make(map[string]int64, len(s.tokensByDay))
	for k, v := range s.tokensByDay {
		result.TokensByDay[k] = v
	}

	result.TokensByHour = make(map[string]int64, len(s.tokensByHour))
	for hour, v := range s.tokensByHour {
		key := formatHour(hour)
		result.TokensByHour[key] = v
	}

	return result
}

func resolveAPIIdentifier(ctx context.Context, record coreusage.Record) string {
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
		return true
	}
	return status < httpStatusBadRequest || status >= 500
}

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

func formatHour(hour int) string {
	if hour < 0 {
		hour = 0
	}
	hour = hour % 24
	return fmt.Sprintf("%02d", hour)
}
