package usage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/nghyane/llm-mux/internal/logging"
	_ "modernc.org/sqlite"
)

// SQLiteBackend implements the Backend interface using SQLite.
type SQLiteBackend struct {
	db            *sql.DB
	recordChan    chan UsageRecord
	flushTicker   *time.Ticker
	cleanupTicker *time.Ticker
	stopChan      chan struct{}
	stopOnce      sync.Once
	wg            sync.WaitGroup
	batchSize     int
	flushInterval time.Duration
	retentionDays int
	dbPath        string
}

// SQLite backend constants
const (
	sqliteDefaultBatchSize         = 100
	sqliteDefaultFlushInterval     = 5 * time.Second
	sqliteDefaultRetentionDays     = 30
	sqliteDefaultChannelBufferSize = 1000
)

func initSchema(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS usage_records (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		provider TEXT NOT NULL,
		model TEXT NOT NULL,
		api_key TEXT NOT NULL DEFAULT '',
		auth_id TEXT NOT NULL DEFAULT '',
		auth_index INTEGER NOT NULL DEFAULT 0,
		source TEXT NOT NULL DEFAULT '',
		requested_at TIMESTAMP NOT NULL,
		failed BOOLEAN NOT NULL DEFAULT 0,
		input_tokens INTEGER NOT NULL DEFAULT 0,
		output_tokens INTEGER NOT NULL DEFAULT 0,
		reasoning_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		audio_tokens INTEGER NOT NULL DEFAULT 0,
		cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0,
		cache_read_input_tokens INTEGER NOT NULL DEFAULT 0,
		tool_use_prompt_tokens INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE INDEX IF NOT EXISTS idx_usage_requested_at ON usage_records(requested_at);
	CREATE INDEX IF NOT EXISTS idx_usage_api_key ON usage_records(api_key);
	CREATE INDEX IF NOT EXISTS idx_usage_provider_model ON usage_records(provider, model);
	`

	if _, err := db.Exec(schema); err != nil {
		return err
	}

	return migrateSchema(db)
}

func migrateSchema(db *sql.DB) error {
	migrations := []string{
		"audio_tokens INTEGER NOT NULL DEFAULT 0",
		"cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0",
		"cache_read_input_tokens INTEGER NOT NULL DEFAULT 0",
		"tool_use_prompt_tokens INTEGER NOT NULL DEFAULT 0",
	}

	for _, colDef := range migrations {
		_, err := db.Exec("ALTER TABLE usage_records ADD COLUMN " + colDef)
		if err != nil {
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migration failed for [%s]: %w", colDef, err)
		}
		colName := strings.Fields(colDef)[0]
		log.Infof("Added column %s to usage_records table", colName)
	}

	return nil
}

// NewSQLiteBackend creates a new SQLite-backed persistence layer.
// The backend must be started with Start() before use.
func NewSQLiteBackend(dbPath string, cfg BackendConfig) (*SQLiteBackend, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("SQLite path is required")
	}

	// Expand ~ to home directory
	if strings.HasPrefix(dbPath, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to get home directory: %w", err)
		}
		dbPath = filepath.Join(home, dbPath[1:])
	}

	// Ensure parent directory exists
	dbDir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dbDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create database directory: %w", err)
	}

	// Open database with WAL mode
	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_synchronous=NORMAL&_cache_size=-64000")
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %w", err)
	}

	// Set connection pool settings for SQLite
	db.SetMaxOpenConns(1) // SQLite works best with single connection
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	// Initialize schema
	if err := initSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize schema: %w", err)
	}

	batchSize := cfg.BatchSize
	if batchSize <= 0 {
		batchSize = sqliteDefaultBatchSize
	}

	flushInterval := cfg.FlushInterval
	if flushInterval <= 0 {
		flushInterval = sqliteDefaultFlushInterval
	}

	retentionDays := cfg.RetentionDays
	if retentionDays <= 0 {
		retentionDays = sqliteDefaultRetentionDays
	}

	return &SQLiteBackend{
		db:            db,
		recordChan:    make(chan UsageRecord, sqliteDefaultChannelBufferSize),
		flushTicker:   time.NewTicker(flushInterval),
		stopChan:      make(chan struct{}),
		batchSize:     batchSize,
		flushInterval: flushInterval,
		retentionDays: retentionDays,
		cleanupTicker: time.NewTicker(24 * time.Hour), // Cleanup daily
		dbPath:        dbPath,
	}, nil
}

// Start begins background workers (write loop, cleanup loop).
func (b *SQLiteBackend) Start() error {
	b.wg.Add(2)
	go b.writeLoop()
	go b.cleanupLoop()
	return nil
}

// Stop gracefully shuts down the backend, flushing pending writes.
func (b *SQLiteBackend) Stop() error {
	if b == nil {
		return nil
	}

	var err error
	b.stopOnce.Do(func() {
		// Signal stop to all goroutines
		close(b.stopChan)

		// Stop tickers
		b.flushTicker.Stop()
		b.cleanupTicker.Stop()

		// Wait for workers to finish
		b.wg.Wait()

		// Close database
		if b.db != nil {
			err = b.db.Close()
		}
	})

	return err
}

// Enqueue adds a usage record to the write queue.
// This method is non-blocking and safe for high-throughput use.
func (b *SQLiteBackend) Enqueue(record UsageRecord) {
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
func (b *SQLiteBackend) Flush(ctx context.Context) error {
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
func (b *SQLiteBackend) QueryGlobalStats(ctx context.Context, since time.Time) (*AggregatedStats, error) {
	row := b.db.QueryRowContext(ctx, `
		SELECT 
			COUNT(*),
			SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END),
			SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END),
			COALESCE(SUM(total_tokens), 0)
		FROM usage_records
		WHERE requested_at >= ?
	`, since)

	var stats AggregatedStats
	if err := row.Scan(&stats.TotalRequests, &stats.SuccessCount, &stats.FailureCount, &stats.TotalTokens); err != nil {
		return nil, fmt.Errorf("failed to query global stats: %w", err)
	}
	return &stats, nil
}

// QueryDailyStats returns per-day statistics since the given time.
func (b *SQLiteBackend) QueryDailyStats(ctx context.Context, since time.Time) ([]DailyStats, error) {
	rows, err := b.db.QueryContext(ctx, `
		SELECT 
			COALESCE(DATE(requested_at), DATE('now')) as day,
			COUNT(*) as requests,
			COALESCE(SUM(total_tokens), 0) as tokens
		FROM usage_records
		WHERE requested_at >= ?
		GROUP BY DATE(requested_at)
		HAVING day IS NOT NULL
		ORDER BY day
	`, since)
	if err != nil {
		return nil, fmt.Errorf("failed to query daily stats: %w", err)
	}
	defer rows.Close()

	var results []DailyStats
	for rows.Next() {
		var d DailyStats
		var dayStr sql.NullString
		if err := rows.Scan(&dayStr, &d.Requests, &d.Tokens); err != nil {
			return nil, err
		}
		if dayStr.Valid && dayStr.String != "" {
			d.Day = dayStr.String
			results = append(results, d)
		}
	}
	return results, rows.Err()
}

// QueryHourlyStats returns per-hour-of-day statistics since the given time.
func (b *SQLiteBackend) QueryHourlyStats(ctx context.Context, since time.Time) ([]HourlyStats, error) {
	rows, err := b.db.QueryContext(ctx, `
		SELECT 
			CAST(strftime('%H', requested_at) AS INTEGER) as hour,
			COUNT(*) as requests,
			COALESCE(SUM(total_tokens), 0) as tokens
		FROM usage_records
		WHERE requested_at >= ?
		GROUP BY hour
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

