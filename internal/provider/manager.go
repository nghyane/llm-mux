package provider

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/registry"
	"github.com/nghyane/llm-mux/internal/resilience"
	"github.com/sony/gobreaker"
	"golang.org/x/sync/semaphore"
)

func init() {
	resilience.DefaultIsSuccessful = func(err error) bool {
		if err == nil {
			return true
		}
		cat := CategorizeError(0, err.Error())
		return cat.IsUserFault()
	}
}

// ProviderExecutor defines the contract required by Manager to execute provider calls.
type ProviderExecutor interface {
	// Identifier returns the provider key handled by this executor.
	Identifier() string
	// Execute handles non-streaming execution and returns the provider response payload.
	Execute(ctx context.Context, auth *Auth, req Request, opts Options) (Response, error)
	// ExecuteStream handles streaming execution and returns a channel of provider chunks.
	ExecuteStream(ctx context.Context, auth *Auth, req Request, opts Options) (<-chan StreamChunk, error)
	// Refresh attempts to refresh provider credentials and returns the updated auth state.
	Refresh(ctx context.Context, auth *Auth) (*Auth, error)
	// CountTokens returns the token count for the given request.
	CountTokens(ctx context.Context, auth *Auth, req Request, opts Options) (Response, error)
}

// RefreshEvaluator allows runtime state to override refresh decisions.
type RefreshEvaluator interface {
	ShouldRefresh(now time.Time, auth *Auth) bool
}

// Result captures execution outcome used to adjust auth state.
type Result struct {
	// AuthID references the auth that produced this result.
	AuthID string
	// Provider is copied for convenience when emitting hooks.
	Provider string
	// Model is the upstream model identifier used for the request.
	Model string
	// Success marks whether the execution succeeded.
	Success bool
	// RetryAfter carries a provider supplied retry hint (e.g. 429 retryDelay).
	RetryAfter *time.Duration
	// Error describes the failure when Success is false.
	Error *Error
}

// Selector chooses an auth candidate for execution.
type Selector interface {
	Pick(ctx context.Context, provider, model string, opts Options, auths []*Auth) (*Auth, error)
}

// Hook captures lifecycle callbacks for observing auth changes.
type Hook interface {
	// OnAuthRegistered fires when a new auth is registered.
	OnAuthRegistered(ctx context.Context, auth *Auth)
	// OnAuthUpdated fires when an existing auth changes state.
	OnAuthUpdated(ctx context.Context, auth *Auth)
	// OnResult fires when execution result is recorded.
	OnResult(ctx context.Context, result Result)
}

// NoopHook provides optional hook defaults.
type NoopHook struct{}

// OnAuthRegistered implements Hook.
func (NoopHook) OnAuthRegistered(context.Context, *Auth) {}

// OnAuthUpdated implements Hook.
func (NoopHook) OnAuthUpdated(context.Context, *Auth) {}

// OnResult implements Hook.
func (NoopHook) OnResult(context.Context, Result) {}

// Manager orchestrates auth lifecycle, selection, execution, and persistence.
type Manager struct {
	store     Store
	executors map[string]ProviderExecutor
	selector  Selector
	hook      Hook
	mu        sync.RWMutex
	auths     map[string]*Auth

	providerCounter atomic.Uint64
	providerStats   *ProviderStats

	requestRetry     atomic.Int32
	maxRetryInterval atomic.Int64

	rtProvider RoundTripperProvider

	refreshCancel context.CancelFunc
	refreshSem    *semaphore.Weighted // limits concurrent refresh goroutines

	breakerMu         sync.RWMutex
	breakers          map[string]*resilience.CircuitBreaker
	streamingBreakers map[string]*resilience.StreamingCircuitBreaker

	retryBudget *resilience.RetryBudget

	// Async result processing for reduced lock contention
	resultWorker *asyncResultWorker
}

