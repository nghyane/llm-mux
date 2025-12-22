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

	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

// UsageRecord represents a single usage record for persistence.
type UsageRecord struct {
	Provider                 string
	Model                    string
	APIKey                   string
	AuthID                   string
	AuthIndex                uint64
	Source                   string
	RequestedAt              time.Time
	Failed                   bool
	InputTokens              int64
	OutputTokens             int64
	ReasoningTokens          int64
	CachedTokens             int64
	TotalTokens              int64
	AudioTokens              int64
	CacheCreationInputTokens int64
	CacheReadInputTokens     int64
	ToolUsePromptTokens      int64
}

// Persister handles SQLite persistence for usage records with async batched writes.
type Persister struct {
	db            *sql.DB
	recordChan    chan UsageRecord
	flushTicker   *time.Ticker
	wg            sync.WaitGroup
	stopOnce      sync.Once
	stopChan      chan struct{}
	batchSize     int
	flushInterval time.Duration
	retentionDays int
	cleanupTicker *time.Ticker
	dbPath        string
}

const (
	defaultBatchSize         = 100
	defaultFlushInterval     = 5 * time.Second
	defaultRetentionDays     = 30
	defaultChannelBufferSize = 1000
)

// NewPersister initializes a new SQLite persister with the given configuration.
// Returns nil if dbPath is empty or creation fails.
func NewPersister(dbPath string, batchSize, flushIntervalSecs, retentionDays int) (*Persister, error) {
	if dbPath == "" {
		return nil, fmt.Errorf("database path cannot be empty")
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

	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	if flushIntervalSecs <= 0 {
		flushIntervalSecs = int(defaultFlushInterval.Seconds())
	}
	if retentionDays <= 0 {
		retentionDays = defaultRetentionDays
	}

	p := &Persister{
		db:            db,
		recordChan:    make(chan UsageRecord, defaultChannelBufferSize),
		flushTicker:   time.NewTicker(time.Duration(flushIntervalSecs) * time.Second),
		stopChan:      make(chan struct{}),
		batchSize:     batchSize,
		flushInterval: time.Duration(flushIntervalSecs) * time.Second,
		retentionDays: retentionDays,
		cleanupTicker: time.NewTicker(24 * time.Hour), // Cleanup daily
		dbPath:        dbPath,
	}

	// Start background workers
	p.wg.Add(2)
	go p.writeLoop()
	go p.cleanupLoop()

	return p, nil
}

// initSchema creates the usage_records table and indexes if they don't exist.
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

	// Run migrations to add new columns to existing tables
	return migrateSchema(db)
}

// migrateSchema adds new columns to existing tables.
// Uses error-based detection: "duplicate column name" errors are ignored.
// This is simpler and more efficient than querying PRAGMA table_info.
func migrateSchema(db *sql.DB) error {
	// Columns added after initial schema release.
	// Each entry is a complete column definition for ALTER TABLE ADD COLUMN.
	migrations := []string{
		"audio_tokens INTEGER NOT NULL DEFAULT 0",
		"cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0",
		"cache_read_input_tokens INTEGER NOT NULL DEFAULT 0",
		"tool_use_prompt_tokens INTEGER NOT NULL DEFAULT 0",
	}

	for _, colDef := range migrations {
		_, err := db.Exec("ALTER TABLE usage_records ADD COLUMN " + colDef)
		if err != nil {
			// SQLite returns "duplicate column name: X" if column already exists
			if strings.Contains(err.Error(), "duplicate column name") {
				continue
			}
			return fmt.Errorf("migration failed for [%s]: %w", colDef, err)
		}
		// Extract column name for logging (first word before space)
		colName := strings.Fields(colDef)[0]
		log.Infof("Added column %s to usage_records table", colName)
	}

	return nil
}

// Enqueue adds a usage record to the persistence queue.
// Non-blocking; drops records if queue is full to prevent blocking.
func (p *Persister) Enqueue(record UsageRecord) {
	if p == nil {
		return
	}
	select {
	case p.recordChan <- record:
		// Successfully enqueued
	default:
		// Channel full, drop record with warning
		log.Warnf("Usage persistence queue full, dropping record for %s/%s", record.Provider, record.Model)
	}
}

// writeLoop continuously reads from the record channel and writes in batches.
func (p *Persister) writeLoop() {
	defer p.wg.Done()

	batch := make([]UsageRecord, 0, p.batchSize)

	flush := func() {
		if len(batch) == 0 {
			return
		}
		if err := p.writeBatch(batch); err != nil {
			log.Errorf("Failed to write usage batch: %v", err)
		}
		batch = batch[:0] // Clear batch
	}

	for {
		select {
		case record := <-p.recordChan:
			batch = append(batch, record)
			if len(batch) >= p.batchSize {
				flush()
			}
		case <-p.flushTicker.C:
			flush()
		case <-p.stopChan:
			// Drain remaining records
			for {
				select {
				case record := <-p.recordChan:
					batch = append(batch, record)
					if len(batch) >= p.batchSize {
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
func (p *Persister) writeBatch(records []UsageRecord) error {
	if len(records) == 0 {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	tx, err := p.db.BeginTx(ctx, nil)
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
func (p *Persister) cleanupLoop() {
	defer p.wg.Done()

	for {
		select {
		case <-p.cleanupTicker.C:
			if err := p.cleanup(); err != nil {
				log.Errorf("Failed to cleanup old usage records: %v", err)
			}
		case <-p.stopChan:
			return
		}
	}
}

// cleanup removes records older than the retention period.
func (p *Persister) cleanup() error {
	cutoffTime := time.Now().AddDate(0, 0, -p.retentionDays)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
	defer cancel()

	result, err := p.db.ExecContext(ctx, `
		DELETE FROM usage_records WHERE requested_at < ?
	`, cutoffTime)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		log.Infof("Cleaned up %d usage records older than %d days", rowsAffected, p.retentionDays)
	}

	return nil
}

// Stop gracefully shuts down the persister, flushing pending writes.
func (p *Persister) Stop() error {
	if p == nil {
		return nil
	}

	var err error
	p.stopOnce.Do(func() {
		// Signal stop to all goroutines
		close(p.stopChan)

		// Stop tickers
		p.flushTicker.Stop()
		p.cleanupTicker.Stop()

		// Wait for workers to finish
		p.wg.Wait()

		// Close database
		if p.db != nil {
			err = p.db.Close()
		}
	})

	return err
}

// DBPath returns the filesystem path to the SQLite database.
func (p *Persister) DBPath() string {
	if p == nil {
		return ""
	}
	return p.dbPath
}
