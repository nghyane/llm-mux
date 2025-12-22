package usage

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	log "github.com/sirupsen/logrus"
)

// LoadRecordsFromDB loads usage statistics from the database using optimized SQL aggregation.
// Instead of loading all records and processing them one by one, it:
// 1. Uses SQL GROUP BY for aggregates (totals, by_day, by_hour, by_api, by_model)
// 2. Only loads recent N records for the Details array
// This reduces memory usage and speeds up recovery significantly.
func LoadRecordsFromDB(db *sql.DB, retentionDays int, stats *RequestStatistics) error {
	if db == nil || stats == nil {
		return fmt.Errorf("database and statistics cannot be nil")
	}

	cutoffTime := time.Now().AddDate(0, 0, -retentionDays)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Load all aggregates in parallel-safe order, then apply with single lock
	var (
		globalAgg   globalAggregate
		dailyAggs   []dailyAggregate
		hourlyAggs  []hourlyAggregate
		apiModelAgg []apiModelAggregate
		recentRecs  []recentRecord
	)

	// Step 1: Load global totals
	if err := loadGlobalAggregate(ctx, db, cutoffTime, &globalAgg); err != nil {
		return fmt.Errorf("failed to load global aggregate: %w", err)
	}

	// Step 2: Load daily aggregates
	if err := loadDailyAggregates(ctx, db, cutoffTime, &dailyAggs); err != nil {
		return fmt.Errorf("failed to load daily aggregates: %w", err)
	}

	// Step 3: Load hourly aggregates
	if err := loadHourlyAggregates(ctx, db, cutoffTime, &hourlyAggs); err != nil {
		return fmt.Errorf("failed to load hourly aggregates: %w", err)
	}

	// Step 4: Load per-API/model aggregates
	if err := loadAPIModelAggregates(ctx, db, cutoffTime, &apiModelAgg); err != nil {
		return fmt.Errorf("failed to load API/model aggregates: %w", err)
	}

	// Step 5: Load only recent records for Details (limited per model)
	if err := loadRecentRecords(ctx, db, cutoffTime, &recentRecs); err != nil {
		return fmt.Errorf("failed to load recent records: %w", err)
	}

	// Apply all data with single lock
	stats.mu.Lock()
	defer stats.mu.Unlock()

	// Apply global stats
	stats.totalRequests = globalAgg.totalRequests
	stats.successCount = globalAgg.successCount
	stats.failureCount = globalAgg.failureCount
	stats.totalTokens = globalAgg.totalTokens

	// Apply daily aggregates
	for _, d := range dailyAggs {
		stats.requestsByDay[d.day] = d.requests
		stats.tokensByDay[d.day] = d.tokens
	}

	// Apply hourly aggregates
	for _, h := range hourlyAggs {
		stats.requestsByHour[h.hour] = h.requests
		stats.tokensByHour[h.hour] = h.tokens
	}

	// Apply API/model aggregates
	for _, am := range apiModelAgg {
		apiEntry, ok := stats.apis[am.apiKey]
		if !ok {
			if len(stats.apis) >= maxTrackedAPIs {
				continue
			}
			apiEntry = &apiStats{Models: make(map[string]*modelStats)}
			stats.apis[am.apiKey] = apiEntry
		}
		apiEntry.TotalRequests += am.requests
		apiEntry.TotalTokens += am.tokens

		modelEntry, ok := apiEntry.Models[am.model]
		if !ok {
			if len(apiEntry.Models) >= maxModelsPerAPI {
				continue
			}
			modelEntry = &modelStats{}
			apiEntry.Models[am.model] = modelEntry
		}
		modelEntry.TotalRequests += am.requests
		modelEntry.TotalTokens += am.tokens
	}

	// Apply recent records to Details arrays
	for _, r := range recentRecs {
		apiEntry, ok := stats.apis[r.apiKey]
		if !ok {
			continue // API was skipped due to capacity
		}
		modelEntry, ok := apiEntry.Models[r.model]
		if !ok {
			continue // Model was skipped due to capacity
		}
		if len(modelEntry.Details) < maxDetailsPerModel {
			modelEntry.Details = append(modelEntry.Details, r.detail)
		}
	}

	totalRecords := globalAgg.totalRequests
	log.Infof("Loaded usage statistics from database: %d total requests, %d API keys, %d recent details (last %d days)",
		totalRecords, len(stats.apis), len(recentRecs), retentionDays)

	return nil
}

// Aggregate types for SQL results
type globalAggregate struct {
	totalRequests int64
	successCount  int64
	failureCount  int64
	totalTokens   int64
}

type dailyAggregate struct {
	day      string
	requests int64
	tokens   int64
}

type hourlyAggregate struct {
	hour     int
	requests int64
	tokens   int64
}

type apiModelAggregate struct {
	apiKey   string
	model    string
	requests int64
	tokens   int64
}

type recentRecord struct {
	apiKey string
	model  string
	detail RequestDetail
}

