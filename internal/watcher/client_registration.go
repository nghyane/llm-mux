package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/util"
)

// reloadClients performs a full scan and reload of all clients.
func (w *Watcher) reloadClients(rescanAuth bool, affectedOAuthProviders []string) {
	log.Debugf("starting full client load process")

	w.clientsMutex.RLock()
	cfg := w.config
	w.clientsMutex.RUnlock()

	if cfg == nil {
		log.Error("config is nil, cannot reload clients")
		return
	}

	if len(affectedOAuthProviders) > 0 {
		w.clientsMutex.Lock()
		if w.currentAuths != nil {
			filtered := make(map[string]*provider.Auth, len(w.currentAuths))
			for id, auth := range w.currentAuths {
				if auth == nil {
					continue
				}
				provider := strings.ToLower(strings.TrimSpace(auth.Provider))
				if _, match := matchProvider(provider, affectedOAuthProviders); match {
					continue
				}
				filtered[id] = auth
			}
			w.currentAuths = filtered
			log.Debugf("applying oauth-excluded-models to providers %v", affectedOAuthProviders)
		} else {
			w.currentAuths = nil
		}
		w.clientsMutex.Unlock()
	}

	geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount := BuildAPIKeyClients(cfg)
	totalAPIKeyClients := geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount
	log.Debugf("loaded %d API key clients", totalAPIKeyClients)

	var authFileCount int
	if rescanAuth {
		// Load file-based clients when explicitly requested (startup or authDir change)
		authFileCount = w.loadFileClients(cfg)
		log.Debugf("loaded %d file-based clients", authFileCount)
	} else {
		// Preserve existing auth hashes and only report current known count to avoid redundant scans.
		w.clientsMutex.RLock()
		authFileCount = len(w.lastAuthHashes)
		w.clientsMutex.RUnlock()
		log.Debugf("skipping auth directory rescan; retaining %d existing auth files", authFileCount)
	}

	if rescanAuth {
		// Build hash map WITHOUT holding lock (do all file I/O first)
		newAuthHashes := make(map[string]string)
		if resolvedAuthDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir); errResolveAuthDir != nil {
			log.Errorf("failed to resolve auth directory for hash cache: %v", errResolveAuthDir)
		} else if resolvedAuthDir != "" {
			_ = filepath.Walk(resolvedAuthDir, func(path string, info fs.FileInfo, err error) error {
				if err != nil {
					return nil
				}
				if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") && info.Name() != ".llm-mux-manifest.json" {
					if data, errReadFile := os.ReadFile(path); errReadFile == nil && len(data) > 0 {
						sum := sha256.Sum256(data)
						newAuthHashes[path] = hex.EncodeToString(sum[:])
					}
				}
				return nil
			})
		}

		// Take lock only to swap/update the map atomically
		w.clientsMutex.Lock()
		w.lastAuthHashes = newAuthHashes
		w.clientsMutex.Unlock()
	}

	totalNewClients := authFileCount + geminiAPIKeyCount + vertexCompatAPIKeyCount + claudeAPIKeyCount + codexAPIKeyCount + openAICompatCount

	// Ensure consumers observe the new configuration before auth updates dispatch.
	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback before auth refresh")
		w.reloadCallback(cfg)
	}

	w.refreshAuthState()

	log.Infof("full client load complete - %d clients (%d auth files + %d Gemini API keys + %d Vertex API keys + %d Claude API keys + %d Codex keys + %d OpenAI-compat)",
		totalNewClients,
		authFileCount,
		geminiAPIKeyCount,
		vertexCompatAPIKeyCount,
		claudeAPIKeyCount,
		codexAPIKeyCount,
		openAICompatCount,
	)
}

