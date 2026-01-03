package management

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/auth/login"
	"github.com/nghyane/llm-mux/internal/json"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/oauth"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/tidwall/gjson"
)

var lastRefreshKeys = []string{"last_refresh", "lastRefresh", "last_refreshed_at", "lastRefreshedAt"}

// callbackForwardersMgr manages callback forwarders using the new oauth.CallbackServersManager.
// This replaces the old manual forwarder management with a cleaner implementation.
var callbackForwardersMgr = oauth.NewCallbackServersManager()

// startCallbackForwarder starts a callback forwarder that redirects OAuth callbacks to the main server.
// Uses the new oauth.CallbackServersManager for cleaner lifecycle management.
func startCallbackForwarder(port int, provider, targetBase string) (any, error) {
	return callbackForwardersMgr.StartForwarder(port, provider, targetBase)
}

func extractLastRefreshTimestamp(meta map[string]any) (time.Time, bool) {
	if len(meta) == 0 {
		return time.Time{}, false
	}
	for _, key := range lastRefreshKeys {
		if val, ok := meta[key]; ok {
			if ts, ok1 := parseLastRefreshValue(val); ok1 {
				return ts, true
			}
		}
	}
	return time.Time{}, false
}

func parseLastRefreshValue(v any) (time.Time, bool) {
	switch val := v.(type) {
	case string:
		s := strings.TrimSpace(val)
		if s == "" {
			return time.Time{}, false
		}
		layouts := []string{time.RFC3339, time.RFC3339Nano, "2006-01-02 15:04:05", "2006-01-02T15:04:05Z07:00"}
		for _, layout := range layouts {
			if ts, err := time.Parse(layout, s); err == nil {
				return ts.UTC(), true
			}
		}
		if unix, err := strconv.ParseInt(s, 10, 64); err == nil && unix > 0 {
			return time.Unix(unix, 0).UTC(), true
		}
	case float64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case int64:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(val, 0).UTC(), true
	case int:
		if val <= 0 {
			return time.Time{}, false
		}
		return time.Unix(int64(val), 0).UTC(), true
	case json.Number:
		if i, err := val.Int64(); err == nil && i > 0 {
			return time.Unix(i, 0).UTC(), true
		}
	}
	return time.Time{}, false
}