func loadGlobalAggregate(ctx context.Context, db *sql.DB, cutoff time.Time, out *globalAggregate) error {
	row := db.QueryRowContext(ctx, `
		SELECT 
			COUNT(*),
			SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END),
			COALESCE(SUM(total_tokens), 0)
		FROM usage_records
		WHERE requested_at >= ?
	`, cutoff)

	return row.Scan(&out.totalRequests, &out.successCount, &out.failureCount, &out.totalTokens)
}

func loadDailyAggregates(ctx context.Context, db *sql.DB, cutoff time.Time, out *[]dailyAggregate) error {
	rows, err := db.QueryContext(ctx, `
		SELECT 
			DATE(requested_at) as day,
			COUNT(*) as requests,
			COALESCE(SUM(total_tokens), 0) as tokens
		FROM usage_records
		WHERE requested_at >= ?
		GROUP BY DATE(requested_at)
		ORDER BY day
	`, cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var d dailyAggregate
		if err := rows.Scan(&d.day, &d.requests, &d.tokens); err != nil {
			return err
		}
		*out = append(*out, d)
	}
	return rows.Err()
}

func loadHourlyAggregates(ctx context.Context, db *sql.DB, cutoff time.Time, out *[]hourlyAggregate) error {
	// Aggregate by hour of day (0-23), not by specific hour
	rows, err := db.QueryContext(ctx, `
		SELECT 
			CAST(strftime('%H', requested_at) AS INTEGER) as hour,
			COUNT(*) as requests,
			COALESCE(SUM(total_tokens), 0) as tokens
		FROM usage_records
		WHERE requested_at >= ?
		GROUP BY hour
		ORDER BY hour
	`, cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var h hourlyAggregate
		if err := rows.Scan(&h.hour, &h.requests, &h.tokens); err != nil {
			return err
		}
		*out = append(*out, h)
	}
	return rows.Err()
}

func loadAPIModelAggregates(ctx context.Context, db *sql.DB, cutoff time.Time, out *[]apiModelAggregate) error {
	rows, err := db.QueryContext(ctx, `
		SELECT 
			COALESCE(NULLIF(api_key, ''), NULLIF(provider, ''), 'unknown') as api_key,
			COALESCE(NULLIF(model, ''), 'unknown') as model,
			COUNT(*) as requests,
			COALESCE(SUM(total_tokens), 0) as tokens
		FROM usage_records
		WHERE requested_at >= ?
		GROUP BY api_key, model
		ORDER BY requests DESC
	`, cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var am apiModelAggregate
		if err := rows.Scan(&am.apiKey, &am.model, &am.requests, &am.tokens); err != nil {
			return err
		}
		*out = append(*out, am)
	}
	return rows.Err()
}

func loadRecentRecords(ctx context.Context, db *sql.DB, cutoff time.Time, out *[]recentRecord) error {
	// Load recent records for Details, limited to avoid memory bloat
	// Using window function to get last N records per api_key/model combination
	rows, err := db.QueryContext(ctx, `
		WITH ranked AS (
			SELECT 
				COALESCE(NULLIF(api_key, ''), NULLIF(provider, ''), 'unknown') as api_key,
				COALESCE(NULLIF(model, ''), 'unknown') as model,
				requested_at, source, auth_index, failed,
				input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
				COALESCE(audio_tokens, 0) as audio_tokens,
				COALESCE(cache_creation_input_tokens, 0) as cache_creation_input_tokens,
				COALESCE(cache_read_input_tokens, 0) as cache_read_input_tokens,
				COALESCE(tool_use_prompt_tokens, 0) as tool_use_prompt_tokens,
				ROW_NUMBER() OVER (PARTITION BY api_key, model ORDER BY requested_at DESC) as rn
			FROM usage_records
			WHERE requested_at >= ?
		)
		SELECT api_key, model, requested_at, source, auth_index, failed,
			input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens,
			audio_tokens, cache_creation_input_tokens, cache_read_input_tokens, tool_use_prompt_tokens
		FROM ranked
		WHERE rn <= ?
		ORDER BY api_key, model, requested_at ASC
	`, cutoff, maxDetailsPerModel)
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var r recentRecord
		var ts time.Time
		var source string
		var authIndex uint64
		var failed bool
		var input, output, reasoning, cached, total int64
		var audio, cacheCreation, cacheRead, toolUse int64

		if err := rows.Scan(
			&r.apiKey, &r.model, &ts, &source, &authIndex, &failed,
			&input, &output, &reasoning, &cached, &total,
			&audio, &cacheCreation, &cacheRead, &toolUse,
		); err != nil {
			return err
		}

		totalTokens := total
		if totalTokens == 0 {
			totalTokens = input + output
		}

		r.detail = RequestDetail{
			Timestamp: ts,
			Source:    source,
			AuthIndex: authIndex,
			Failed:    failed,
			Tokens: TokenStats{
				PromptTokens:             input,
				CompletionTokens:         output,
				ReasoningTokens:          reasoning,
				CachedTokens:             cached,
				TotalTokens:              totalTokens,
				AudioTokens:              audio,
				CacheCreationInputTokens: cacheCreation,
				CacheReadInputTokens:     cacheRead,
				ToolUsePromptTokens:      toolUse,
			},
		}
		*out = append(*out, r)
	}
	return rows.Err()
}
