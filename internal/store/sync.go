package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"time"

	"github.com/nghyane/llm-mux/internal/json"
)

// ManifestFileName is the name of the manifest file used to track remote-synced files.
const ManifestFileName = ".llm-mux-manifest.json"

// SyncManifest tracks which files are managed by a remote store.
// Files marked as FromRemote will be deleted locally if they are removed from the remote.
// Local-only files (not from remote) are preserved during sync.
type SyncManifest struct {
	// LastSync is the timestamp of the last successful sync operation.
	LastSync time.Time `json:"last_sync"`
	// ManagedFiles maps relative filenames to their metadata.
	ManagedFiles map[string]FileInfo `json:"managed_files"`
}

// FileInfo contains metadata about a managed file.
type FileInfo struct {
	// Checksum is a truncated SHA-256 hash of the file content.
	Checksum string `json:"checksum"`
	// ModifiedAt is the timestamp when the file was last synced.
	ModifiedAt time.Time `json:"modified_at"`
	// FromRemote indicates whether this file was synced from the remote store.
	// If true, the file will be deleted locally when removed from remote.
	// If false, the file is a local-only addition and will be preserved.
	FromRemote bool `json:"from_remote"`
}

// LoadManifest reads the sync manifest from the specified directory.
// If the manifest doesn't exist, returns an empty manifest with initialized maps.
func LoadManifest(dir string) (*SyncManifest, error) {
	path := filepath.Join(dir, ManifestFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &SyncManifest{
			ManagedFiles: make(map[string]FileInfo),
		}, nil
	}
	if err != nil {
		return nil, err
	}
	var m SyncManifest
	if err := json.Unmarshal(data, &m); err != nil {
		// If corrupt, return empty manifest
		return &SyncManifest{
			ManagedFiles: make(map[string]FileInfo),
		}, nil
	}
	if m.ManagedFiles == nil {
		m.ManagedFiles = make(map[string]FileInfo)
	}
	return &m, nil
}

// Save persists the manifest to disk in the specified directory.
func (m *SyncManifest) Save(dir string) error {
	if m == nil {
		return nil
	}
	path := filepath.Join(dir, ManifestFileName)
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically via temp file
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// MarkFile records a file in the manifest with the given checksum and origin.
func (m *SyncManifest) MarkFile(filename string, content []byte, fromRemote bool) {
	if m == nil || m.ManagedFiles == nil {
		return
	}
	m.ManagedFiles[filename] = FileInfo{
		Checksum:   ComputeChecksum(content),
		ModifiedAt: time.Now(),
		FromRemote: fromRemote,
	}
}

// RemoveFile removes a file entry from the manifest.
func (m *SyncManifest) RemoveFile(filename string) {
	if m == nil || m.ManagedFiles == nil {
		return
	}
	delete(m.ManagedFiles, filename)
}

// IsFromRemote checks if a file was synced from remote.
func (m *SyncManifest) IsFromRemote(filename string) bool {
	if m == nil || m.ManagedFiles == nil {
		return false
	}
	info, exists := m.ManagedFiles[filename]
	return exists && info.FromRemote
}

// GetOrphanedFiles returns a list of files that were previously synced from remote
// but are no longer present in the provided set of current remote files.
func (m *SyncManifest) GetOrphanedFiles(currentRemoteFiles map[string]bool) []string {
	if m == nil || m.ManagedFiles == nil {
		return nil
	}
	var orphaned []string
	for filename, info := range m.ManagedFiles {
		if info.FromRemote && !currentRemoteFiles[filename] {
			orphaned = append(orphaned, filename)
		}
	}
	return orphaned
}

// ComputeChecksum generates a truncated SHA-256 checksum of the given data.
// Returns the first 16 hex characters (8 bytes) which is sufficient for change detection.
func ComputeChecksum(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:8])
}

// IsManifestFile returns true if the given path is a manifest file.
func IsManifestFile(path string) bool {
	return filepath.Base(path) == ManifestFileName
}
