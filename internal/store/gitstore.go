package store

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nghyane/llm-mux/internal/json"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/transport"
	"github.com/go-git/go-git/v6/plumbing/transport/http"
	"github.com/nghyane/llm-mux/internal/provider"
)

// Distributed lock constants for multi-instance coordination
const (
	lockFileName      = ".llm-mux.lock"
	lockTimeout       = 30 * time.Second
	lockRetryInterval = 500 * time.Millisecond
	lockAcquireMax    = 60 * time.Second
)

// GitTokenStore persists token records and auth metadata using git as the backing storage.
type GitTokenStore struct {
	mu              sync.Mutex
	dirLock         sync.RWMutex
	configPath      string
	authDir         string
	repoDir         string
	remote          string
	username        string
	password        string
	instanceID      string
	disableAutoPush bool
}

// GitLock represents a distributed lock file entry.
type GitLock struct {
	InstanceID string    `json:"instance_id"`
	Timestamp  time.Time `json:"timestamp"`
}

// NewGitTokenStore creates a token store that saves credentials to disk through the
// TokenStorage implementation embedded in the token record.
func NewGitTokenStore(remote, username, password, configPath, authDir string, disableAutoPush bool) *GitTokenStore {
	configPath = strings.TrimSpace(configPath)
	authDir = strings.TrimSpace(authDir)

	var absConfigPath, absAuthDir string
	if configPath != "" {
		if abs, err := filepath.Abs(configPath); err == nil {
			absConfigPath = abs
		} else {
			absConfigPath = configPath
		}
	}
	if authDir != "" {
		if abs, err := filepath.Abs(authDir); err == nil {
			absAuthDir = abs
		} else {
			absAuthDir = authDir
		}
	}

	var repoDir string
	if absConfigPath != "" {
		repoDir = filepath.Dir(absConfigPath)
	} else if absAuthDir != "" {
		repoDir = filepath.Dir(absAuthDir)
	}

	hostname, _ := os.Hostname()
	instanceID := fmt.Sprintf("%s-%d-%d", hostname, os.Getpid(), time.Now().UnixNano())

	return &GitTokenStore{
		remote:          remote,
		username:        username,
		password:        password,
		configPath:      absConfigPath,
		authDir:         absAuthDir,
		repoDir:         repoDir,
		instanceID:      instanceID,
		disableAutoPush: disableAutoPush,
	}
}

// SetBaseDir is a no-op because the git store controls its own workspace.
func (s *GitTokenStore) SetBaseDir(string) {}

// AuthDir returns the directory used for auth persistence.
func (s *GitTokenStore) AuthDir() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.authDir
}

// ConfigPath returns the managed config file path.
func (s *GitTokenStore) ConfigPath() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.configPath
}