// NewManager constructs a manager with optional custom selector and hook.
// If no selector is provided, uses QuotaManager with provider-aware quota tracking.
func NewManager(store Store, selector Selector, hook Hook) *Manager {
	if selector == nil {
		selector = NewQuotaManager()
	}
	if hook == nil {
		hook = NoopHook{}
	}
	m := &Manager{
		store:             store,
		executors:         make(map[string]ProviderExecutor),
		selector:          selector,
		hook:              hook,
		auths:             make(map[string]*Auth),
		providerStats:     NewProviderStats(),
		breakers:          make(map[string]*resilience.CircuitBreaker),
		streamingBreakers: make(map[string]*resilience.StreamingCircuitBreaker),
		retryBudget:       resilience.NewRetryBudget(100), // Allow up to 100 concurrent retries
		refreshSem:        newRefreshSemaphore(),          // Bound concurrent refresh goroutines
	}
	// Initialize async result worker for reduced lock contention
	m.resultWorker = newAsyncResultWorker(m, resultWorkerConfig{
		QueueSize: 2048,
		Workers:   4,
	})
	if lc, ok := selector.(SelectorLifecycle); ok {
		lc.Start()
	}
	return m
}

// Stop gracefully shuts down the manager and its components.
func (m *Manager) Stop() {
	if m.refreshCancel != nil {
		m.refreshCancel()
	}
	// Stop async result worker
	if m.resultWorker != nil {
		m.resultWorker.Stop()
	}
	m.mu.RLock()
	selector := m.selector
	m.mu.RUnlock()
	if lc, ok := selector.(SelectorLifecycle); ok {
		lc.Stop()
	}
}

// SetStore swaps the underlying persistence store.
func (m *Manager) SetStore(store Store) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.store = store
}

// SetRoundTripperProvider register a provider that returns a per-auth RoundTripper.
func (m *Manager) SetRoundTripperProvider(p RoundTripperProvider) {
	m.mu.Lock()
	m.rtProvider = p
	m.mu.Unlock()
}

// SetRetryConfig updates retry attempts and cooldown wait interval.
func (m *Manager) SetRetryConfig(retry int, maxRetryInterval time.Duration) {
	if m == nil {
		return
	}
	if retry < 0 {
		retry = 0
	}
	if maxRetryInterval < 0 {
		maxRetryInterval = 0
	}
	m.requestRetry.Store(int32(retry))
	m.maxRetryInterval.Store(maxRetryInterval.Nanoseconds())
}

// RegisterExecutor registers a provider executor with the manager.
func (m *Manager) RegisterExecutor(executor ProviderExecutor) {
	if executor == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.executors[executor.Identifier()] = executor
}

// UnregisterExecutor removes the executor associated with the provider key.
func (m *Manager) UnregisterExecutor(provider string) {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "" {
		return
	}
	m.mu.Lock()
	delete(m.executors, provider)
	m.mu.Unlock()
}

