package registry

import (
	"sync"
	"sync/atomic"
	"time"
)

type ModelInfo struct {
	ID                         string           `json:"id"`
	Object                     string           `json:"object"`
	Created                    int64            `json:"created"`
	OwnedBy                    string           `json:"owned_by"`
	Type                       string           `json:"type"`
	CanonicalID                string           `json:"canonical_id,omitempty"`
	DisplayName                string           `json:"display_name,omitempty"`
	Name                       string           `json:"name,omitempty"`
	Version                    string           `json:"version,omitempty"`
	Description                string           `json:"description,omitempty"`
	InputTokenLimit            int              `json:"inputTokenLimit,omitempty"`
	OutputTokenLimit           int              `json:"outputTokenLimit,omitempty"`
	SupportedGenerationMethods []string         `json:"supportedGenerationMethods,omitempty"`
	ContextLength              int              `json:"context_length,omitempty"`
	MaxCompletionTokens        int              `json:"max_completion_tokens,omitempty"`
	SupportedParameters        []string         `json:"supported_parameters,omitempty"`
	Thinking                   *ThinkingSupport `json:"thinking,omitempty"`
	Priority                   int              `json:"priority,omitempty"`
	UpstreamName               string           `json:"-"`
	Hidden                     bool             `json:"-"`
}

type ThinkingSupport struct {
	Min            int  `json:"min,omitempty"`
	Max            int  `json:"max,omitempty"`
	ZeroAllowed    bool `json:"zero_allowed,omitempty"`
	DynamicAllowed bool `json:"dynamic_allowed,omitempty"`
}

type ModelRegistration struct {
	Info                 *ModelInfo
	Count                int
	LastUpdated          time.Time
	QuotaExceededClients map[string]*time.Time
	Providers            map[string]int
	SuspendedClients     map[string]string
}

type ProviderModelMapping struct {
	Provider string
	ModelID  string
	Priority int
}

// ModelRegistry uses Copy-on-Write for lock-free reads.
// Reads: Load atomic pointer, work with immutable snapshot.
// Writes: Lock writerMu, clone state, modify clone, store atomically.
type ModelRegistry struct {
	state    atomic.Pointer[registryState]
	writerMu sync.Mutex
}

var getGlobalRegistry = sync.OnceValue(func() *ModelRegistry {
	r := &ModelRegistry{}
	r.state.Store(newRegistryState())
	return r
})

func GetGlobalRegistry() *ModelRegistry {
	return getGlobalRegistry()
}

func (r *ModelRegistry) snapshot() *registryState {
	return r.state.Load()
}

func (r *ModelRegistry) SetShowProviderPrefixes(enabled bool) {
	if r == nil {
		return
	}
	r.writerMu.Lock()
	defer r.writerMu.Unlock()

	newState := r.state.Load().clone()
	newState.showProviderPrefixes = enabled
	r.state.Store(newState)
}