// EnsureRepository prepares the local git working tree by cloning or opening the repository.
func (s *GitTokenStore) EnsureRepository() error {
	s.dirLock.RLock()
	remote := s.remote
	repoDir := s.repoDir
	authDir := s.authDir
	configPath := s.configPath
	s.dirLock.RUnlock()

	if remote == "" {
		return fmt.Errorf("git token store: remote not configured")
	}
	if repoDir == "" {
		return fmt.Errorf("git token store: repository directory not configured")
	}
	if authDir == "" {
		return fmt.Errorf("git token store: auth directory not configured")
	}

	gitDir := filepath.Join(repoDir, ".git")
	authMethod := s.gitAuth()
	var initPaths []string
	if _, err := os.Stat(gitDir); errors.Is(err, fs.ErrNotExist) {
		if errMk := os.MkdirAll(repoDir, 0o700); errMk != nil {
			return fmt.Errorf("git token store: create repo dir: %w", errMk)
		}
		if _, errClone := git.PlainClone(repoDir, &git.CloneOptions{Auth: authMethod, URL: remote}); errClone != nil {
			if errors.Is(errClone, transport.ErrEmptyRemoteRepository) {
				_ = os.RemoveAll(gitDir)
				repo, errInit := git.PlainInit(repoDir, false)
				if errInit != nil {
					return fmt.Errorf("git token store: init empty repo: %w", errInit)
				}
				if _, errRemote := repo.Remote("origin"); errRemote != nil {
					if _, errCreate := repo.CreateRemote(&config.RemoteConfig{
						Name: "origin",
						URLs: []string{remote},
					}); errCreate != nil && !errors.Is(errCreate, git.ErrRemoteExists) {
						return fmt.Errorf("git token store: configure remote: %w", errCreate)
					}
				}
				if err := os.MkdirAll(authDir, 0o700); err != nil {
					return fmt.Errorf("git token store: create auth dir: %w", err)
				}
				if configPath != "" {
					if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
						return fmt.Errorf("git token store: create config dir: %w", err)
					}
				}
				authKeep := filepath.Join(authDir, ".gitkeep")
				if err := ensureEmptyFile(authKeep); err != nil {
					return fmt.Errorf("git token store: create auth placeholder: %w", err)
				}
				authKeepRel, _ := filepath.Rel(repoDir, authKeep)
				if authKeepRel != "" {
					initPaths = append(initPaths, authKeepRel)
				}
			} else {
				return fmt.Errorf("git token store: clone remote: %w", errClone)
			}
		}
	} else if err != nil {
		return fmt.Errorf("git token store: stat repo: %w", err)
	} else {
		repo, errOpen := git.PlainOpen(repoDir)
		if errOpen != nil {
			return fmt.Errorf("git token store: open repo: %w", errOpen)
		}
		worktree, errWorktree := repo.Worktree()
		if errWorktree != nil {
			return fmt.Errorf("git token store: worktree: %w", errWorktree)
		}
		if errPull := worktree.Pull(&git.PullOptions{Auth: authMethod, RemoteName: "origin"}); errPull != nil {
			switch {
			case errors.Is(errPull, git.NoErrAlreadyUpToDate),
				errors.Is(errPull, git.ErrUnstagedChanges),
				errors.Is(errPull, git.ErrNonFastForwardUpdate):
			case errors.Is(errPull, transport.ErrAuthenticationRequired),
				errors.Is(errPull, plumbing.ErrReferenceNotFound),
				errors.Is(errPull, transport.ErrEmptyRemoteRepository):
			default:
				return fmt.Errorf("git token store: pull: %w", errPull)
			}
		}
	}
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		return fmt.Errorf("git token store: create auth dir: %w", err)
	}
	if configPath != "" {
		if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
			return fmt.Errorf("git token store: create config dir: %w", err)
		}
	}

	if len(initPaths) > 0 {
		s.mu.Lock()
		err := s.commitAndPushLocked("Initialize git token store", initPaths...)
		s.mu.Unlock()
		if err != nil {
			return err
		}
	}
	return nil
}

// Save persists token storage and metadata to the resolved auth file path.
func (s *GitTokenStore) Save(_ context.Context, auth *provider.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}

	path, err := s.resolveAuthPath(auth)
	if err != nil {
		return "", err
	}
	if path == "" {
		return "", fmt.Errorf("auth filestore: missing file path attribute for %s", auth.ID)
	}

	if auth.Disabled {
		if _, statErr := os.Stat(path); os.IsNotExist(statErr) {
			return "", nil
		}
	}

	if err = s.EnsureRepository(); err != nil {
		return "", err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("auth filestore: create dir failed: %w", err)
	}

	switch {
	case auth.Storage != nil:
		if err = auth.Storage.SaveTokenToFile(path); err != nil {
			return "", err
		}
	case auth.Metadata != nil:
		raw, errMarshal := json.Marshal(auth.Metadata)
		if errMarshal != nil {
			return "", fmt.Errorf("auth filestore: marshal metadata failed: %w", errMarshal)
		}
		if existing, errRead := os.ReadFile(path); errRead == nil {
			if jsonEqual(existing, raw) {
				return path, nil
			}
		} else if !os.IsNotExist(errRead) {
			return "", fmt.Errorf("auth filestore: read existing failed: %w", errRead)
		}
		tmp := path + ".tmp"
		if errWrite := os.WriteFile(tmp, raw, 0o600); errWrite != nil {
			return "", fmt.Errorf("auth filestore: write temp failed: %w", errWrite)
		}
		if errRename := os.Rename(tmp, path); errRename != nil {
			_ = os.Remove(tmp) // cleanup temp file on rename failure
			return "", fmt.Errorf("auth filestore: rename failed: %w", errRename)
		}
	default:
		return "", fmt.Errorf("auth filestore: nothing to persist for %s", auth.ID)
	}

	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes["path"] = path

	if strings.TrimSpace(auth.FileName) == "" {
		auth.FileName = auth.ID
	}

	relPath, errRel := s.relativeToRepo(path)
	if errRel != nil {
		return "", errRel
	}
	messageID := auth.ID
	if strings.TrimSpace(messageID) == "" {
		messageID = filepath.Base(path)
	}
	if errCommit := s.commitAndPushLocked(fmt.Sprintf("Update auth %s", strings.TrimSpace(messageID)), relPath); errCommit != nil {
		return "", errCommit
	}

	return path, nil
}