// Register inserts a new auth entry into the manager.
func (m *Manager) Register(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil {
		return nil, nil
	}
	auth.EnsureIndex()
	if auth.ID == "" {
		auth.ID = uuid.NewString()
	}
	m.mu.Lock()
	m.auths[auth.ID] = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(ctx, auth)
	m.hook.OnAuthRegistered(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Update replaces an existing auth entry and notifies hooks.
func (m *Manager) Update(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth == nil || auth.ID == "" {
		return nil, nil
	}
	m.mu.Lock()
	if existing, ok := m.auths[auth.ID]; ok && existing != nil && !auth.indexAssigned && auth.Index == 0 {
		auth.Index = existing.Index
		auth.indexAssigned = existing.indexAssigned
	}
	auth.EnsureIndex()
	m.auths[auth.ID] = auth.Clone()
	m.mu.Unlock()
	_ = m.persist(ctx, auth)
	m.hook.OnAuthUpdated(ctx, auth.Clone())
	return auth.Clone(), nil
}

// Load resets manager state from the backing store.
func (m *Manager) Load(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	items, err := m.store.List(ctx)
	if err != nil {
		return err
	}
	m.auths = make(map[string]*Auth, len(items))
	for _, auth := range items {
		if auth == nil || auth.ID == "" {
			continue
		}
		auth.EnsureIndex()
		m.auths[auth.ID] = auth.Clone()
	}
	return nil
}

// Execute performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model with weighted selection based on performance.
func (m *Manager) Execute(ctx context.Context, providers []string, req Request, opts Options) (Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	selected := m.selectProviders(req.Model, normalized)

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	var lastProvider string
	for attempt := 0; attempt < attempts; attempt++ {
		acquiredBudget := false
		if attempt > 0 {
			if !m.retryBudget.TryAcquire() {
				break
			}
			acquiredBudget = true
		}

		start := time.Now()
		resp, errExec := m.executeProvidersOnce(ctx, selected, func(execCtx context.Context, provider string) (Response, error) {
			lastProvider = provider
			return m.executeWithProvider(execCtx, provider, req, opts)
		})
		latency := time.Since(start)

		if errExec == nil {
			// Record success for weighted selection
			m.recordProviderResult(lastProvider, req.Model, true, latency)
			if acquiredBudget {
				m.retryBudget.Release()
			}
			return resp, nil
		}

		// Record failure for weighted selection
		m.recordProviderResult(lastProvider, req.Model, false, latency)
		lastErr = errExec

		if acquiredBudget {
			m.retryBudget.Release()
		}

		if !m.shouldRetryAfterError(errExec, attempt, attempts, selected, req.Model) {
			break
		}
		if errWait := m.waitForAvailableAuth(ctx, selected, req.Model, maxWait); errWait != nil {
			break
		}
	}
	if lastErr != nil {
		return Response{}, lastErr
	}
	return Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteCount performs a non-streaming execution using the configured selector and executor.
// It supports multiple providers for the same model with weighted selection based on performance.
func (m *Manager) ExecuteCount(ctx context.Context, providers []string, req Request, opts Options) (Response, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return Response{}, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	selected := m.selectProviders(req.Model, normalized)

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	var lastProvider string
	for attempt := 0; attempt < attempts; attempt++ {
		acquiredBudget := false
		if attempt > 0 {
			if !m.retryBudget.TryAcquire() {
				break
			}
			acquiredBudget = true
		}

		start := time.Now()
		resp, errExec := m.executeProvidersOnce(ctx, selected, func(execCtx context.Context, provider string) (Response, error) {
			lastProvider = provider
			return m.executeCountWithProvider(execCtx, provider, req, opts)
		})
		latency := time.Since(start)

		if errExec == nil {
			m.recordProviderResult(lastProvider, req.Model, true, latency)
			if acquiredBudget {
				m.retryBudget.Release()
			}
			return resp, nil
		}

		m.recordProviderResult(lastProvider, req.Model, false, latency)
		lastErr = errExec

		if acquiredBudget {
			m.retryBudget.Release()
		}

		if !m.shouldRetryAfterError(errExec, attempt, attempts, selected, req.Model) {
			break
		}
		if errWait := m.waitForAvailableAuth(ctx, selected, req.Model, maxWait); errWait != nil {
			break
		}
	}
	if lastErr != nil {
		return Response{}, lastErr
	}
	return Response{}, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// ExecuteStream performs a streaming execution using the configured selector and executor.
// It supports multiple providers for the same model with weighted selection based on performance.
// Stats tracking is now consolidated in executeStreamWithProvider to reduce wrapper overhead.
func (m *Manager) ExecuteStream(ctx context.Context, providers []string, req Request, opts Options) (<-chan StreamChunk, error) {
	normalized := m.normalizeProviders(providers)
	if len(normalized) == 0 {
		return nil, &Error{Code: "provider_not_found", Message: "no provider supplied"}
	}
	selected := m.selectProviders(req.Model, normalized)

	retryTimes, maxWait := m.retrySettings()
	attempts := retryTimes + 1
	if attempts < 1 {
		attempts = 1
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		acquiredBudget := false
		if attempt > 0 {
			if !m.retryBudget.TryAcquire() {
				break
			}
			acquiredBudget = true
		}

		// Stats are now tracked inside executeStreamWithProvider - no need for wrapStreamForStats
		chunks, errStream := m.executeStreamProvidersOnce(ctx, selected, func(execCtx context.Context, provider string) (<-chan StreamChunk, error) {
			return m.executeStreamWithProvider(execCtx, provider, req, opts)
		})

		if errStream == nil {
			if acquiredBudget {
				m.retryBudget.Release()
			}
			// Return directly - stats tracking is now inline in executeStreamWithProvider
			return chunks, nil
		}

		lastErr = errStream

		if acquiredBudget {
			m.retryBudget.Release()
		}

		if !m.shouldRetryAfterError(errStream, attempt, attempts, selected, req.Model) {
			break
		}
		if errWait := m.waitForAvailableAuth(ctx, selected, req.Model, maxWait); errWait != nil {
			break
		}
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, &Error{Code: "auth_not_found", Message: "no auth available"}
}

// MarkResult records an execution result and notifies hooks.
// Uses async worker to reduce lock contention in the hot path.
func (m *Manager) MarkResult(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}
	// Delegate to async worker for non-blocking processing
	if m.resultWorker != nil {
		m.resultWorker.Submit(ctx, result)
		return
	}
	// Fallback to sync processing if worker not initialized
	m.markResultSync(ctx, result)
}

// markResultSync is the synchronous fallback for MarkResult.
// Used when async worker is not available or queue is full.
func (m *Manager) markResultSync(ctx context.Context, result Result) {
	if result.AuthID == "" {
		return
	}

	shouldResumeModel := false
	shouldSuspendModel := false
	suspendReason := ""
	clearModelQuota := false
	setModelQuota := false

	m.mu.Lock()
	if auth, ok := m.auths[result.AuthID]; ok && auth != nil {
		now := time.Now()

		if result.Success {
			if result.Model != "" {
				state := ensureModelState(auth, result.Model)
				resetModelState(state, now)

				// Clear quota for all models in the same quota group
				// (e.g., for Antigravity: if one Claude model succeeds, others in group can retry)
				clearedModels := clearQuotaGroupOnSuccess(auth, result.Model, now)
				for _, clearedModel := range clearedModels {
					registry.GetGlobalRegistry().ClearModelQuotaExceeded(result.AuthID, clearedModel)
					registry.GetGlobalRegistry().ResumeClientModel(result.AuthID, clearedModel)
				}

				updateAggregatedAvailability(auth, now)
				if !hasModelError(auth, now) {
					auth.LastError = nil
					auth.StatusMessage = ""
					auth.Status = StatusActive
				}
				auth.UpdatedAt = now
				shouldResumeModel = true
				clearModelQuota = true
			} else {
				clearAuthStateOnSuccess(auth, now)
			}
		} else {
			if result.Model != "" {
				state := ensureModelState(auth, result.Model)
				statusCode := statusCodeFromResult(result.Error)

				// Determine error category to decide if auth should be marked unavailable
				var errMsg string
				if result.Error != nil {
					errMsg = result.Error.Message
				}
				category := CategorizeError(statusCode, errMsg)

				// User errors (400) should NOT mark auth as unavailable
				if category != CategoryUserError {
					state.Unavailable = true
					state.Status = StatusError
				}
				state.UpdatedAt = now
				// Only record error details for non-user errors
				if result.Error != nil && category != CategoryUserError {
					state.LastError = cloneError(result.Error)
					state.StatusMessage = result.Error.Message
					auth.LastError = cloneError(result.Error)
					auth.StatusMessage = result.Error.Message
				}
				switch statusCode {
				case 401:
					next := now.Add(30 * time.Minute)
					state.NextRetryAfter = next
					suspendReason = "unauthorized"
					shouldSuspendModel = true
				case 402, 403:
					next := now.Add(30 * time.Minute)
					state.NextRetryAfter = next
					suspendReason = "payment_required"
					shouldSuspendModel = true
				case 404:
					next := now.Add(12 * time.Hour)
					state.NextRetryAfter = next
					suspendReason = "not_found"
					shouldSuspendModel = true
				case 429:
					var next time.Time
					backoffLevel := state.Quota.BackoffLevel
					if result.RetryAfter != nil {
						next = now.Add(*result.RetryAfter)
					} else {
						cooldown, nextLevel := nextQuotaCooldown(backoffLevel)
						if cooldown > 0 {
							next = now.Add(cooldown)
						}
						backoffLevel = nextLevel
					}
					state.NextRetryAfter = next
					state.Quota = QuotaState{
						Exceeded:      true,
						Reason:        "quota",
						NextRecoverAt: next,
						BackoffLevel:  backoffLevel,
					}
					suspendReason = "quota"
					shouldSuspendModel = true
					setModelQuota = true

					// Propagate quota to all models in the same quota group
					// (e.g., for Antigravity: all Claude models share quota)
					affectedModels := propagateQuotaToGroup(auth, result.Model, state.Quota, next, now)
					for _, affectedModel := range affectedModels {
						registry.GetGlobalRegistry().SetModelQuotaExceeded(result.AuthID, affectedModel)
						registry.GetGlobalRegistry().SuspendClientModel(result.AuthID, affectedModel, "quota_group")
					}
				case 408, 500, 502, 503, 504:
					next := now.Add(1 * time.Minute)
					state.NextRetryAfter = next
				default:
					// Unknown/unhandled errors (network failures, parsing errors, etc.)
					// Set short cooldown to enable auto-recovery via updateAggregatedAvailability
					state.NextRetryAfter = now.Add(30 * time.Second)
				}

				// Only update auth-level status for non-user errors
				if category != CategoryUserError {
					auth.Status = StatusError
				}
				auth.UpdatedAt = now
				updateAggregatedAvailability(auth, now)
			} else {
				applyAuthFailureState(auth, result.Error, result.RetryAfter, now)
			}
		}

		_ = m.persist(ctx, auth)
	}
	m.mu.Unlock()

	if clearModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().ClearModelQuotaExceeded(result.AuthID, result.Model)
	}
	if setModelQuota && result.Model != "" {
		registry.GetGlobalRegistry().SetModelQuotaExceeded(result.AuthID, result.Model)
	}
	if shouldResumeModel {
		registry.GetGlobalRegistry().ResumeClientModel(result.AuthID, result.Model)
	} else if shouldSuspendModel {
		registry.GetGlobalRegistry().SuspendClientModel(result.AuthID, result.Model, suspendReason)
	}

	if result.Error != nil && result.Error.HTTPStatus == 429 {
		if qm, ok := m.selector.(*QuotaManager); ok {
			qm.RecordQuotaHit(result.AuthID, result.Provider, result.Model, result.RetryAfter)
		}
	}

	m.hook.OnResult(ctx, result)
}

// GetProviderStats returns current provider statistics for monitoring.
func (m *Manager) GetProviderStats() map[string]map[string]int64 {
	if m.providerStats == nil {
		return nil
	}
	return m.providerStats.Stats()
}

// CleanupProviderStats removes stale provider statistics older than maxAge.
func (m *Manager) CleanupProviderStats(maxAge time.Duration) int {
	if m.providerStats == nil {
		return 0
	}
	return m.providerStats.Cleanup(maxAge)
}

// List returns all auth entries currently known by the manager.
func (m *Manager) List() []*Auth {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*Auth, 0, len(m.auths))
	for _, auth := range m.auths {
		list = append(list, auth.Clone())
	}
	return list
}

// GetByID retrieves an auth entry by its ID.

func (m *Manager) GetByID(id string) (*Auth, bool) {
	if id == "" {
		return nil, false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth, ok := m.auths[id]
	if !ok {
		return nil, false
	}
	return auth.Clone(), true
}

func (m *Manager) pickNext(ctx context.Context, provider, model string, opts Options, tried map[string]struct{}) (*Auth, ProviderExecutor, error) {
	// Phase 1: Quick read-locked filter to collect candidate pointers
	m.mu.RLock()
	executor, okExecutor := m.executors[provider]
	if !okExecutor {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "executor_not_found", Message: "executor not registered"}
	}

	// Avoid allocation when model doesn't need trimming
	modelKey := model
	if len(model) > 0 && (model[0] == ' ' || model[len(model)-1] == ' ') {
		modelKey = strings.TrimSpace(model)
	}

	// Collect candidate pointers under lock (cheap - no cloning yet)
	candidatePtrs := make([]*Auth, 0, len(m.auths))
	registryRef := registry.GetGlobalRegistry()
	for _, candidate := range m.auths {
		if candidate.Provider != provider || candidate.Disabled {
			continue
		}
		if _, used := tried[candidate.ID]; used {
			continue
		}
		if modelKey != "" && registryRef != nil && !registryRef.ClientSupportsModel(candidate.ID, modelKey) {
			continue
		}
		candidatePtrs = append(candidatePtrs, candidate)
	}
	if len(candidatePtrs) == 0 {
		m.mu.RUnlock()
		return nil, nil, &Error{Code: "auth_not_found", Message: "no auth available"}
	}

	// Phase 2: Clone candidates while still under RLock (necessary for consistent snapshot)
	// But now we only clone the filtered set, not all candidates
	candidates := make([]*Auth, len(candidatePtrs))
	for i, ptr := range candidatePtrs {
		candidates[i] = ptr.Clone()
	}
	m.mu.RUnlock()

	// Phase 3: Selector runs outside lock - OK because we have cloned data
	selected, errPick := m.selector.Pick(ctx, provider, model, opts, candidates)
	if errPick != nil {
		return nil, nil, errPick
	}
	if selected == nil {
		return nil, nil, &Error{Code: "auth_not_found", Message: "selector returned no auth"}
	}

	// Phase 4: Handle index assignment (rare path)
	authCopy := selected
	if !selected.indexAssigned {
		m.mu.Lock()
		if current := m.auths[authCopy.ID]; current != nil && !current.indexAssigned {
			current.EnsureIndex()
			authCopy = current.Clone()
		}
		m.mu.Unlock()
	}
	return authCopy, executor, nil
}

func (m *Manager) persist(ctx context.Context, auth *Auth) error {
	if m.store == nil || auth == nil {
		return nil
	}
	if auth.Attributes != nil {
		if v := strings.ToLower(strings.TrimSpace(auth.Attributes["runtime_only"])); v == "true" {
			return nil
		}
	}
	// Skip persistence when metadata is absent (e.g., runtime-only auths).
	if auth.Metadata == nil {
		return nil
	}
	_, err := m.store.Save(ctx, auth)
	return err
}

func (m *Manager) markRefreshPending(id string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	auth, ok := m.auths[id]
	if !ok || auth == nil || auth.Disabled {
		return false
	}
	if !auth.NextRefreshAfter.IsZero() && now.Before(auth.NextRefreshAfter) {
		return false
	}
	auth.NextRefreshAfter = now.Add(refreshPendingBackoff)
	m.auths[id] = auth
	return true
}

func (m *Manager) refreshAuth(ctx context.Context, id string) {
	m.mu.RLock()
	auth := m.auths[id]
	var exec ProviderExecutor
	if auth != nil {
		exec = m.executors[auth.Provider]
	}
	m.mu.RUnlock()
	if auth == nil || exec == nil {
		return
	}
	cloned := auth.Clone()
	authUpdatedAt := auth.UpdatedAt
	updated, err := exec.Refresh(ctx, cloned)
	log.Debugf("refreshed %s, %s, %v", auth.Provider, auth.ID, err)
	now := time.Now()
	if err != nil {
		m.mu.Lock()
		if current := m.auths[id]; current != nil && current.UpdatedAt == authUpdatedAt {
			current.NextRefreshAfter = now.Add(refreshFailureBackoff)
			current.LastError = &Error{Message: err.Error()}
			m.auths[id] = current
		}
		m.mu.Unlock()
		return
	}
	if updated == nil {
		updated = cloned
	}
	// Preserve runtime created by the executor during Refresh.
	// If executor didn't set one, fall back to the previous runtime.
	if updated.Runtime == nil {
		updated.Runtime = auth.Runtime
	}
	updated.LastRefreshedAt = now
	updated.NextRefreshAfter = time.Time{}
	updated.LastError = nil
	updated.UpdatedAt = now
	_, _ = m.Update(ctx, updated)
}

func (m *Manager) executorFor(provider string) ProviderExecutor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.executors[provider]
}

// roundTripperContextKey is an unexported context key type to avoid collisions.
type roundTripperContextKey struct{}

// roundTripperFor retrieves an HTTP RoundTripper for the given auth if a provider is registered.
func (m *Manager) roundTripperFor(auth *Auth) http.RoundTripper {
	m.mu.RLock()
	p := m.rtProvider
	m.mu.RUnlock()
	if p == nil || auth == nil {
		return nil
	}
	return p.RoundTripperFor(auth)
}

// RoundTripperProvider defines a minimal provider of per-auth HTTP transports.
type RoundTripperProvider interface {
	RoundTripperFor(auth *Auth) http.RoundTripper
}

// RequestPreparer is an optional interface that provider executors can implement
// to mutate outbound HTTP requests with provider credentials.
type RequestPreparer interface {
	PrepareRequest(req *http.Request, auth *Auth) error
}

// InjectCredentials delegates per-provider HTTP request preparation when supported.
// If the registered executor for the auth provider implements RequestPreparer,
// it will be invoked to modify the request (e.g., add headers).
func (m *Manager) InjectCredentials(req *http.Request, authID string) error {
	if req == nil || authID == "" {
		return nil
	}
	m.mu.RLock()
	a := m.auths[authID]
	var exec ProviderExecutor
	if a != nil {
		exec = m.executors[a.Provider]
	}
	m.mu.RUnlock()
	if a == nil || exec == nil {
		return nil
	}
	if p, ok := exec.(RequestPreparer); ok && p != nil {
		return p.PrepareRequest(req, a)
	}
	return nil
}

func (m *Manager) getOrCreateBreaker(provider string) *resilience.CircuitBreaker {
	m.breakerMu.RLock()
	if cb, ok := m.breakers[provider]; ok {
		m.breakerMu.RUnlock()
		return cb
	}
	m.breakerMu.RUnlock()

	m.breakerMu.Lock()
	defer m.breakerMu.Unlock()
	if cb, ok := m.breakers[provider]; ok {
		return cb
	}

	cfg := resilience.DefaultBreakerConfig("provider:" + provider)
	cfg.OnStateChange = func(name string, from, to gobreaker.State) {
		log.Infof("circuit breaker %s: %s -> %s", name, from, to)
	}
	cb := resilience.NewCircuitBreaker(cfg)
	m.breakers[provider] = cb
	return cb
}

func (m *Manager) getOrCreateStreamingBreaker(provider string) *resilience.StreamingCircuitBreaker {
	m.breakerMu.RLock()
	if cb, ok := m.streamingBreakers[provider]; ok {
		m.breakerMu.RUnlock()
		return cb
	}
	m.breakerMu.RUnlock()

	m.breakerMu.Lock()
	defer m.breakerMu.Unlock()
	if cb, ok := m.streamingBreakers[provider]; ok {
		return cb
	}

	cfg := resilience.DefaultBreakerConfig("streaming:" + provider)
	cfg.OnStateChange = func(name string, from, to gobreaker.State) {
		log.Infof("circuit breaker %s: %s -> %s", name, from, to)
	}
	cb := resilience.NewStreamingCircuitBreaker(cfg)
	m.streamingBreakers[provider] = cb
	return cb
}

func (m *Manager) BreakerState(provider string) gobreaker.State {
	m.breakerMu.RLock()
	cb, ok := m.breakers[provider]
	m.breakerMu.RUnlock()
	if !ok {
		return gobreaker.StateClosed
	}
	return cb.State()
}

// RetryBudgetStats returns current retry budget status.
func (m *Manager) RetryBudgetStats() (available, max int64) {
	if m.retryBudget == nil {
		return 0, 0
	}
	return m.retryBudget.Available(), m.retryBudget.MaxCapacity()
}

// GetQuotaManager returns the QuotaManager if it is the configured selector.
func (m *Manager) GetQuotaManager() *QuotaManager {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if qm, ok := m.selector.(*QuotaManager); ok {
		return qm
	}
	return nil
}
