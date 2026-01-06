package usage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	log "github.com/nghyane/llm-mux/internal/logging"
)

// PostgresBackend implements the Backend interface using PostgreSQL with pgx.
type PostgresBackend struct {
	pool          *pgxpool.Pool
	recordChan    chan UsageRecord
	flushTicker   *time.Ticker
	cleanupTicker *time.Ticker
	stopChan      chan struct{}
	stopOnce      sync.Once
	wg            sync.WaitGroup
	batchSize     int
	flushInterval time.Duration
	retentionDays int
}

// Postgres backend constants
const (
	pgDefaultBatchSize         = 100
	pgDefaultFlushInterval     = 5 * time.Second
	pgDefaultRetentionDays     = 30
	pgDefaultChannelBufferSize = 1000
)

// NewPostgresBackend creates a new PostgreSQL-backed persistence layer.
// The backend must be started with Start() before use.
func NewPostgresBackend(dsn string, cfg BackendConfig) (*PostgresBackend, error) {
	if dsn == "" {
		return nil, fmt.Errorf("postgres DSN is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to create connection pool: %w", err)
	}

	// Verify connection
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	// Initialize schema
	if err := ensurePostgresSchema(ctx, pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = pgDefaultBatchSize
	}

	flushInterval := cfg.FlushInterval
	if flushInterval <= 0 {
		flushInterval = pgDefaultFlushInterval
	}

	retentionDays := cfg.RetentionDays
	if retentionDays <= 0 {
		retentionDays = pgDefaultRetentionDays
	}

	return &PostgresBackend{
		pool:          pool,
		recordChan:    make(chan UsageRecord, pgDefaultChannelBufferSize),
		flushTicker:   time.NewTicker(flushInterval),
		stopChan:      make(chan struct{}),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		retentionDays: retentionDays,
		cleanupTicker: time.NewTicker(24 * time.Hour),
	}, nil
}

// ensurePostgresSchema creates the usage_records table and indexes if they don't exist.
func ensurePostgresSchema(ctx context.Context, pool *pgxpool.Pool) error {
	schema := `
	CREATE TABLE IF NOT EXISTS usage_records (
		id BIGSERIAL PRIMARY KEY,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		api_key TEXT NOT NULL DEFAULT '',
		auth_id TEXT NOT NULL DEFAULT '',
		auth_index INTEGER NOT NULL DEFAULT 0,
		source TEXT NOT NULL DEFAULT '',
		requested_at TIMESTAMPTZ NOT NULL,
		failed BOOLEAN NOT NULL DEFAULT FALSE,
		input_tokens BIGINT NOT NULL DEFAULT 0,
		output_tokens BIGINT NOT NULL DEFAULT 0,
		reasoning_tokens BIGINT NOT NULL DEFAULT 0,
		cached_tokens BIGINT NOT NULL DEFAULT 0,
		total_tokens BIGINT NOT NULL DEFAULT 0,
		audio_tokens BIGINT NOT NULL DEFAULT 0,
		cache_creation_input_tokens BIGINT NOT NULL DEFAULT 0,
		cache_read_input_tokens BIGINT NOT NULL DEFAULT 0,
		tool_use_prompt_tokens BIGINT NOT NULL DEFAULT 0,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	CREATE INDEX IF NOT EXISTS idx_usage_requested_at ON usage_records(requested_at);
	CREATE INDEX IF NOT EXISTS idx_usage_api_key ON usage_records(api_key);
	CREATE INDEX IF NOT EXISTS idx_usage_provider_model ON usage_records(provider, model);
	`

	_, err := pool.Exec(ctx, schema)
	return err
}

// Start begins background workers (write loop, cleanup loop).
func (b *PostgresBackend) Start() error {
	b.wg.Add(2)
	go b.writeLoop()
	go b.cleanupLoop()
	return nil
}

// Stop gracefully shuts down the backend, flushing pending writes.
func (b *PostgresBackend) Stop() error {
	if b == nil {
		return nil
	}

	b.stopOnce.Do(func() {
		// Signal stop to all goroutines
		close(b.stopChan)

		// Stop tickers
		b.flushTicker.Stop()
		b.cleanupTicker.Stop()

		// Wait for workers to finish
		b.wg.Wait()

		// Close connection pool
		if b.pool != nil {
			b.pool.Close()
		}
	})

	return nil
}

// Enqueue adds a usage record to the write queue.
// This method is non-blocking and safe for high-throughput use.
func (b *PostgresBackend) Enqueue(record UsageRecord) {
	if b == nil {
		return
	}
	select {
	case b.recordChan <- record:
		// Successfully enqueued
	default:
		// Channel full, drop record with warning
		log.Warnf("Usage persistence queue full, dropping record for %s/%s", record.Provider, record.Model)
	}
}