func (h *Handler) managementCallbackURL(path string) (string, error) {
	if h == nil || h.cfg == nil || h.cfg.Port <= 0 {
		return "", fmt.Errorf("server port is not configured")
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	scheme := "http"
	if h.cfg.TLS.Enable {
		scheme = "https"
	}
	return fmt.Sprintf("%s://127.0.0.1:%d%s", scheme, h.cfg.Port, path), nil
}

func (h *Handler) ListAuthFiles(c *gin.Context) {
	if h == nil {
		respondInternalError(c, "handler not initialized")
		return
	}
	if h.authManager == nil {
		h.listAuthFilesFromDisk(c)
		return
	}
	auths := h.authManager.List()
	quotaManager := h.authManager.GetQuotaManager()
	now := time.Now()

	files := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if entry := h.buildAuthFileEntry(auth); entry != nil {
			h.enrichWithQuotaState(entry, auth.ID, quotaManager, now)
			files = append(files, entry)
		}
	}
	sort.Slice(files, func(i, j int) bool {
		nameI, _ := files[i]["name"].(string)
		nameJ, _ := files[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	respondOK(c, gin.H{"files": files})
}

func (h *Handler) enrichWithQuotaState(entry gin.H, authID string, qm *provider.QuotaManager, now time.Time) {
	if qm == nil {
		return
	}
	state := qm.GetState(authID)
	if state == nil {
		entry["quota_state"] = gin.H{
			"active_requests":   int64(0),
			"total_tokens_used": int64(0),
			"in_cooldown":       false,
		}
		return
	}

	inCooldown := now.Before(state.CooldownUntil)
	qs := gin.H{
		"active_requests":   state.ActiveRequests,
		"total_tokens_used": state.TotalTokensUsed,
		"in_cooldown":       inCooldown,
	}

	if inCooldown {
		qs["cooldown_until"] = state.CooldownUntil
		qs["cooldown_remaining_seconds"] = int64(state.CooldownUntil.Sub(now).Seconds())
	}

	if state.LearnedLimit > 0 {
		qs["learned_limit"] = state.LearnedLimit
	}
	if state.LearnedCooldown > 0 {
		qs["learned_cooldown_seconds"] = int64(state.LearnedCooldown.Seconds())
	}
	if !state.LastExhaustedAt.IsZero() {
		qs["last_exhausted_at"] = state.LastExhaustedAt
	}

	entry["quota_state"] = qs
}

// List auth files from disk when the auth manager is unavailable.
func (h *Handler) listAuthFilesFromDisk(c *gin.Context) {
	entries, err := os.ReadDir(h.cfg.AuthDir)
	if err != nil {
		respondInternalError(c, fmt.Sprintf("failed to read auth dir: %v", err))
		return
	}
	files := make([]gin.H, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".json") {
			continue
		}
		if info, errInfo := e.Info(); errInfo == nil {
			fileData := gin.H{"name": name, "size": info.Size(), "modtime": info.ModTime()}

			// Read file to get type field
			full := filepath.Join(h.cfg.AuthDir, name)
			if data, errRead := os.ReadFile(full); errRead == nil {
				typeValue := gjson.GetBytes(data, "type").String()
				emailValue := gjson.GetBytes(data, "email").String()
				fileData["type"] = typeValue
				fileData["email"] = emailValue
			}

			files = append(files, fileData)
		}
	}
	respondOK(c, gin.H{"files": files})
}

func (h *Handler) buildAuthFileEntry(auth *provider.Auth) gin.H {
	if auth == nil {
		return nil
	}
	auth.EnsureIndex()
	runtimeOnly := isRuntimeOnlyAuth(auth)
	if runtimeOnly && (auth.Disabled || auth.Status == provider.StatusDisabled) {
		return nil
	}
	path := strings.TrimSpace(authAttribute(auth, "path"))
	if path == "" && !runtimeOnly {
		return nil
	}
	name := strings.TrimSpace(auth.FileName)
	if name == "" {
		name = auth.ID
	}
	entry := gin.H{
		"id":             auth.ID,
		"auth_index":     auth.Index,
		"name":           name,
		"type":           strings.TrimSpace(auth.Provider),
		"provider":       strings.TrimSpace(auth.Provider),
		"label":          auth.Label,
		"status":         auth.Status,
		"status_message": auth.StatusMessage,
		"disabled":       auth.Disabled,
		"unavailable":    auth.Unavailable,
		"runtime_only":   runtimeOnly,
		"source":         "memory",
		"size":           int64(0),
	}
	if email := authEmail(auth); email != "" {
		entry["email"] = email
	}
	if accountType, account := auth.AccountInfo(); accountType != "" || account != "" {
		if accountType != "" {
			entry["account_type"] = accountType
		}
		if account != "" {
			entry["account"] = account
		}
	}
	if !auth.CreatedAt.IsZero() {
		entry["created_at"] = auth.CreatedAt
	}
	if !auth.UpdatedAt.IsZero() {
		entry["modtime"] = auth.UpdatedAt
		entry["updated_at"] = auth.UpdatedAt
	}
	if !auth.LastRefreshedAt.IsZero() {
		entry["last_refresh"] = auth.LastRefreshedAt
	}
	if path != "" {
		entry["path"] = path
		entry["source"] = "file"
		if info, err := os.Stat(path); err == nil {
			entry["size"] = info.Size()
			entry["modtime"] = info.ModTime()
		} else if os.IsNotExist(err) {
			// Hide credentials removed from disk but still lingering in memory.
			if !runtimeOnly && (auth.Disabled || auth.Status == provider.StatusDisabled || strings.EqualFold(strings.TrimSpace(auth.StatusMessage), "removed via management api")) {
				return nil
			}
			entry["source"] = "memory"
		} else {
			log.WithError(err).Warnf("failed to stat auth file %s", path)
		}
	}
	return entry
}

func authEmail(auth *provider.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["email"].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["email"]); v != "" {
			return v
		}
		if v := strings.TrimSpace(auth.Attributes["account_email"]); v != "" {
			return v
		}
	}
	return ""
}

func authAttribute(auth *provider.Auth, key string) string {
	if auth == nil || len(auth.Attributes) == 0 {
		return ""
	}
	return auth.Attributes[key]
}

func isRuntimeOnlyAuth(auth *provider.Auth) bool {
	if auth == nil || len(auth.Attributes) == 0 {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Attributes["runtime_only"]), "true")
}

// Download single auth file by name
func (h *Handler) DownloadAuthFile(c *gin.Context) {
	name := c.Query("name")
	if name == "" || strings.Contains(name, string(os.PathSeparator)) {
		respondBadRequest(c, "invalid name")
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		respondBadRequest(c, "name must end with .json")
		return
	}
	full := filepath.Join(h.cfg.AuthDir, name)
	data, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			respondNotFound(c, "file not found")
		} else {
			respondInternalError(c, fmt.Sprintf("failed to read file: %v", err))
		}
		return
	}
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=\"%s\"", name))
	c.Data(200, "application/json", data)
}

type uploadResult struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

