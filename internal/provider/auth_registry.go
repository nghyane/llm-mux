package provider

import (
	"container/heap"
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	log "github.com/nghyane/llm-mux/internal/logging"
)

const (
	numAuthShards         = 32
	persistDebounceMs     = 500
	persistQueueSize      = 256
	refreshHeapInitialCap = 64
)

type authShard struct {
	mu      sync.RWMutex
	entries map[string]*AuthEntry
}

type refreshHeapEntry struct {
	authID    string
	refreshAt time.Time
	index     int
}

type refreshHeap []*refreshHeapEntry

func (h refreshHeap) Len() int           { return len(h) }
func (h refreshHeap) Less(i, j int) bool { return h[i].refreshAt.Before(h[j].refreshAt) }
func (h refreshHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}
func (h *refreshHeap) Push(x any) {
	n := len(*h)
	entry := x.(*refreshHeapEntry)
	entry.index = n
	*h = append(*h, entry)
}
func (h *refreshHeap) Pop() any {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*h = old[0 : n-1]
	return entry
}

type AuthRegistry struct {
	shards [numAuthShards]*authShard

	refreshMu      sync.Mutex
	refreshHeap    refreshHeap
	refreshEntries map[string]*refreshHeapEntry
	refreshSignal  chan struct{}

	store        Store
	persistQueue chan string
	persistBatch map[string]struct{}
	persistMu    sync.Mutex

	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup

	getExecutor func(provider string) ProviderExecutor

	hook Hook

	indexCounter uint64
	indexMu      sync.Mutex

	quotaManager *QuotaManager
}

func NewAuthRegistry(store Store, hook Hook, quotaManager *QuotaManager) *AuthRegistry {
	if hook == nil {
		hook = NoopHook{}
	}
	r := &AuthRegistry{
		store:          store,
		hook:           hook,
		quotaManager:   quotaManager,
		refreshHeap:    make(refreshHeap, 0, refreshHeapInitialCap),
		refreshEntries: make(map[string]*refreshHeapEntry),
		refreshSignal:  make(chan struct{}, 1),
		persistQueue:   make(chan string, persistQueueSize),
		persistBatch:   make(map[string]struct{}),
		stopCh:         make(chan struct{}),
	}
	for i := range r.shards {
		r.shards[i] = &authShard{
			entries: make(map[string]*AuthEntry),
		}
	}
	heap.Init(&r.refreshHeap)
	return r
}

func (r *AuthRegistry) SetExecutorProvider(fn func(provider string) ProviderExecutor) {
	r.getExecutor = fn
}

func (r *AuthRegistry) Start() {
	r.wg.Add(2)
	go r.refreshLoop()
	go r.persistLoop()
}

func (r *AuthRegistry) Stop() {
	r.stopOnce.Do(func() {
		close(r.stopCh)
	})
	r.wg.Wait()
}

func (r *AuthRegistry) getShard(authID string) *authShard {
	return r.shards[quotaHashKey(authID)%numAuthShards]
}

func (r *AuthRegistry) nextIndex() uint64 {
	r.indexMu.Lock()
	idx := r.indexCounter
	r.indexCounter++
	r.indexMu.Unlock()
	return idx
}

func (r *AuthRegistry) GetEntry(id string) *AuthEntry {
	if id == "" {
		return nil
	}
	shard := r.getShard(id)
	shard.mu.RLock()
	entry := shard.entries[id]
	shard.mu.RUnlock()
	return entry
}

func (r *AuthRegistry) Get(id string) *Auth {
	entry := r.GetEntry(id)
	if entry == nil {
		return nil
	}
	return entry.ToAuth()
}

func (r *AuthRegistry) GetByID(id string) (*Auth, bool) {
	auth := r.Get(id)
	return auth, auth != nil
}

func (r *AuthRegistry) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}

	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}

	shard := r.getShard(auth.ID)

	shard.mu.Lock()
	existing := shard.entries[auth.ID]
	if existing != nil {
		shard.mu.Unlock()
		return r.Update(ctx, auth)
	}

	auth.Index = r.nextIndex()
	entry := NewAuthEntry(auth)
	shard.entries[auth.ID] = entry
	shard.mu.Unlock()

	r.scheduleRefreshIfNeeded(entry)
	r.markDirty(auth.ID)

	result := entry.ToAuth()
	r.hook.OnAuthRegistered(ctx, result)
	return result, nil
}