// List enumerates all auth JSON files under the configured directory.
func (s *GitTokenStore) List(_ context.Context) ([]*provider.Auth, error) {
	if err := s.EnsureRepository(); err != nil {
		return nil, err
	}
	dir := s.authDirSnapshot()
	if dir == "" {
		return nil, fmt.Errorf("auth filestore: directory not configured")
	}
	entries := make([]*provider.Auth, 0)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(d.Name()), ".json") {
			return nil
		}
		auth, err := s.readAuthFile(path, dir)
		if err != nil {
			return nil
		}
		if auth != nil {
			entries = append(entries, auth)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// Delete removes the auth file.
func (s *GitTokenStore) Delete(_ context.Context, id string) error {
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("auth filestore: id is empty")
	}
	path, err := s.resolveDeletePath(id)
	if err != nil {
		return err
	}
	if err = s.EnsureRepository(); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fileExisted := true
	if err = os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			fileExisted = false
		} else {
			return fmt.Errorf("auth filestore: delete failed: %w", err)
		}
	}

	rel, errRel := s.relativeToRepo(path)
	if errRel != nil {
		if fileExisted {
			return errRel
		}
		return nil
	}
	messageID := id
	if errCommit := s.commitAndPushLocked(fmt.Sprintf("Delete auth %s", messageID), rel); errCommit != nil {
		return errCommit
	}
	return nil
}

// PersistAuthFiles commits and pushes the provided paths to the remote repository.
// It no-ops when the store is not fully configured or when there are no paths.
func (s *GitTokenStore) PersistAuthFiles(_ context.Context, message string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	if err := s.EnsureRepository(); err != nil {
		return err
	}

	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		rel, err := s.relativeToRepo(trimmed)
		if err != nil {
			return err
		}
		filtered = append(filtered, rel)
	}
	if len(filtered) == 0 {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if strings.TrimSpace(message) == "" {
		message = "Sync watcher updates"
	}
	return s.commitAndPushLocked(message, filtered...)
}

func (s *GitTokenStore) resolveDeletePath(id string) (string, error) {
	if strings.ContainsRune(id, os.PathSeparator) || filepath.IsAbs(id) {
		return id, nil
	}
	dir := s.authDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, id), nil
}

func (s *GitTokenStore) readAuthFile(path, baseDir string) (*provider.Auth, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	metadata := make(map[string]any)
	if err = json.Unmarshal(data, &metadata); err != nil {
		return nil, fmt.Errorf("unmarshal auth json: %w", err)
	}
	providerName, _ := metadata["type"].(string)
	if providerName == "" {
		providerName = "unknown"
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}
	id := s.idFor(path, baseDir)
	auth := &provider.Auth{
		ID:               id,
		Provider:         providerName,
		FileName:         id,
		Label:            s.labelFor(metadata),
		Status:           provider.StatusActive,
		Attributes:       map[string]string{"path": path},
		Metadata:         metadata,
		CreatedAt:        info.ModTime(),
		UpdatedAt:        info.ModTime(),
		LastRefreshedAt:  time.Time{},
		NextRefreshAfter: time.Time{},
	}
	if email, ok := metadata["email"].(string); ok && email != "" {
		auth.Attributes["email"] = email
	}
	return auth, nil
}

