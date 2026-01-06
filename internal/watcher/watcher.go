// Package watcher provides file system monitoring functionality for the CLI Proxy API.
// It watches configuration files and authentication directories for changes,
// automatically reloading clients and configuration when files are modified.
// The package handles cross-platform file system events and supports hot-reloading.
package watcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/nghyane/llm-mux/internal/auth/login"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"golang.org/x/sync/singleflight"
	"gopkg.in/yaml.v3"
)

func matchProvider(provider string, targets []string) (string, bool) {
	p := strings.ToLower(strings.TrimSpace(provider))
	for _, t := range targets {
		if strings.EqualFold(p, strings.TrimSpace(t)) {
			return p, true
		}
	}
	return p, false
}

// storePersister captures persistence-capable token store methods used by the watcher.
type storePersister interface {
	PersistConfig(ctx context.Context) error
	PersistAuthFiles(ctx context.Context, message string, paths ...string) error
}

type authDirProvider interface {
	AuthDir() string
}

// Watcher manages file watching for configuration and authentication files
type Watcher struct {
	configPath        string
	authDir           string
	config            *config.Config
	clientsMutex      sync.RWMutex
	configReloadMu    sync.Mutex
	configReloadTimer *time.Timer
	reloadCallback    func(*config.Config)
	watcher           *fsnotify.Watcher
	lastAuthHashes    map[string]string
	lastConfigHash    string
	authQueue         chan<- AuthUpdate
	currentAuths      map[string]*provider.Auth
	runtimeAuths      map[string]*provider.Auth
	dispatchMu        sync.Mutex
	dispatchCond      *sync.Cond
	pendingUpdates    map[string]AuthUpdate
	pendingOrder      []string
	dispatchCancel    context.CancelFunc
	storePersister    storePersister
	mirroredAuthDir   string
	oldConfigYaml     []byte
	// pendingWrites tracks files being written by the application itself.
	// Key: absolute file path, Value: expiry time for the pending write marker.
	// This prevents the watcher from reacting to its own writes.
	pendingWritesMu sync.RWMutex
	pendingWrites   map[string]time.Time
	// persistGroup deduplicates concurrent config persistence calls using singleflight
	persistGroup singleflight.Group
}

type stableIDGenerator struct {
	counters map[string]int
}

func newStableIDGenerator() *stableIDGenerator {
	return &stableIDGenerator{counters: make(map[string]int)}
}

func (g *stableIDGenerator) next(kind string, parts ...string) (string, string) {
	if g == nil {
		return kind + ":000000000000", "000000000000"
	}
	hasher := sha256.New()
	hasher.Write([]byte(kind))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		hasher.Write([]byte{0})
		hasher.Write([]byte(trimmed))
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if len(digest) < 12 {
		digest = fmt.Sprintf("%012s", digest)
	}
	short := digest[:12]
	key := kind + ":" + short
	index := g.counters[key]
	g.counters[key] = index + 1
	if index > 0 {
		short = fmt.Sprintf("%s-%d", short, index)
	}
	return fmt.Sprintf("%s:%s", kind, short), short
}

// AuthUpdateAction represents the type of change detected in auth sources.
type AuthUpdateAction string

const (
	AuthUpdateActionAdd    AuthUpdateAction = "add"
	AuthUpdateActionModify AuthUpdateAction = "modify"
	AuthUpdateActionDelete AuthUpdateAction = "delete"
)

// AuthUpdate describes an incremental change to auth configuration.
type AuthUpdate struct {
	Action AuthUpdateAction
	ID     string
	Auth   *provider.Auth
}

const (
	// replaceCheckDelay is a short delay to allow atomic replace (rename) to settle
	// before deciding whether a Remove event indicates a real deletion.
	replaceCheckDelay    = 50 * time.Millisecond
	configReloadDebounce = 150 * time.Millisecond
	pendingWriteTTL      = 2 * time.Second
)

// NewWatcher creates a new file watcher instance
func NewWatcher(configPath, authDir string, reloadCallback func(*config.Config)) (*Watcher, error) {
	watcher, errNewWatcher := fsnotify.NewWatcher()
	if errNewWatcher != nil {
		return nil, errNewWatcher
	}
	w := &Watcher{
		configPath:     configPath,
		authDir:        authDir,
		reloadCallback: reloadCallback,
		watcher:        watcher,
		lastAuthHashes: make(map[string]string),
		pendingWrites:  make(map[string]time.Time),
	}
	w.dispatchCond = sync.NewCond(&w.dispatchMu)
	if store := login.GetTokenStore(); store != nil {
		if persister, ok := store.(storePersister); ok {
			w.storePersister = persister
			log.Debug("persistence-capable token store detected; watcher will propagate persisted changes")
		}
		if provider, ok := store.(authDirProvider); ok {
			if fixed := strings.TrimSpace(provider.AuthDir()); fixed != "" {
				w.mirroredAuthDir = fixed
				log.Debugf("mirrored auth directory locked to %s", fixed)
			}
		}
	}
	return w, nil
}