func (h *Handler) UploadAuthFile(c *gin.Context) {
	if h.authManager == nil {
		respondError(c, http.StatusServiceUnavailable, ErrCodeInternalError, "core auth manager unavailable")
		return
	}
	ctx := c.Request.Context()

	if form, err := c.MultipartForm(); err == nil && form != nil && form.File != nil {
		var files []*multipart.FileHeader
		if f := form.File["files"]; len(f) > 0 {
			files = f
		} else if f := form.File["file[]"]; len(f) > 0 {
			files = f
		} else if f := form.File["file"]; len(f) > 0 {
			files = f
		}

		if len(files) > 0 {
			results := h.processBatchUpload(ctx, files)
			allOK := true
			for _, r := range results {
				if r.Status != "ok" {
					allOK = false
					break
				}
			}
			if allOK {
				respondOK(c, gin.H{"status": "ok", "count": len(results), "results": results})
			} else {
				c.JSON(http.StatusMultiStatus, gin.H{"status": "partial", "count": len(results), "results": results})
			}
			return
		}
	}

	name := c.Query("name")
	if name == "" || strings.Contains(name, string(os.PathSeparator)) {
		respondBadRequest(c, "invalid name")
		return
	}
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		respondBadRequest(c, "name must end with .json")
		return
	}
	// Limit request body to 1MB to prevent DoS attacks
	const maxAuthFileSize = 1 * 1024 * 1024
	data, err := io.ReadAll(io.LimitReader(c.Request.Body, maxAuthFileSize))
	if err != nil {
		respondBadRequest(c, "failed to read body")
		return
	}
	dst := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}
	if errWrite := os.WriteFile(dst, data, 0o600); errWrite != nil {
		respondInternalError(c, fmt.Sprintf("failed to write file: %v", errWrite))
		return
	}
	if err = h.registerAuthFromFile(ctx, dst, data); err != nil {
		respondInternalError(c, err.Error())
		return
	}
	h.syncAuthToRemote(ctx, "Upload", name, dst)
	respondOK(c, gin.H{"status": "ok"})
}

func (h *Handler) processBatchUpload(ctx context.Context, files []*multipart.FileHeader) []uploadResult {
	results := make([]uploadResult, 0, len(files))
	for _, fileHeader := range files {
		result := h.processOneUpload(ctx, fileHeader)
		results = append(results, result)
	}
	return results
}

func (h *Handler) processOneUpload(ctx context.Context, fileHeader *multipart.FileHeader) uploadResult {
	name := filepath.Base(fileHeader.Filename)
	if !strings.HasSuffix(strings.ToLower(name), ".json") {
		return uploadResult{Name: name, Status: "error", Message: "file must be .json"}
	}

	file, err := fileHeader.Open()
	if err != nil {
		return uploadResult{Name: name, Status: "error", Message: fmt.Sprintf("failed to open: %v", err)}
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return uploadResult{Name: name, Status: "error", Message: fmt.Sprintf("failed to read: %v", err)}
	}

	dst := filepath.Join(h.cfg.AuthDir, name)
	if !filepath.IsAbs(dst) {
		if abs, errAbs := filepath.Abs(dst); errAbs == nil {
			dst = abs
		}
	}

	if errWrite := os.WriteFile(dst, data, 0o600); errWrite != nil {
		return uploadResult{Name: name, Status: "error", Message: fmt.Sprintf("failed to write: %v", errWrite)}
	}

	if errReg := h.registerAuthFromFile(ctx, dst, data); errReg != nil {
		return uploadResult{Name: name, Status: "error", Message: errReg.Error()}
	}

	h.syncAuthToRemote(ctx, "Upload", name, dst)
	return uploadResult{Name: name, Status: "ok"}
}

func (h *Handler) DeleteAuthFile(c *gin.Context) {
	if h.authManager == nil {
		respondError(c, http.StatusServiceUnavailable, ErrCodeInternalError, "core auth manager unavailable")
		return
	}
	ctx := c.Request.Context()
	if all := c.Query("all"); all == "true" || all == "1" || all == "*" {
		entries, err := os.ReadDir(h.cfg.AuthDir)
		if err != nil {
			respondInternalError(c, fmt.Sprintf("failed to read auth dir: %v", err))
			return
		}
		deleted := 0
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			if !strings.HasSuffix(strings.ToLower(name), ".json") {
				continue
			}
			full := filepath.Join(h.cfg.AuthDir, name)
			if !filepath.IsAbs(full) {
				if abs, errAbs := filepath.Abs(full); errAbs == nil {
					full = abs
				}
			}
			if errDel := h.deleteTokenRecord(ctx, full); errDel != nil {
				respondInternalError(c, errDel.Error())
				return
			}
			deleted++
			h.disableAuth(ctx, full)
		}
		respondOK(c, gin.H{"status": "ok", "deleted": deleted})
		return
	}
	name := c.Query("name")
	if name == "" || strings.Contains(name, string(os.PathSeparator)) {
		respondBadRequest(c, "invalid name")
		return
	}
	full := filepath.Join(h.cfg.AuthDir, filepath.Base(name))
	if !filepath.IsAbs(full) {
		if abs, errAbs := filepath.Abs(full); errAbs == nil {
			full = abs
		}
	}
	if _, err := os.Stat(full); os.IsNotExist(err) {
		respondNotFound(c, "file not found")
		return
	}
	if err := h.deleteTokenRecord(ctx, full); err != nil {
		respondInternalError(c, err.Error())
		return
	}
	h.disableAuth(ctx, full)
	respondOK(c, gin.H{"status": "ok"})
}