func (b *SQLiteBackend) QueryProviderStats(ctx context.Context, since time.Time) ([]ProviderStats, error) {
	rows, err := b.db.QueryContext(ctx, `
		SELECT 
			COALESCE(NULLIF(provider, ''), 'unknown') as provider,
			COUNT(*) as requests,
			SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens,
			COUNT(DISTINCT NULLIF(auth_id, '')) as account_count,
			GROUP_CONCAT(DISTINCT NULLIF(model, '')) as models
		FROM usage_records
		WHERE requested_at >= ?
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
		var modelsStr sql.NullString
		if err := rows.Scan(
			&ps.Provider, &ps.Requests, &ps.SuccessCount, &ps.FailureCount,
			&ps.InputTokens, &ps.OutputTokens, &ps.ReasoningTokens, &ps.TotalTokens,
			&ps.AccountCount, &modelsStr,
		); err != nil {
			return nil, err
		}
		if modelsStr.Valid && modelsStr.String != "" {
			ps.Models = strings.Split(modelsStr.String, ",")
		}
		results = append(results, ps)
	}
	return results, rows.Err()
}

func (b *SQLiteBackend) QueryAuthStats(ctx context.Context, since time.Time) ([]AuthStats, error) {
	rows, err := b.db.QueryContext(ctx, `
		SELECT 
			COALESCE(NULLIF(provider, ''), 'unknown') as provider,
			COALESCE(NULLIF(auth_id, ''), 'unknown') as auth_id,
			COUNT(*) as requests,
			SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		FROM usage_records
		WHERE requested_at >= ?
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

func (b *SQLiteBackend) QueryModelStats(ctx context.Context, since time.Time) ([]ModelStats, error) {
	rows, err := b.db.QueryContext(ctx, `
		SELECT 
			COALESCE(NULLIF(model, ''), 'unknown') as model,
			COALESCE(NULLIF(provider, ''), 'unknown') as provider,
			COUNT(*) as requests,
			SUM(CASE WHEN failed = 0 THEN 1 ELSE 0 END) as success_count,
			SUM(CASE WHEN failed = 1 THEN 1 ELSE 0 END) as failure_count,
			COALESCE(SUM(input_tokens), 0) as input_tokens,
			COALESCE(SUM(output_tokens), 0) as output_tokens,
			COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
			COALESCE(SUM(total_tokens), 0) as total_tokens
		FROM usage_records
		WHERE requested_at >= ?
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
func (b *SQLiteBackend) Cleanup(ctx context.Context, before time.Time) (int64, error) {
	result, err := b.db.ExecContext(ctx, `
		DELETE FROM usage_records WHERE requested_at < ?
	`, before)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

// DBPath returns the filesystem path to the SQLite database.
func (b *SQLiteBackend) DBPath() string {
	if b == nil {
		return ""
	}
	return b.dbPath
}

// writeLoop continuously reads from the record channel and writes in batches.
func (b *SQLiteBackend) writeLoop() {
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

// writeBatch writes a batch of records to the database in a single transaction.
func (b *SQLiteBackend) writeBatch(ctx context.Context, records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}

	tx, err := b.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO usage_records (
			provider, model, api_key, auth_id, auth_index, source,
			requested_at, failed, input_tokens, output_tokens,
			reasoning_tokens, cached_tokens, total_tokens,
			audio_tokens, cache_creation_input_tokens, cache_read_input_tokens, tool_use_prompt_tokens
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	for _, record := range records {
		_, err := stmt.ExecContext(ctx,
			record.Provider,
			record.Model,
			record.APIKey,
			record.AuthID,
			record.AuthIndex,
			record.Source,
			record.RequestedAt,
			record.Failed,
			record.InputTokens,
			record.OutputTokens,
			record.ReasoningTokens,
			record.CachedTokens,
			record.TotalTokens,
			record.AudioTokens,
			record.CacheCreationInputTokens,
			record.CacheReadInputTokens,
			record.ToolUsePromptTokens,
		)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("failed to insert record: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	return nil
}

// cleanupLoop periodically removes old records based on retention policy.
func (b *SQLiteBackend) cleanupLoop() {
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