func (r *AuthRegistry) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}

	shard := r.getShard(auth.ID)

	shard.mu.RLock()
	entry := shard.entries[auth.ID]
	shard.mu.RUnlock()

	if entry == nil {
		return r.Register(ctx, auth)
	}

	entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
		newMeta := &AuthMetadata{
			Label:            auth.Label,
			Status:           auth.Status,
			StatusMessage:    auth.StatusMessage,
			ProxyURL:         auth.ProxyURL,
			CreatedAt:        old.CreatedAt,
			UpdatedAt:        time.Now(),
			MaterialVersion:  auth.MaterialVersion,
			LastRefreshedAt:  auth.LastRefreshedAt,
			NextRefreshAfter: auth.NextRefreshAfter,
			NextRetryAfter:   auth.NextRetryAfter,
			Storage:          auth.Storage,
			FileName:         auth.FileName,
			Runtime:          auth.Runtime,
		}

		if auth.LastError != nil {
			newMeta.LastError = &Error{
				Code:       auth.LastError.Code,
				Message:    auth.LastError.Message,
				Retryable:  auth.LastError.Retryable,
				HTTPStatus: auth.LastError.HTTPStatus,
			}
		}

		if len(auth.Attributes) > 0 {
			newMeta.Attributes = make(map[string]string, len(auth.Attributes))
			for k, v := range auth.Attributes {
				newMeta.Attributes[k] = v
			}
		}

		if len(auth.Metadata) > 0 {
			newMeta.Metadata = make(map[string]any, len(auth.Metadata))
			for k, v := range auth.Metadata {
				newMeta.Metadata[k] = v
			}
		}

		return newMeta
	})

	entry.SetDisabled(auth.Disabled)
	entry.SetUnavailable(auth.Unavailable)

	if len(auth.ModelStates) > 0 {
		entry.UpdateAllModelStates(func(old *ModelStatesSnapshot) *ModelStatesSnapshot {
			newSnapshot := &ModelStatesSnapshot{
				States: make(map[string]ModelStateSnapshot, len(auth.ModelStates)),
			}
			for model, state := range auth.ModelStates {
				if state == nil {
					continue
				}
				ms := ModelStateSnapshot{
					Status:        state.Status,
					StatusMessage: state.StatusMessage,
					Unavailable:   state.Unavailable,
					BackoffLevel:  state.Quota.BackoffLevel,
					QuotaExceeded: state.Quota.Exceeded,
					QuotaReason:   state.Quota.Reason,
				}
				if !state.NextRetryAfter.IsZero() {
					ms.NextRetryAfter = state.NextRetryAfter.UnixNano()
				}
				if !state.Quota.NextRecoverAt.IsZero() {
					ms.QuotaRecover = state.Quota.NextRecoverAt.UnixNano()
				}
				if !state.UpdatedAt.IsZero() {
					ms.UpdatedAt = state.UpdatedAt.UnixNano()
				}
				if state.LastError != nil {
					ms.LastError = &Error{
						Code:       state.LastError.Code,
						Message:    state.LastError.Message,
						Retryable:  state.LastError.Retryable,
						HTTPStatus: state.LastError.HTTPStatus,
					}
				}
				newSnapshot.States[model] = ms
			}
			return newSnapshot
		})
	}

	r.scheduleRefreshIfNeeded(entry)
	r.markDirty(auth.ID)

	result := entry.ToAuth()
	r.hook.OnAuthUpdated(ctx, result)
	return result, nil
}

func (r *AuthRegistry) Delete(ctx context.Context, id string) error {
	if id == "" {
		return nil
	}

	shard := r.getShard(id)
	shard.mu.Lock()
	delete(shard.entries, id)
	shard.mu.Unlock()

	r.unscheduleRefresh(id)

	if r.store != nil {
		return r.store.Delete(ctx, id)
	}
	return nil
}

func (r *AuthRegistry) List() []*Auth {
	var result []*Auth
	for _, shard := range r.shards {
		shard.mu.RLock()
		for _, entry := range shard.entries {
			result = append(result, entry.ToAuth())
		}
		shard.mu.RUnlock()
	}
	return result
}