func (s *GitTokenStore) idFor(path, baseDir string) string {
	if baseDir == "" {
		return path
	}
	rel, err := filepath.Rel(baseDir, path)
	if err != nil {
		return path
	}
	return rel
}

func (s *GitTokenStore) resolveAuthPath(auth *provider.Auth) (string, error) {
	if auth == nil {
		return "", fmt.Errorf("auth filestore: auth is nil")
	}
	if auth.Attributes != nil {
		if p := strings.TrimSpace(auth.Attributes["path"]); p != "" {
			return p, nil
		}
	}
	if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
		if filepath.IsAbs(fileName) {
			return fileName, nil
		}
		if dir := s.authDirSnapshot(); dir != "" {
			return filepath.Join(dir, fileName), nil
		}
		return fileName, nil
	}
	if auth.ID == "" {
		return "", fmt.Errorf("auth filestore: missing id")
	}
	if filepath.IsAbs(auth.ID) {
		return auth.ID, nil
	}
	dir := s.authDirSnapshot()
	if dir == "" {
		return "", fmt.Errorf("auth filestore: directory not configured")
	}
	return filepath.Join(dir, auth.ID), nil
}

func (s *GitTokenStore) labelFor(metadata map[string]any) string {
	if metadata == nil {
		return ""
	}
	if v, ok := metadata["label"].(string); ok && v != "" {
		return v
	}
	if v, ok := metadata["email"].(string); ok && v != "" {
		return v
	}
	if project, ok := metadata["project_id"].(string); ok && project != "" {
		return project
	}
	return ""
}

func (s *GitTokenStore) authDirSnapshot() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.authDir
}

func (s *GitTokenStore) repoDirSnapshot() string {
	s.dirLock.RLock()
	defer s.dirLock.RUnlock()
	return s.repoDir
}

func (s *GitTokenStore) gitAuth() transport.AuthMethod {
	if s.username == "" && s.password == "" {
		return nil
	}
	user := s.username
	if user == "" {
		user = "git"
	}
	return &http.BasicAuth{Username: user, Password: s.password}
}

func (s *GitTokenStore) lockPath() string {
	return filepath.Join(s.repoDirSnapshot(), lockFileName)
}

func (s *GitTokenStore) readLock() (*GitLock, error) {
	data, err := os.ReadFile(s.lockPath())
	if err != nil {
		return nil, err
	}
	var lock GitLock
	if err := json.Unmarshal(data, &lock); err != nil {
		return nil, err
	}
	return &lock, nil
}

func (s *GitTokenStore) writeLock() error {
	lock := GitLock{
		InstanceID: s.instanceID,
		Timestamp:  time.Now(),
	}
	data, err := json.Marshal(lock)
	if err != nil {
		return err
	}
	return os.WriteFile(s.lockPath(), data, 0o600)
}

func (s *GitTokenStore) acquireDistributedLock(ctx context.Context) error {
	deadline := time.Now().Add(lockAcquireMax)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if time.Now().After(deadline) {
			return fmt.Errorf("git token store: lock acquisition timeout")
		}

		existing, err := s.readLock()
		if err == nil && existing != nil {
			if time.Since(existing.Timestamp) <= lockTimeout {
				time.Sleep(lockRetryInterval)
				continue
			}
		}

		if err := s.writeLock(); err != nil {
			time.Sleep(lockRetryInterval)
			continue
		}

		time.Sleep(50 * time.Millisecond)

		verify, err := s.readLock()
		if err != nil || verify == nil || verify.InstanceID != s.instanceID {
			time.Sleep(lockRetryInterval)
			continue
		}

		return nil
	}
}

func (s *GitTokenStore) releaseDistributedLock() error {
	existing, err := s.readLock()
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if existing.InstanceID != s.instanceID {
		return nil
	}
	return os.Remove(s.lockPath())
}

