package store

import (
	"context"
	"fmt"
	"time"

	"github.com/nghyane/llm-mux/internal/provider"
)

const bootstrapTimeout = 30 * time.Second

// StoreResult holds the initialized store and its resolved paths.
type StoreResult struct {
	Store      provider.Store
	ConfigPath string
	AuthDir    string
	StoreType  StoreType
}

// NewStore creates and initializes a store based on the provided configuration.
// For TypeNone, it returns a nil Store indicating file-based fallback.
func NewStore(ctx context.Context, cfg StoreConfig) (*StoreResult, error) {
	if cfg.TargetConfigPath == "" || cfg.TargetAuthDir == "" {
		return nil, fmt.Errorf("store: TargetConfigPath and TargetAuthDir are required")
	}

	switch cfg.Type {
	case TypePostgres:
		return newPostgresStore(ctx, cfg.Postgres, cfg.TargetConfigPath, cfg.TargetAuthDir)
	case TypeObject:
		return newObjectStore(ctx, cfg.Object, cfg.TargetConfigPath, cfg.TargetAuthDir)
	case TypeGit:
		return newGitStore(cfg.Git, cfg.TargetConfigPath, cfg.TargetAuthDir)
	case TypeNone:
		return &StoreResult{
			Store:      nil,
			ConfigPath: cfg.TargetConfigPath,
			AuthDir:    cfg.TargetAuthDir,
			StoreType:  TypeNone,
		}, nil
	default:
		return nil, fmt.Errorf("store: unknown store type: %s", cfg.Type)
	}
}

func newPostgresStore(ctx context.Context, cfg PostgresStoreConfig, configPath, authDir string) (*StoreResult, error) {
	store, err := NewPostgresStore(ctx, cfg, configPath, authDir)
	if err != nil {
		return nil, fmt.Errorf("store: create postgres store: %w", err)
	}

	bootstrapCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()

	if err := store.Bootstrap(bootstrapCtx); err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("store: bootstrap postgres store: %w", err)
	}

	return &StoreResult{
		Store:      store,
		ConfigPath: store.ConfigPath(),
		AuthDir:    store.AuthDir(),
		StoreType:  TypePostgres,
	}, nil
}

func newObjectStore(ctx context.Context, cfg ObjectStoreConfig, configPath, authDir string) (*StoreResult, error) {
	store, err := NewObjectTokenStore(cfg, configPath, authDir)
	if err != nil {
		return nil, fmt.Errorf("store: create object store: %w", err)
	}

	bootstrapCtx, cancel := context.WithTimeout(ctx, bootstrapTimeout)
	defer cancel()

	if err := store.Bootstrap(bootstrapCtx); err != nil {
		return nil, fmt.Errorf("store: bootstrap object store: %w", err)
	}

	return &StoreResult{
		Store:      store,
		ConfigPath: store.ConfigPath(),
		AuthDir:    store.AuthDir(),
		StoreType:  TypeObject,
	}, nil
}

func newGitStore(cfg GitStoreConfig, configPath, authDir string) (*StoreResult, error) {
	store := NewGitTokenStore(cfg.RemoteURL, cfg.Username, cfg.Password, configPath, authDir, cfg.DisableAutoPush)

	if err := store.EnsureRepository(); err != nil {
		return nil, fmt.Errorf("store: ensure git repository: %w", err)
	}

	return &StoreResult{
		Store:      store,
		ConfigPath: store.ConfigPath(),
		AuthDir:    store.AuthDir(),
		StoreType:  TypeGit,
	}, nil
}