func (r *AuthRegistry) ListEntries() []*AuthEntry {
	var result []*AuthEntry
	for _, shard := range r.shards {
		shard.mu.RLock()
		for _, entry := range shard.entries {
			result = append(result, entry)
		}
		shard.mu.RUnlock()
	}
	return result
}

func (r *AuthRegistry) ListByProvider(provider string) []*AuthEntry {
	var result []*AuthEntry
	for _, shard := range r.shards {
		shard.mu.RLock()
		for _, entry := range shard.entries {
			if entry.Provider() == provider {
				result = append(result, entry)
			}
		}
		shard.mu.RUnlock()
	}
	return result
}

func (r *AuthRegistry) Count() int {
	total := 0
	for _, shard := range r.shards {
		shard.mu.RLock()
		total += len(shard.entries)
		shard.mu.RUnlock()
	}
	return total
}

func (r *AuthRegistry) Load(ctx context.Context) error {
	if r.store == nil {
		return nil
	}

	items, err := r.store.List(ctx)
	if err != nil {
		return err
	}

	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		auth.Index = r.nextIndex()
		entry := NewAuthEntry(auth)

		shard := r.getShard(auth.ID)
		shard.mu.Lock()
		shard.entries[auth.ID] = entry
		shard.mu.Unlock()

		r.scheduleRefreshIfNeeded(entry)
	}

	return nil
}

func (r *AuthRegistry) scheduleRefreshIfNeeded(entry *AuthEntry) {
	if entry == nil {
		return
	}

	meta := entry.Metadata()
	if meta == nil || meta.Metadata == nil {
		return
	}

	_, hasAccessToken := meta.Metadata["access_token"]
	_, hasRefreshToken := meta.Metadata["refresh_token"]
	if !hasAccessToken || !hasRefreshToken {
		return
	}

	refreshAt := entry.Token.GetRefreshAt()
	if refreshAt.IsZero() {
		expiresAt := entry.Token.GetExpiresAt()
		if expiresAt.IsZero() {
			return
		}
		refreshAt = expiresAt.Add(-5 * time.Minute)
		if refreshAt.Before(time.Now()) {
			refreshAt = time.Now().Add(5 * time.Second)
		}
		entry.Token.SetRefreshAt(refreshAt)
	}

	r.scheduleRefresh(entry.ID(), refreshAt)
}

func (r *AuthRegistry) scheduleRefresh(authID string, at time.Time) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	if existing, ok := r.refreshEntries[authID]; ok {
		heap.Remove(&r.refreshHeap, existing.index)
	}

	entry := &refreshHeapEntry{
		authID:    authID,
		refreshAt: at,
	}
	r.refreshEntries[authID] = entry
	heap.Push(&r.refreshHeap, entry)

	select {
	case r.refreshSignal <- struct{}{}:
	default:
	}
}

func (r *AuthRegistry) unscheduleRefresh(authID string) {
	r.refreshMu.Lock()
	defer r.refreshMu.Unlock()

	if entry, ok := r.refreshEntries[authID]; ok {
		heap.Remove(&r.refreshHeap, entry.index)
		delete(r.refreshEntries, authID)
	}
}

func (r *AuthRegistry) refreshLoop() {
	defer r.wg.Done()

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		r.refreshMu.Lock()
		var nextRefresh time.Time
		if r.refreshHeap.Len() > 0 {
			nextRefresh = r.refreshHeap[0].refreshAt
		}
		r.refreshMu.Unlock()

		var wait time.Duration
		if nextRefresh.IsZero() {
			wait = time.Hour
		} else {
			wait = time.Until(nextRefresh)
			if wait < 0 {
				wait = 0
			}
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)

		select {
		case <-r.stopCh:
			return
		case <-r.refreshSignal:
			continue
		case <-timer.C:
			if !nextRefresh.IsZero() {
				r.processNextRefresh()
			}
		}
	}
}

func (r *AuthRegistry) processNextRefresh() {
	r.refreshMu.Lock()
	if r.refreshHeap.Len() == 0 {
		r.refreshMu.Unlock()
		return
	}

	entry := heap.Pop(&r.refreshHeap).(*refreshHeapEntry)
	delete(r.refreshEntries, entry.authID)
	r.refreshMu.Unlock()

	go r.doRefresh(entry.authID)
}