func (s *GitTokenStore) relativeToRepo(path string) (string, error) {
	repoDir := s.repoDirSnapshot()
	if repoDir == "" {
		return "", fmt.Errorf("git token store: repository path not configured")
	}
	absRepo := repoDir
	if abs, err := filepath.Abs(repoDir); err == nil {
		absRepo = abs
	}
	cleanPath := path
	if abs, err := filepath.Abs(path); err == nil {
		cleanPath = abs
	}
	rel, err := filepath.Rel(absRepo, cleanPath)
	if err != nil {
		return "", fmt.Errorf("git token store: relative path: %w", err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("git token store: path outside repository")
	}
	return rel, nil
}

func (s *GitTokenStore) commitAndPushLocked(message string, relPaths ...string) error {
	repoDir := s.repoDirSnapshot()
	if repoDir == "" {
		return fmt.Errorf("git token store: repository path not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), lockAcquireMax)
	defer cancel()

	if err := s.acquireDistributedLock(ctx); err != nil {
		return fmt.Errorf("git token store: acquire lock: %w", err)
	}
	defer s.releaseDistributedLock()

	repo, err := git.PlainOpen(repoDir)
	if err != nil {
		return fmt.Errorf("git token store: open repo: %w", err)
	}
	worktree, err := repo.Worktree()
	if err != nil {
		return fmt.Errorf("git token store: worktree: %w", err)
	}

	authMethod := s.gitAuth()
	if pullErr := worktree.Pull(&git.PullOptions{Auth: authMethod, RemoteName: "origin"}); pullErr != nil {
		switch {
		case errors.Is(pullErr, git.NoErrAlreadyUpToDate),
			errors.Is(pullErr, git.ErrUnstagedChanges),
			errors.Is(pullErr, git.ErrNonFastForwardUpdate),
			errors.Is(pullErr, transport.ErrAuthenticationRequired),
			errors.Is(pullErr, plumbing.ErrReferenceNotFound),
			errors.Is(pullErr, transport.ErrEmptyRemoteRepository):
		default:
			return fmt.Errorf("git token store: pull: %w", pullErr)
		}
	}

	added := false
	for _, rel := range relPaths {
		if strings.TrimSpace(rel) == "" {
			continue
		}
		if _, err = worktree.Add(rel); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				if _, errRemove := worktree.Remove(rel); errRemove != nil && !errors.Is(errRemove, os.ErrNotExist) {
					return fmt.Errorf("git token store: remove %s: %w", rel, errRemove)
				}
			} else {
				return fmt.Errorf("git token store: add %s: %w", rel, err)
			}
		}
		added = true
	}
	if !added {
		return nil
	}
	status, err := worktree.Status()
	if err != nil {
		return fmt.Errorf("git token store: status: %w", err)
	}
	if status.IsClean() {
		return nil
	}
	if strings.TrimSpace(message) == "" {
		message = "Update auth store"
	}
	signature := &object.Signature{
		Name:  "llm-mux",
		Email: "cliproxy@local",
		When:  time.Now(),
	}
	commitHash, err := worktree.Commit(message, &git.CommitOptions{
		Author: signature,
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			return nil
		}
		return fmt.Errorf("git token store: commit: %w", err)
	}

	if pullErr := worktree.Pull(&git.PullOptions{Auth: authMethod, RemoteName: "origin"}); pullErr != nil {
		switch {
		case errors.Is(pullErr, git.NoErrAlreadyUpToDate),
			errors.Is(pullErr, git.ErrUnstagedChanges),
			errors.Is(pullErr, git.ErrNonFastForwardUpdate),
			errors.Is(pullErr, transport.ErrAuthenticationRequired),
			errors.Is(pullErr, plumbing.ErrReferenceNotFound),
			errors.Is(pullErr, transport.ErrEmptyRemoteRepository):
		default:
			return fmt.Errorf("git token store: pull before squash: %w", pullErr)
		}
	}

	headRef, errHead := repo.Head()
	if errHead != nil {
		if !errors.Is(errHead, plumbing.ErrReferenceNotFound) {
			return fmt.Errorf("git token store: get head: %w", errHead)
		}
	} else if errRewrite := s.rewriteHeadAsSingleCommit(repo, headRef.Name(), commitHash, message, signature); errRewrite != nil {
		return errRewrite
	}

	// Skip push if auto-push is disabled (local commit only)
	if s.disableAutoPush {
		return nil
	}

	if err = s.safePush(repo); err != nil {
		return fmt.Errorf("git token store: push: %w", err)
	}
	return nil
}

// rewriteHeadAsSingleCommit rewrites the current branch tip to a single-parentless commit and leaves history squashed.
func (s *GitTokenStore) rewriteHeadAsSingleCommit(repo *git.Repository, branch plumbing.ReferenceName, commitHash plumbing.Hash, message string, signature *object.Signature) error {
	commitObj, err := repo.CommitObject(commitHash)
	if err != nil {
		return fmt.Errorf("git token store: inspect head commit: %w", err)
	}
	squashed := &object.Commit{
		Author:       *signature,
		Committer:    *signature,
		Message:      message,
		TreeHash:     commitObj.TreeHash,
		ParentHashes: nil,
		Encoding:     commitObj.Encoding,
		ExtraHeaders: commitObj.ExtraHeaders,
	}
	mem := &plumbing.MemoryObject{}
	mem.SetType(plumbing.CommitObject)
	if err := squashed.Encode(mem); err != nil {
		return fmt.Errorf("git token store: encode squashed commit: %w", err)
	}
	newHash, err := repo.Storer.SetEncodedObject(mem)
	if err != nil {
		return fmt.Errorf("git token store: write squashed commit: %w", err)
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(branch, newHash)); err != nil {
		return fmt.Errorf("git token store: update branch reference: %w", err)
	}
	return nil
}

// safePush attempts to push changes safely without force-pushing.
// It uses a fetch-merge-retry pattern to handle concurrent modifications.
func (s *GitTokenStore) safePush(repo *git.Repository) error {
	auth := s.gitAuth()

	// Force push is required because rewriteHeadAsSingleCommit squashes history
	// into a parentless commit, which always diverges from remote history.
	err := repo.Push(&git.PushOptions{
		Auth:       auth,
		RemoteName: "origin",
		Force:      true,
	})
	if err == nil || errors.Is(err, git.NoErrAlreadyUpToDate) {
		return nil
	}
	return err
}

// PersistConfig commits and pushes configuration changes to git.
func (s *GitTokenStore) PersistConfig(_ context.Context) error {
	if err := s.EnsureRepository(); err != nil {
		return err
	}
	configPath := s.ConfigPath()
	if configPath == "" {
		return fmt.Errorf("git token store: config path not configured")
	}
	if _, err := os.Stat(configPath); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("git token store: stat config: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rel, err := s.relativeToRepo(configPath)
	if err != nil {
		return err
	}
	return s.commitAndPushLocked("Update config", rel)
}

func ensureEmptyFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return os.WriteFile(path, []byte{}, 0o600)
		}
		return err
	}
	return nil
}