func (h *Handler) authIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if h == nil || h.cfg == nil {
		return path
	}
	authDir := strings.TrimSpace(h.cfg.AuthDir)
	if authDir == "" {
		return path
	}
	if rel, err := filepath.Rel(authDir, path); err == nil && rel != "" {
		return rel
	}
	return path
}

func (h *Handler) registerAuthFromFile(ctx context.Context, path string, data []byte) error {
	if h.authManager == nil {
		return nil
	}
	if path == "" {
		return fmt.Errorf("auth path is empty")
	}
	if data == nil {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read auth file: %w", err)
		}
	}
	metadata := make(map[string]any)
	if err := json.Unmarshal(data, &metadata); err != nil {
		return fmt.Errorf("invalid auth file: %w", err)
	}
	providerType, _ := metadata["type"].(string)
	if providerType == "" {
		providerType = "unknown"
	}
	label := providerType
	if email, ok := metadata["email"].(string); ok && email != "" {
		label = email
	}
	lastRefresh, hasLastRefresh := extractLastRefreshTimestamp(metadata)

	authID := h.authIDForPath(path)
	if authID == "" {
		authID = path
	}
	attr := map[string]string{
		"path":   path,
		"source": path,
	}
	auth := &provider.Auth{
		ID:         authID,
		Provider:   providerType,
		FileName:   filepath.Base(path),
		Label:      label,
		Status:     provider.StatusActive,
		Attributes: attr,
		Metadata:   metadata,
		CreatedAt:  time.Now(),
		UpdatedAt:  time.Now(),
	}
	if hasLastRefresh {
		auth.LastRefreshedAt = lastRefresh
	}
	if existing, ok := h.authManager.GetByID(authID); ok {
		auth.CreatedAt = existing.CreatedAt
		if !hasLastRefresh {
			auth.LastRefreshedAt = existing.LastRefreshedAt
		}
		auth.NextRefreshAfter = existing.NextRefreshAfter
		auth.Runtime = existing.Runtime
		_, err := h.authManager.Update(ctx, auth)
		return err
	}
	_, err := h.authManager.Register(ctx, auth)
	return err
}

func (h *Handler) disableAuth(ctx context.Context, id string) {
	if h == nil || h.authManager == nil {
		return
	}
	authID := h.authIDForPath(id)
	if authID == "" {
		authID = strings.TrimSpace(id)
	}
	if authID == "" {
		return
	}
	if auth, ok := h.authManager.GetByID(authID); ok {
		auth.Disabled = true
		auth.Status = provider.StatusDisabled
		auth.StatusMessage = "removed via management API"
		auth.UpdatedAt = time.Now()
		_, _ = h.authManager.Update(ctx, auth)
	}
}

func (h *Handler) deleteTokenRecord(ctx context.Context, path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("auth path is empty")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return fmt.Errorf("token store unavailable")
	}
	return store.Delete(ctx, path)
}

func (h *Handler) tokenStoreWithBaseDir() provider.Store {
	if h == nil {
		return nil
	}
	store := h.tokenStore
	if store == nil {
		store = login.GetTokenStore()
		h.tokenStore = store
	}
	if h.cfg != nil {
		if dirSetter, ok := store.(interface{ SetBaseDir(string) }); ok {
			dirSetter.SetBaseDir(h.cfg.AuthDir)
		}
	}
	return store
}

func (h *Handler) saveTokenRecord(ctx context.Context, record *provider.Auth) (string, error) {
	if record == nil {
		return "", fmt.Errorf("token record is nil")
	}
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return "", fmt.Errorf("token store unavailable")
	}
	return store.Save(ctx, record)
}

type authPersister interface {
	PersistAuthFiles(ctx context.Context, message string, paths ...string) error
}

func (h *Handler) syncAuthToRemote(ctx context.Context, action, name, path string) {
	store := h.tokenStoreWithBaseDir()
	if store == nil {
		return
	}
	persister, ok := store.(authPersister)
	if !ok {
		return
	}
	go func() {
		if err := persister.PersistAuthFiles(ctx, fmt.Sprintf("%s auth %s", action, name), path); err != nil {
			log.Warnf("failed to sync auth to remote store: %v", err)
		}
	}()
}