func (r *AuthRegistry) doRefresh(authID string) {
	entry := r.GetEntry(authID)
	if entry == nil {
		return
	}

	if !entry.Token.TryStartRefresh() {
		return
	}
	defer entry.Token.FinishRefresh()

	if r.getExecutor == nil {
		return
	}

	exec := r.getExecutor(entry.Provider())
	if exec == nil {
		return
	}

	auth := entry.ToAuth()
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		updated, err := exec.Refresh(ctx, auth)
		cancel()

		if err == nil && updated != nil {
			entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
				newMeta := old.Clone()
				if newMeta.Metadata == nil {
					newMeta.Metadata = make(map[string]any)
				}
				for k, v := range updated.Metadata {
					newMeta.Metadata[k] = v
				}
				newMeta.LastRefreshedAt = time.Now()
				newMeta.LastError = nil
				newMeta.UpdatedAt = time.Now()
				return newMeta
			})

			if ts, ok := expirationFromMap(updated.Metadata); ok {
				entry.Token.SetExpiresAt(ts)
				refreshAt := ts.Add(-5 * time.Minute)
				if refreshAt.Before(time.Now()) {
					refreshAt = time.Now().Add(5 * time.Second)
				}
				entry.Token.SetRefreshAt(refreshAt)
				r.scheduleRefresh(authID, refreshAt)
			}

			r.markDirty(authID)
			log.Debugf("auth_registry: refreshed %s", authID)

			if r.hook != nil {
				go r.hook.OnAuthUpdated(context.Background(), entry.ToAuth())
			}
			return
		}

		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
	}

	log.Warnf("auth_registry: failed refresh %s after 3 attempts, retry in 1min", authID)
	r.scheduleRefresh(authID, time.Now().Add(time.Minute))
}

func (r *AuthRegistry) markDirty(authID string) {
	select {
	case r.persistQueue <- authID:
	default:
	}
}

func (r *AuthRegistry) persistLoop() {
	defer r.wg.Done()

	ticker := time.NewTicker(persistDebounceMs * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			r.flushPending()
			return
		case id := <-r.persistQueue:
			r.persistMu.Lock()
			r.persistBatch[id] = struct{}{}
			r.persistMu.Unlock()
		case <-ticker.C:
			r.flushPending()
		}
	}
}

func (r *AuthRegistry) flushPending() {
	if r.store == nil {
		return
	}

	r.persistMu.Lock()
	batch := r.persistBatch
	r.persistBatch = make(map[string]struct{})
	r.persistMu.Unlock()

	if len(batch) == 0 {
		return
	}

	ctx := context.Background()
	for id := range batch {
		entry := r.GetEntry(id)
		if entry == nil {
			continue
		}

		meta := entry.Metadata()
		if meta.Attributes != nil {
			if v := meta.Attributes["runtime_only"]; v == "true" {
				continue
			}
		}
		if meta.Metadata == nil {
			continue
		}

		auth := entry.ToAuth()
		if _, err := r.store.Save(ctx, auth); err != nil {
			log.Warnf("auth_registry: persist failed for %s: %v", id, err)
		}
	}
}

func (r *AuthRegistry) MarkResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}

	entry := r.GetEntry(result.AuthID)
	if entry == nil {
		return
	}

	now := time.Now()

	// Always decrement active requests for non-success results
	// This ensures active count stays accurate even if stream errors occur
	if !result.Success {
		entry.DecrementActiveRequests()
	}

	if result.Success {
		r.handleSuccessResult(ctx, entry, result, now)
	} else {
		r.handleFailureResult(ctx, entry, result, now)
	}

	r.markDirty(result.AuthID)
	r.hook.OnResult(ctx, result)
}

func (r *AuthRegistry) handleSuccessResult(ctx context.Context, entry *AuthEntry, result Result, now time.Time) {
	if result.Model == "" {
		entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
			return old.ClearError()
		})
		entry.SetUnavailable(false)
		entry.ClearCooldown()
		// Also clear QuotaManager cooldown to avoid false "all accounts exhausted" errors
		if r.quotaManager != nil {
			r.quotaManager.RecordRequestEnd(entry.ID(), entry.Provider(), 0, false)
		}
		return
	}

	entry.UpdateModelState(result.Model, func(old ModelStateSnapshot) ModelStateSnapshot {
		return ModelStateSnapshot{
			Status:    StatusActive,
			UpdatedAt: now.UnixNano(),
		}
	})

	entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
		newMeta := old.Clone()
		newMeta.LastError = nil
		newMeta.StatusMessage = ""
		newMeta.Status = StatusActive
		newMeta.UpdatedAt = now
		return newMeta
	})

	entry.SetUnavailable(false)
}