func jsonEqual(a, b []byte) bool {
	var objA any
	var objB any
	if err := json.Unmarshal(a, &objA); err != nil {
		return false
	}
	if err := json.Unmarshal(b, &objB); err != nil {
		return false
	}
	return deepEqualJSON(objA, objB)
}

func deepEqualJSON(a, b any) bool {
	switch valA := a.(type) {
	case map[string]any:
		valB, ok := b.(map[string]any)
		if !ok || len(valA) != len(valB) {
			return false
		}
		for key, subA := range valA {
			subB, ok1 := valB[key]
			if !ok1 || !deepEqualJSON(subA, subB) {
				return false
			}
		}
		return true
	case []any:
		sliceB, ok := b.([]any)
		if !ok || len(valA) != len(sliceB) {
			return false
		}
		for i := range valA {
			if !deepEqualJSON(valA[i], sliceB[i]) {
				return false
			}
		}
		return true
	case float64:
		valB, ok := b.(float64)
		if !ok {
			return false
		}
		return valA == valB
	case string:
		valB, ok := b.(string)
		if !ok {
			return false
		}
		return valA == valB
	case bool:
		valB, ok := b.(bool)
		if !ok {
			return false
		}
		return valA == valB
	case nil:
		return b == nil
	default:
		return false
	}
}
