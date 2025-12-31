package util

import (
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/logging"
)

func SetLogLevel(cfg *config.Config) {
	currentLevel := logging.GetLevel()
	var newLevel slog.Level
	if cfg.Debug {
		newLevel = slog.LevelDebug
	} else {
		newLevel = slog.LevelInfo
	}

	if currentLevel != newLevel {
		logging.SetLevel(newLevel)
		slog.Info(fmt.Sprintf("log level changed to %s (debug=%t)", newLevel, cfg.Debug))
	}
}

func ResolveAuthDir(authDir string) (string, error) {
	if authDir == "" {
		return "", nil
	}

	if strings.HasPrefix(authDir, "$XDG_CONFIG_HOME") {
		xdg := os.Getenv("XDG_CONFIG_HOME")
		if xdg == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", fmt.Errorf("resolve auth dir: %w", err)
			}
			xdg = filepath.Join(home, ".config")
		}
		remainder := strings.TrimPrefix(authDir, "$XDG_CONFIG_HOME")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			return filepath.Clean(xdg), nil
		}
		normalized := strings.ReplaceAll(remainder, "\\", "/")
		return filepath.Clean(filepath.Join(xdg, filepath.FromSlash(normalized))), nil
	}

	if strings.HasPrefix(authDir, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve auth dir: %w", err)
		}
		remainder := strings.TrimPrefix(authDir, "~")
		remainder = strings.TrimLeft(remainder, "/\\")
		if remainder == "" {
			return filepath.Clean(home), nil
		}
		normalized := strings.ReplaceAll(remainder, "\\", "/")
		return filepath.Clean(filepath.Join(home, filepath.FromSlash(normalized))), nil
	}
	return filepath.Clean(authDir), nil
}

func CountAuthFiles(authDir string) int {
	dir, err := ResolveAuthDir(authDir)
	if err != nil {
		slog.Debug(fmt.Sprintf("countAuthFiles: failed to resolve auth directory: %v", err))
		return 0
	}
	if dir == "" {
		return 0
	}
	count := 0
	walkErr := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			slog.Debug(fmt.Sprintf("countAuthFiles: error accessing %s: %v", path, err))
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			count++
		}
		return nil
	})
	if walkErr != nil {
		slog.Debug(fmt.Sprintf("countAuthFiles: walk error: %v", walkErr))
	}
	return count
}

func WritablePath() string {
	for _, key := range []string{"WRITABLE_PATH", "writable_path"} {
		if value, ok := os.LookupEnv(key); ok {
			trimmed := strings.TrimSpace(value)
			if trimmed != "" {
				return filepath.Clean(trimmed)
			}
		}
	}
	return ""
}
