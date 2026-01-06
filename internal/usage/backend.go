// Package usage provides usage tracking and persistence backends.
package usage

import (
	"context"
	"fmt"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
)

// Backend defines the persistence contract for usage records.
// Implementations must be safe for concurrent use.
type Backend interface {
	// Enqueue adds a usage record to the write queue.
	// This method is non-blocking and safe for high-throughput use.
	Enqueue(record UsageRecord)

	// Flush forces pending records to be written to storage.
	Flush(ctx context.Context) error

	// QueryGlobalStats returns aggregate statistics since the given time.
	QueryGlobalStats(ctx context.Context, since time.Time) (*AggregatedStats, error)

	// QueryDailyStats returns per-day statistics since the given time.
	QueryDailyStats(ctx context.Context, since time.Time) ([]DailyStats, error)

	// QueryHourlyStats returns per-hour-of-day statistics since the given time.
	QueryHourlyStats(ctx context.Context, since time.Time) ([]HourlyStats, error)

	// QueryProviderStats returns per-provider statistics since the given time.
	QueryProviderStats(ctx context.Context, since time.Time) ([]ProviderStats, error)

	// QueryAuthStats returns per-auth-account statistics since the given time.
	QueryAuthStats(ctx context.Context, since time.Time) ([]AuthStats, error)

	// QueryModelStats returns per-model statistics since the given time.
	QueryModelStats(ctx context.Context, since time.Time) ([]ModelStats, error)

	// Cleanup removes records older than the given time.
	Cleanup(ctx context.Context, before time.Time) (int64, error)

	// Start begins background workers (write loop, cleanup loop).
	Start() error

	// Stop gracefully shuts down the backend, flushing pending writes.
	Stop() error
}

// BackendConfig holds parameters for backend initialization.
type BackendConfig struct {
	// DSN is the database connection string (sqlite://... or postgres://...).
	DSN string

	// BatchSize is the number of records to batch before writing.
	BatchSize int

	// FlushInterval is how often to flush pending writes.
	FlushInterval time.Duration

	// RetentionDays is how many days of records to keep.
	RetentionDays int
}

// NewBackend creates the appropriate backend based on DSN configuration.
func NewBackend(cfg BackendConfig) (Backend, error) {
	parsed, err := config.ParseDSN(cfg.DSN)
	if err != nil {
		return nil, err
	}
	if parsed == nil {
		return nil, fmt.Errorf("DSN is required (use sqlite:// or postgres://)")
	}

	switch parsed.Backend {
	case "postgres":
		return NewPostgresBackend(parsed.URL, cfg)
	case "sqlite":
		return NewSQLiteBackend(parsed.Path, cfg)
	default:
		return nil, fmt.Errorf("unknown backend type: %q", parsed.Backend)
	}
}