func (r *AuthRegistry) handleFailureResult(ctx context.Context, entry *AuthEntry, result Result, now time.Time) {
	if result.Model == "" {
		r.applyAuthLevelFailure(entry, result, now)
		return
	}

	statusCode := 0
	errMsg := ""
	if result.Error != nil {
		statusCode = result.Error.StatusCode()
		errMsg = result.Error.Message
	}
	category := CategorizeError(statusCode, errMsg)

	if category == CategoryUserError || category == CategoryClientCanceled {
		return
	}

	entry.UpdateModelState(result.Model, func(old ModelStateSnapshot) ModelStateSnapshot {
		newState := old.Clone()
		newState.Unavailable = true
		newState.Status = StatusError
		newState.UpdatedAt = now.UnixNano()

		if result.Error != nil {
			newState.LastError = &Error{
				Code:       result.Error.Code,
				Message:    result.Error.Message,
				Retryable:  result.Error.Retryable,
				HTTPStatus: result.Error.HTTPStatus,
			}
			newState.StatusMessage = result.Error.Message
		}

		switch statusCode {
		case 401:
			newState.NextRetryAfter = now.Add(30 * time.Minute).UnixNano()
		case 402, 403:
			newState.NextRetryAfter = now.Add(30 * time.Minute).UnixNano()
		case 404:
			newState.NextRetryAfter = now.Add(12 * time.Hour).UnixNano()
		case 429:
			var next time.Time
			if result.RetryAfter != nil {
				next = now.Add(*result.RetryAfter)
			} else {
				cooldown, nextLevel := nextQuotaCooldown(old.BackoffLevel)
				if cooldown > 0 {
					next = now.Add(cooldown)
				}
				newState.BackoffLevel = nextLevel
			}
			newState.NextRetryAfter = next.UnixNano()
			newState.QuotaExceeded = true
			newState.QuotaReason = "quota"
			newState.QuotaRecover = next.UnixNano()
		case 408, 500, 502, 503, 504:
			newState.NextRetryAfter = now.Add(time.Minute).UnixNano()
		default:
			newState.NextRetryAfter = now.Add(30 * time.Second).UnixNano()
		}

		return newState
	})

	entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
		newMeta := old.Clone()
		newMeta.Status = StatusError
		newMeta.UpdatedAt = now
		if result.Error != nil {
			newMeta.LastError = &Error{
				Code:       result.Error.Code,
				Message:    result.Error.Message,
				Retryable:  result.Error.Retryable,
				HTTPStatus: result.Error.HTTPStatus,
			}
			newMeta.StatusMessage = result.Error.Message
		}
		return newMeta
	})
}

