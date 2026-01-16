package store

import (
	"net/url"
	"strings"
)

// StoreType identifies the backend storage mechanism.
type StoreType string

const (
	// TypeNone indicates file-based storage (no remote store).
	TypeNone StoreType = ""
	// TypePostgres indicates PostgreSQL-backed storage.
	TypePostgres StoreType = "postgres"
	// TypeObject indicates S3-compatible object storage.
	TypeObject StoreType = "object"
	// TypeGit indicates git repository-backed storage.
	TypeGit StoreType = "git"
)

// GitStoreConfig captures configuration for git-backed storage.
type GitStoreConfig struct {
	RemoteURL string
	Username  string
	Password  string
	LocalPath string
	// DisableAutoPush prevents automatic git push on every change.
	// When true, changes are committed locally but not automatically pushed to remote.
	// Default: false (auto-push enabled for backward compatibility).
	DisableAutoPush bool
}

// StoreConfig unifies configuration for all store backends.
type StoreConfig struct {
	Type     StoreType
	Postgres PostgresStoreConfig
	Git      GitStoreConfig
	Object   ObjectStoreConfig

	// Target paths for transparent sync layer.
	// When set, stores sync to these paths instead of creating their own workspace.
	TargetConfigPath string // e.g., ~/.config/llm-mux/config.yaml
	TargetAuthDir    string // e.g., ~/.config/llm-mux/auth
}

// LookupEnvFunc is a function that looks up environment variables.
// It accepts multiple keys and returns the first non-empty value found.
type LookupEnvFunc func(keys ...string) (string, bool)

// ParseFromEnv builds a StoreConfig from environment variables.
// The lookupEnv function is injected to avoid circular dependencies with the env package.
func ParseFromEnv(lookupEnv LookupEnvFunc) StoreConfig {
	cfg := StoreConfig{}

	storeType, _ := lookupEnv("LLM_MUX_STORE_TYPE")
	storeType = strings.ToLower(strings.TrimSpace(storeType))

	if storeType == "postgres" || storeType == "pg" {
		cfg.Type = TypePostgres
	}
	if value, ok := lookupEnv("LLM_MUX_PGSTORE_DSN", "PGSTORE_DSN"); ok {
		cfg.Type = TypePostgres
		cfg.Postgres.DSN = value
	}
	if cfg.Type == TypePostgres {
		if value, ok := lookupEnv("LLM_MUX_PGSTORE_SCHEMA", "PGSTORE_SCHEMA"); ok {
			cfg.Postgres.Schema = value
		}
		return cfg
	}

	if storeType == "git" {
		cfg.Type = TypeGit
	}
	if value, ok := lookupEnv("LLM_MUX_GITSTORE_URL", "GITSTORE_GIT_URL"); ok {
		cfg.Type = TypeGit
		cfg.Git.RemoteURL = value
	}
	if cfg.Type == TypeGit {
		if value, ok := lookupEnv("LLM_MUX_GITSTORE_USERNAME", "GITSTORE_GIT_USERNAME"); ok {
			cfg.Git.Username = value
		}
		if value, ok := lookupEnv("LLM_MUX_GITSTORE_TOKEN", "GITSTORE_GIT_TOKEN"); ok {
			cfg.Git.Password = value
		}
		// Check if auto-push should be disabled
		if value, ok := lookupEnv("LLM_MUX_GITSTORE_DISABLE_AUTO_PUSH", "GITSTORE_DISABLE_AUTO_PUSH"); ok {
			disableValue := strings.ToLower(strings.TrimSpace(value))
			cfg.Git.DisableAutoPush = disableValue == "true" || disableValue == "1" || disableValue == "yes"
		}
		return cfg
	}

	if storeType == "s3" || storeType == "object" || storeType == "minio" {
		cfg.Type = TypeObject
	}
	if value, ok := lookupEnv("LLM_MUX_OBJECTSTORE_ENDPOINT", "OBJECTSTORE_ENDPOINT"); ok {
		cfg.Type = TypeObject
		cfg.Object.Endpoint = value
	}
	if cfg.Type == TypeObject {
		if value, ok := lookupEnv("LLM_MUX_OBJECTSTORE_ACCESS_KEY", "OBJECTSTORE_ACCESS_KEY"); ok {
			cfg.Object.AccessKey = value
		}
		if value, ok := lookupEnv("LLM_MUX_OBJECTSTORE_SECRET_KEY", "OBJECTSTORE_SECRET_KEY"); ok {
			cfg.Object.SecretKey = value
		}
		if value, ok := lookupEnv("LLM_MUX_OBJECTSTORE_BUCKET", "OBJECTSTORE_BUCKET"); ok {
			cfg.Object.Bucket = value
		}
		cfg.Object.PathStyle = true
		cfg.Object.UseSSL = true
		endpoint := strings.TrimSpace(cfg.Object.Endpoint)
		if strings.Contains(endpoint, "://") {
			if parsed, err := url.Parse(endpoint); err == nil {
				switch strings.ToLower(parsed.Scheme) {
				case "http":
					cfg.Object.UseSSL = false
				case "https":
					cfg.Object.UseSSL = true
				}
				cfg.Object.Endpoint = parsed.Host
				if parsed.Path != "" && parsed.Path != "/" {
					cfg.Object.Endpoint = strings.TrimSuffix(parsed.Host+parsed.Path, "/")
				}
			}
		}
		cfg.Object.Endpoint = strings.TrimRight(cfg.Object.Endpoint, "/")
		return cfg
	}

	return cfg
}

// IsConfigured returns true if any store backend is configured.
func (c StoreConfig) IsConfigured() bool {
	return c.Type != TypeNone
}

// IsPostgres returns true if PostgreSQL backend is configured.
func (c StoreConfig) IsPostgres() bool {
	return c.Type == TypePostgres
}

// IsGit returns true if Git backend is configured.
func (c StoreConfig) IsGit() bool {
	return c.Type == TypeGit
}

// IsObject returns true if object storage backend is configured.
func (c StoreConfig) IsObject() bool {
	return c.Type == TypeObject
}