// addOrUpdateClient handles the addition or update of a single client.
func (w *Watcher) addOrUpdateClient(path string) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		log.Errorf("failed to read auth file %s: %v", filepath.Base(path), errRead)
		return
	}
	if len(data) == 0 {
		log.Debugf("ignoring empty auth file: %s", filepath.Base(path))
		return
	}

	sum := sha256.Sum256(data)
	curHash := hex.EncodeToString(sum[:])

	w.clientsMutex.Lock()

	cfg := w.config
	if cfg == nil {
		log.Error("config is nil, cannot add or update client")
		w.clientsMutex.Unlock()
		return
	}
	if prev, ok := w.lastAuthHashes[path]; ok && prev == curHash {
		log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(path))
		w.clientsMutex.Unlock()
		return
	}

	// Update hash cache
	w.lastAuthHashes[path] = curHash

	w.clientsMutex.Unlock() // Unlock before the callback

	w.refreshAuthState()

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after add/update")
		w.reloadCallback(cfg)
	}
	w.persistAuthAsync(fmt.Sprintf("Sync auth %s", filepath.Base(path)), path)
}

// removeClient handles the removal of a single client.
func (w *Watcher) removeClient(path string) {
	w.clientsMutex.Lock()

	cfg := w.config
	delete(w.lastAuthHashes, path)

	w.clientsMutex.Unlock() // Release the lock before the callback

	w.refreshAuthState()

	if w.reloadCallback != nil {
		log.Debugf("triggering server update callback after removal")
		w.reloadCallback(cfg)
	}
	w.persistAuthAsync(fmt.Sprintf("Remove auth %s", filepath.Base(path)), path)
}

// loadFileClients scans the auth directory and creates clients from .json files.
func (w *Watcher) loadFileClients(cfg *config.Config) int {
	authFileCount := 0
	successfulAuthCount := 0

	authDir, errResolveAuthDir := util.ResolveAuthDir(cfg.AuthDir)
	if errResolveAuthDir != nil {
		log.Errorf("failed to resolve auth directory: %v", errResolveAuthDir)
		return 0
	}
	if authDir == "" {
		return 0
	}

	errWalk := filepath.Walk(authDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			log.Debugf("error accessing path %s: %v", path, err)
			return err
		}
		if !info.IsDir() && strings.HasSuffix(strings.ToLower(info.Name()), ".json") && info.Name() != ".llm-mux-manifest.json" {
			authFileCount++
			log.Debugf("processing auth file %d: %s", authFileCount, filepath.Base(path))
			// Count readable JSON files as successful auth entries
			if data, errCreate := os.ReadFile(path); errCreate == nil && len(data) > 0 {
				successfulAuthCount++
			}
		}
		return nil
	})

	if errWalk != nil {
		log.Errorf("error walking auth directory: %v", errWalk)
	}
	log.Debugf("auth directory scan complete - found %d .json files, %d readable", authFileCount, successfulAuthCount)
	return authFileCount
}

// BuildAPIKeyClients counts API key clients from the configuration.
func BuildAPIKeyClients(cfg *config.Config) (int, int, int, int, int) {
	geminiAPIKeyCount := 0
	vertexCompatAPIKeyCount := 0
	claudeAPIKeyCount := 0
	codexAPIKeyCount := 0
	openAICompatCount := 0

	if cfg == nil {
		return 0, 0, 0, 0, 0
	}

	for _, p := range cfg.Providers {
		keys := p.GetAPIKeys()
		keyCount := len(keys)

		switch p.Type {
		case config.ProviderTypeGemini:
			geminiAPIKeyCount += keyCount
		case config.ProviderTypeAnthropic:
			if strings.EqualFold(p.Name, "codex") {
				codexAPIKeyCount += keyCount
			} else {
				claudeAPIKeyCount += keyCount
			}
		case config.ProviderTypeOpenAI:
			openAICompatCount += keyCount
		case config.ProviderTypeVertexCompat:
			vertexCompatAPIKeyCount += keyCount
		}
	}

	return geminiAPIKeyCount, vertexCompatAPIKeyCount, claudeAPIKeyCount, codexAPIKeyCount, openAICompatCount
}