// Flush forces pending records to be written to storage.
func (b *PostgresBackend) Flush(ctx context.Context) error {
	if b == nil {
		return nil
	}

	// Drain channel into batch and write
	batch := make([]UsageRecord, 0, b.batchSize)
	for {
		select {
		case record := <-b.recordChan:
			batch = append(batch, record)
			if len(batch) >= b.batchSize {
				if err := b.writeBatch(ctx, batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		default:
			// Channel empty, write remaining batch
			if len(batch) > 0 {
				return b.writeBatch(ctx, batch)
			}
			return nil
		}
	}
}

// QueryGlobalStats returns aggregate statistics since the given time.
func (b *PostgresBackend) QueryGlobalStats(ctx context.Context, since time.Time) (*AggregatedStats, error) {
	row := b.pool.QueryRow(ctx, `
		SELECT 
			COUNT(*),
			SUM(CASE WHEN failed = false THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed = true THEN 1 ELSE 0 END),
			COALESCE(SUM(total_tokens), 0)
		FROM usage_records
		WHERE requested_at >= $1
	`, since)

	var stats AggregatedStats
	if err := row.Scan(&stats.TotalRequests, &stats.SuccessCount, &stats.FailureCount, &stats.TotalTokens); err != nil {
		return nil, fmt.Errorf("failed to query global stats: %w", err)
	}
	return &stats, nil
}

// QueryDailyStats returns per-day statistics since the given time.
func (b *PostgresBackend) QueryDailyStats(ctx context.Context, since time.Time) ([]DailyStats, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT 
			COALESCE(DATE(requested_at)::TEXT, TO_CHAR(NOW(), 'YYYY-MM-DD')) as day,
			COUNT(*) as requests,
			COALESCE(SUM(total_tokens), 0) as tokens
		FROM usage_records
		WHERE requested_at >= $1
		GROUP BY DATE(requested_at)
		HAVING DATE(requested_at) IS NOT NULL
		ORDER BY day
	`, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily stats: %w", err)
	}
	defer rows.Close()

	var results []DailyStats
	for rows.Next() {
		var d DailyStats
		if err := rows.Scan(&d.Day, &d.Requests, &d.Tokens); err != nil {
			return nil, err
		}
		if d.Day != "" {
			results = append(results, d)
		}
	}
	return results, rows.Err()
}

// QueryHourlyStats returns per-hour-of-day statistics since the given time.
func (b *PostgresBackend) QueryHourlyStats(ctx context.Context, since time.Time) ([]HourlyStats, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT 
			EXTRACT(HOUR FROM requested_at)::INTEGER as hour,
			COUNT(*) as requests,
			COALESCE(SUM(total_tokens), 0) as tokens
		FROM usage_records
		WHERE requested_at >= $1
		GROUP BY EXTRACT(HOUR FROM requested_at)
		ORDER BY hour
	`, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query hourly stats: %w", err)
	}
	defer rows.Close()

	var results []HourlyStats
	for rows.Next() {
		var h HourlyStats
		if err := rows.Scan(&h.Hour, &h.Requests, &h.Tokens); err != nil {
			return nil, err
		}
		results = append(results, h)
	}
	return results, rows.Err()
}

func (b *PostgresBackend) QueryProviderStats(ctx context.Context, since time.Time) ([]ProviderStats, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT 
			COALESCE(NULLIF(provider, ''), 'unknown') as provider,
			COUNT(*) as requests,
			SUM(CASE WHEN failed = false THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN failed = true THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COUNT(DISTINCT NULLIF(auth_id, '')) as account_count,
			ARRAY_AGG(DISTINCT NULLIF(model, '')) FILTER (WHERE model != '') as models
		FROM usage_records
		WHERE requested_at >= $1
		GROUP BY provider
		ORDER BY requests DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query provider stats: %w", err)
	}
	defer rows.Close()

	var results []ProviderStats
	for rows.Next() {
		var ps ProviderStats
		if err := rows.Scan(
			&ps.Provider, &ps.Requests, &ps.SuccessCount, &ps.FailureCount,
			&ps.InputTokens, &ps.OutputTokens, &ps.ReasoningTokens, &ps.TotalTokens,
			&ps.AccountCount, &ps.Models,
		); err != nil {
			return nil, err
		}
		results = append(results, ps)
	}
	return results, rows.Err()
}

func (b *PostgresBackend) QueryAuthStats(ctx context.Context, since time.Time) ([]AuthStats, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT 
			COALESCE(NULLIF(provider, ''), 'unknown') as provider,
			COALESCE(NULLIF(auth_id, ''), 'unknown') as auth_id,
			COUNT(*) as requests,
			SUM(CASE WHEN failed = false THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN failed = true THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		FROM usage_records
		WHERE requested_at >= $1
		GROUP BY provider, auth_id
		ORDER BY requests DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query auth stats: %w", err)
	}
	defer rows.Close()

	var results []AuthStats
	for rows.Next() {
		var as AuthStats
		if err := rows.Scan(
			&as.Provider, &as.AuthID, &as.Requests, &as.SuccessCount, &as.FailureCount,
			&as.InputTokens, &as.OutputTokens, &as.ReasoningTokens, &as.TotalTokens,
		); err != nil {
			return nil, err
		}
		results = append(results, as)
	}
	return results, rows.Err()
}

func (b *PostgresBackend) QueryModelStats(ctx context.Context, since time.Time) ([]ModelStats, error) {
	rows, err := b.pool.Query(ctx, `
		SELECT 
			COALESCE(NULLIF(model, ''), 'unknown') as model,
			COALESCE(NULLIF(provider, ''), 'unknown') as provider,
			COUNT(*) as requests,
			SUM(CASE WHEN failed = false THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN failed = true THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		FROM usage_records
		WHERE requested_at >= $1
		GROUP BY model, provider
		ORDER BY requests DESC
	`, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query model stats: %w", err)
	}
	defer rows.Close()

	var results []ModelStats
	for rows.Next() {
		var ms ModelStats
		if err := rows.Scan(
			&ms.Model, &ms.Provider, &ms.Requests, &ms.SuccessCount, &ms.FailureCount,
			&ms.InputTokens, &ms.OutputTokens, &ms.ReasoningTokens, &ms.TotalTokens,
		); err != nil {
			return nil, err
		}
		results = append(results, ms)
	}
	return results, rows.Err()
}

// Cleanup removes records older than the given time.
func (b *PostgresBackend) Cleanup(ctx context.Context, before time.Time) (int64, error) {
	result, err := b.pool.Exec(ctx, `
		DELETE FROM usage_records WHERE requested_at < $1
	`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected(), nil
}

// writeLoop continuously reads from the record channel and writes in batches.
func (b *PostgresBackend) writeLoop() {
	defer b.wg.Done()

	batch := make([]UsageRecord, 0, b.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if err := b.writeBatch(ctx, batch); err != nil {
			log.Errorf("Failed to write usage batch: %v", err)
		}
		cancel()
		batch = batch[:0] // Clear batch
	}

	for {
		select {
		case record := <-b.recordChan:
			batch = append(batch, record)
			if len(batch) >= b.batchSize {
				flush()
			}
		case <-b.flushTicker.C:
			flush()
		case <-b.stopChan:
			// Drain remaining records
			for {
				select {
				case record := <-b.recordChan:
					batch = append(batch, record)
					if len(batch) >= b.batchSize {
						flush()
					}
				default:
					flush()
					return
				}
			}
		}
	}
}

// writeBatch writes a batch of records using CopyFrom for high performance.
func (b *PostgresBackend) writeBatch(ctx context.Context, records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}

	columns := []string{
		"provider", "model", "api_key", "auth_id", "auth_index", "source",
		"requested_at", "failed", "input_tokens", "output_tokens",
		"reasoning_tokens", "cached_tokens", "total_tokens",
		"audio_tokens", "cache_creation_input_tokens", "cache_read_input_tokens",
		"tool_use_prompt_tokens",
	}

	_, err := b.pool.CopyFrom(
		ctx,
		pgx.Identifier{"usage_records"},
		columns,
		pgx.CopyFromSlice(len(records), func(i int) ([]any, error) {
			r := records[i]
			return []any{
				r.Provider,
				r.Model,
				r.APIKey,
				r.AuthID,
				r.AuthIndex,
				r.Source,
				r.RequestedAt,
				r.Failed,
				r.InputTokens,
				r.OutputTokens,
				r.ReasoningTokens,
				r.CachedTokens,
				r.TotalTokens,
				r.AudioTokens,
				r.CacheCreationInputTokens,
				r.CacheReadInputTokens,
				r.ToolUsePromptTokens,
			}, nil
		}),
	)

	if err != nil {
		return fmt.Errorf("failed to copy records: %w", err)
	}

	return nil
}

// cleanupLoop periodically removes old records based on retention policy.
func (b *PostgresBackend) cleanupLoop() {
	defer b.wg.Done()

	for {
		select {
		case <-b.cleanupTicker.C:
			cutoffTime := time.Now().AddDate(0, 0, -b.retentionDays)
			ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
			rowsDeleted, err := b.Cleanup(ctx, cutoffTime)
			cancel()
			if err != nil {
				log.Errorf("Failed to cleanup old usage records: %v", err)
			} else if rowsDeleted > 0 {
				log.Infof("Cleaned up %d usage records older than %d days", rowsDeleted, b.retentionDays)
			}
		case <-b.stopChan:
			return
		}
	}
}