// Start begins watching the configuration file and authentication directory
func (w *Watcher) Start(ctx context.Context) error {
	// Watch the config file only if it exists (zero-config mode support)
	if _, err := os.Stat(w.configPath); err == nil {
		if errAddConfig := w.watcher.Add(w.configPath); errAddConfig != nil {
			log.Errorf("failed to watch config file %s: %v", w.configPath, errAddConfig)
			return errAddConfig
		}
		log.Debugf("watching config file: %s", w.configPath)
	} else {
		log.Infof("config file %s not found, running with defaults (use --init to create)", w.configPath)
	}

	// Ensure auth directory exists and watch it
	if err := os.MkdirAll(w.authDir, 0o700); err != nil {
		log.Errorf("failed to create auth directory %s: %v", w.authDir, err)
		return err
	}
	if errAddAuthDir := w.watcher.Add(w.authDir); errAddAuthDir != nil {
		log.Errorf("failed to watch auth directory %s: %v", w.authDir, errAddAuthDir)
		return errAddAuthDir
	}
	log.Debugf("watching auth directory: %s", w.authDir)

	// Start the event processing goroutine
	go w.processEvents(ctx)

	// Perform an initial full reload based on current config and auth dir
	w.reloadClients(true, nil)
	return nil
}

// Stop stops the file watcher
func (w *Watcher) Stop() error {
	w.stopDispatch()
	w.stopConfigReloadTimer()
	return w.watcher.Close()
}

// SetConfig updates the current configuration
func (w *Watcher) SetConfig(cfg *config.Config) {
	w.clientsMutex.Lock()
	defer w.clientsMutex.Unlock()
	w.config = cfg
	oldConfigYaml, _ := yaml.Marshal(cfg)
	w.oldConfigYaml = oldConfigYaml
}

// processEvents handles file system events
func (w *Watcher) processEvents(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handleEvent(event)
		case errWatch, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Errorf("file watcher error: %v", errWatch)
		}
	}
}

func (w *Watcher) authFileUnchanged(path string) (bool, error) {
	data, errRead := os.ReadFile(path)
	if errRead != nil {
		return false, errRead
	}
	if len(data) == 0 {
		return false, nil
	}
	sum := sha256.Sum256(data)
	curHash := hex.EncodeToString(sum[:])

	w.clientsMutex.RLock()
	prevHash, ok := w.lastAuthHashes[path]
	w.clientsMutex.RUnlock()
	if ok && prevHash == curHash {
		return true, nil
	}
	return false, nil
}

func (w *Watcher) isKnownAuthFile(path string) bool {
	w.clientsMutex.RLock()
	defer w.clientsMutex.RUnlock()
	_, ok := w.lastAuthHashes[path]
	return ok
}

// handleEvent processes individual file system events
func (w *Watcher) handleEvent(event fsnotify.Event) {
	// Filter only relevant events: config file or auth-dir JSON files.
	configOps := fsnotify.Write | fsnotify.Create | fsnotify.Rename
	isConfigEvent := event.Name == w.configPath && event.Op&configOps != 0
	authOps := fsnotify.Create | fsnotify.Write | fsnotify.Remove | fsnotify.Rename
	isAuthJSON := strings.HasPrefix(event.Name, w.authDir) && strings.HasSuffix(event.Name, ".json") && event.Op&authOps != 0
	if !isConfigEvent && !isAuthJSON {
		return
	}
	if filepath.Base(event.Name) == ".llm-mux-manifest.json" {
		return
	}

	if isAuthJSON && w.isPendingWrite(event.Name) {
		log.Debugf("skipping self-write event for: %s", filepath.Base(event.Name))
		return
	}

	now := time.Now()
	log.Debugf("file system event detected: %s %s", event.Op.String(), event.Name)

	// Handle config file changes
	if isConfigEvent {
		log.Debugf("config file change details - operation: %s, timestamp: %s", event.Op.String(), now.Format("2006-01-02 15:04:05.000"))
		w.scheduleConfigReload()
		return
	}

	// Handle auth directory changes incrementally (.json only)
	if event.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
		// Atomic replace on some platforms may surface as Rename (or Remove) before the new file is ready.
		// Wait briefly; if the path exists again, treat as an update instead of removal.
		time.Sleep(replaceCheckDelay)
		if _, statErr := os.Stat(event.Name); statErr == nil {
			if unchanged, errSame := w.authFileUnchanged(event.Name); errSame == nil && unchanged {
				log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.Name))
				return
			}
			log.Debugf("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
			w.addOrUpdateClient(event.Name)
			return
		}
		if !w.isKnownAuthFile(event.Name) {
			log.Debugf("ignoring remove for unknown auth file: %s", filepath.Base(event.Name))
			return
		}
		log.Debugf("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		w.removeClient(event.Name)
		return
	}
	if event.Op&(fsnotify.Create|fsnotify.Write) != 0 {
		if unchanged, errSame := w.authFileUnchanged(event.Name); errSame == nil && unchanged {
			log.Debugf("auth file unchanged (hash match), skipping reload: %s", filepath.Base(event.Name))
			return
		}
		log.Debugf("auth file changed (%s): %s, processing incrementally", event.Op.String(), filepath.Base(event.Name))
		w.addOrUpdateClient(event.Name)
	}
}

func (w *Watcher) MarkPendingWrite(path string) {
	if w == nil {
		return
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	w.pendingWritesMu.Lock()
	if w.pendingWrites == nil {
		w.pendingWrites = make(map[string]time.Time)
	}
	w.pendingWrites[absPath] = time.Now().Add(pendingWriteTTL)
	w.pendingWritesMu.Unlock()
}

func (w *Watcher) isPendingWrite(path string) bool {
	if w == nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		absPath = path
	}
	w.pendingWritesMu.Lock()
	defer w.pendingWritesMu.Unlock()
	if w.pendingWrites == nil {
		return false
	}
	expiry, ok := w.pendingWrites[absPath]
	if !ok {
		return false
	}
	if time.Now().After(expiry) {
		delete(w.pendingWrites, absPath)
		return false
	}
	delete(w.pendingWrites, absPath)
	return true
}