func (r *AuthRegistry) applyAuthLevelFailure(entry *AuthEntry, result Result, now time.Time) {
	entry.SetUnavailable(true)

	category := CategoryUnknown
	if result.Error != nil && result.Error.ErrCategory != CategoryUnknown {
		category = result.Error.ErrCategory
	} else if result.Error != nil {
		category = CategorizeError(result.Error.StatusCode(), result.Error.Message)
	}

	entry.UpdateMetadata(func(old *AuthMetadata) *AuthMetadata {
		newMeta := old.Clone()
		newMeta.Status = StatusError
		newMeta.UpdatedAt = now

		if result.Error != nil {
			newMeta.LastError = &Error{
				Code:       result.Error.Code,
				Message:    result.Error.Message,
				Retryable:  result.Error.Retryable,
				HTTPStatus: result.Error.HTTPStatus,
			}
			newMeta.StatusMessage = result.Error.Message
		}

		switch category {
		case CategoryAuthRevoked:
			newMeta.StatusMessage = "oauth_token_revoked"
			newMeta.Status = StatusDisabled
		case CategoryAuthError:
			newMeta.StatusMessage = "unauthorized"
			newMeta.NextRetryAfter = now.Add(30 * time.Minute)
		case CategoryQuotaError:
			newMeta.StatusMessage = "quota exhausted"
			var next time.Time
			if result.RetryAfter != nil {
				next = now.Add(*result.RetryAfter)
			} else {
				cooldown, _ := nextQuotaCooldown(0)
				if cooldown > 0 {
					next = now.Add(cooldown)
				}
			}
			newMeta.NextRetryAfter = next
		case CategoryNotFound:
			newMeta.StatusMessage = "not_found"
			newMeta.NextRetryAfter = now.Add(12 * time.Hour)
		case CategoryTransient:
			newMeta.StatusMessage = "transient upstream error"
			newMeta.NextRetryAfter = now.Add(time.Minute)
		case CategoryUserError:
			newMeta.StatusMessage = "user_request_error"
			newMeta.Status = StatusActive
		default:
			if newMeta.StatusMessage == "" {
				newMeta.StatusMessage = "request failed"
			}
		}

		return newMeta
	})

	if category == CategoryAuthRevoked {
		entry.SetDisabled(true)
	}

	if category == CategoryQuotaError {
		var cooldownUntil time.Time
		if result.RetryAfter != nil {
			cooldownUntil = now.Add(*result.RetryAfter)
		} else {
			cooldown, _ := nextQuotaCooldown(0)
			cooldownUntil = now.Add(cooldown)
		}
		entry.SetCooldown(cooldownUntil)
	}
}

func (r *AuthRegistry) Pick(ctx context.Context, provider, model string, opts Options, auths []*AuthEntry) (*AuthEntry, error) {
	if len(auths) == 0 {
		return nil, &Error{Code: "auth_not_found", Message: "no auth candidates"}
	}

	now := time.Now()
	available := make([]*AuthEntry, 0, len(auths))

	var earliest time.Time
	cooldownCount := 0

	for _, entry := range auths {
		if entry.IsDisabled() {
			continue
		}

		if entry.IsInCooldown(now) {
			cooldownCount++
			cd := entry.Quota.GetCooldownUntil()
			if earliest.IsZero() || cd.Before(earliest) {
				earliest = cd
			}
			continue
		}

		blocked, reason, retryAt := entry.IsBlockedForModel(model, now)
		if blocked {
			if reason == blockReasonCooldown || reason == blockReasonOther {
				if !retryAt.IsZero() && (earliest.IsZero() || retryAt.Before(earliest)) {
					earliest = retryAt
				}
			}
			if reason == blockReasonCooldown {
				cooldownCount++
			}
			continue
		}

		available = append(available, entry)
	}

	if len(available) == 0 {
		if !earliest.IsZero() {
			resetIn := earliest.Sub(now)
			if resetIn < 0 {
				resetIn = 0
			}
			return nil, newModelCooldownError(model, provider, resetIn)
		}
		return nil, &Error{Code: "auth_unavailable", Message: "no auth available"}
	}

	if len(available) == 1 {
		available[0].IncrementActiveRequests()
		return available[0], nil
	}

	var selected *AuthEntry
	minActive := int64(1<<63 - 1)
	for _, entry := range available {
		active := entry.Quota.ActiveRequests.Load()
		if active < minActive {
			minActive = active
			selected = entry
		}
	}

	if selected == nil {
		selected = available[0]
	}

	selected.IncrementActiveRequests()
	return selected, nil
}

func (r *AuthRegistry) PickAuth(ctx context.Context, provider, model string, opts Options, auths []*Auth) (*Auth, error) {
	entries := make([]*AuthEntry, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		if entry := r.GetEntry(auth.ID); entry != nil {
			entries = append(entries, entry)
		}
	}
	selected, err := r.Pick(ctx, provider, model, opts, entries)
	if err != nil {
		return nil, err
	}
	return selected.ToAuth(), nil
}

func (r *AuthRegistry) PickFromIDs(ctx context.Context, provider, model string, opts Options, authIDs []string) (*AuthEntry, error) {
	var entries []*AuthEntry
	for _, id := range authIDs {
		if entry := r.GetEntry(id); entry != nil && entry.Provider() == provider {
			entries = append(entries, entry)
		}
	}
	return r.Pick(ctx, provider, model, opts, entries)
}
