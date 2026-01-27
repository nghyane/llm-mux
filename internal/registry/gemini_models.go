// Package registry provides shared Gemini model metadata for all Google providers.
// Centralizes model definitions to avoid duplication across AI Studio, Gemini CLI, Vertex, Antigravity.
package registry

// =============================================================================
// Shared Gemini Model Definitions
// =============================================================================

// geminiModels defines metadata for Gemini-family models (used by all Google providers).
// Models not in this list will use upstream values directly (passthrough).
var geminiModels = []*ModelInfo{
	// Gemini 3.x
	Gemini("gemini-3-pro-high").Display("Gemini 3 Pro").
		Desc("Gemini 3 Pro").Version("3.0").Created(1731888000).Thinking(128, 32768).B(),
	Gemini("gemini-3-pro-low").Display("Gemini 3 Pro Low").
		Desc("Gemini 3 Pro Low").Version("3.0").Created(1731888000).B(),
	Gemini("gemini-3-flash").Display("Gemini 3 Flash").
		Desc("Gemini 3 Flash").Version("3.0").Created(1737158400).Thinking(128, 32768).B(),
	Gemini("gemini-3-flash-preview").Display("Gemini 3 Flash Preview").
		Desc("Gemini 3 Flash Preview").Version("3.0").Created(1737158400).Thinking(128, 32768).B(),
	Gemini("gemini-3-pro-image-preview").Display("Gemini 3 Pro Image Preview").
		Desc("Gemini 3 Pro Image Preview").Version("3.0").Created(1737158400).B(),

	// Gemini 2.5
	Gemini("gemini-2.5-pro").Display("Gemini 2.5 Pro").
		Desc("Stable release (June 17th, 2025) of Gemini 2.5 Pro").Version("2.5").Created(1750118400).Thinking(128, 32768).B(),
	Gemini("gemini-2.5-flash").Display("Gemini 2.5 Flash").
		Desc("Stable version of Gemini 2.5 Flash, our mid-size multimodal model that supports up to 1 million tokens, released in June of 2025.").
		Version("001").Created(1750118400).ThinkingFull(0, 24576, true, true).B(),
	Gemini("gemini-2.5-flash-lite").Display("Gemini 2.5 Flash Lite").
		Desc("Our smallest and most cost effective model, built for at scale usage.").
		Version("2.5").Created(1753142400).ThinkingFull(0, 24576, true, true).B(),
	Gemini("gemini-2.5-flash-image-preview").Display("Gemini 2.5 Flash Image Preview").
		Desc("State-of-the-art image generation and editing model.").Version("2.5").Created(1756166400).Limits(geminiInputLimit, 8192).B(),
	Gemini("gemini-2.5-flash-image").Display("Gemini 2.5 Flash Image").
		Desc("State-of-the-art image generation and editing model.").Version("2.5").Created(1759363200).Limits(geminiInputLimit, 8192).B(),
	Gemini("gemini-2.5-computer-use-preview-10-2025").Upstream("rev19-uic3-1p").Display("Gemini 2.5 Computer Use Preview").B(),
}

// claudeViaAntigravityModels defines Claude models accessed via Antigravity (gemini-cli only).
var claudeViaAntigravityModels = []*ModelInfo{
	ClaudeVia("claude-sonnet-4-5", "antigravity").Display("Claude Sonnet 4.5").
		Desc("Claude Sonnet 4.5 via google antigravity").Version("4.5").Created(1759104000).B(),
	ClaudeVia("claude-sonnet-4-5-thinking", "antigravity").Display("Claude Sonnet 4.5 Thinking").
		Desc("Claude Sonnet 4.5 with extended thinking via google antigravity").Version("4.5").Created(1759104000).Thinking(8192, 32768).B(),
	ClaudeVia("claude-opus-4-5-thinking", "antigravity").Display("Claude Opus 4.5 Thinking").
		Desc("Claude Opus 4.5 with extended thinking via google antigravity").Version("4.5").Created(1761955200).Thinking(8192, 32768).B(),
}

// =============================================================================
// Provider-Specific Hidden Models
// =============================================================================

var antigravityHiddenModels = []string{
	"chat_20706", "chat_23310", "gemini-2.5-flash-thinking", "gemini-2.5-pro",
}

// =============================================================================
// Lookup Maps (built at init)
// =============================================================================

var (
	geminiMetaByID       map[string]*ModelInfo
	geminiUpstreamToID   map[string]string
	geminiIDToUpstream   map[string]string
	antigravityHiddenSet map[string]bool
)

func init() {
	// Consolidate model slices for single-pass initialization
	allModels := append(append([]*ModelInfo{}, geminiModels...), claudeViaAntigravityModels...)

	n := len(allModels)
	geminiMetaByID = make(map[string]*ModelInfo, n)
	geminiUpstreamToID = make(map[string]string, n)
	geminiIDToUpstream = make(map[string]string, n)

	// Single pass through all models (Gemini + Claude via Antigravity)
	for _, m := range allModels {
		geminiMetaByID[m.ID] = m
		upstream := m.UpstreamName
		if upstream == "" {
			upstream = m.ID
		}
		geminiUpstreamToID[upstream] = m.ID
		geminiIDToUpstream[m.ID] = upstream
	}

	// Pre-allocate and populate hidden set
	antigravityHiddenSet = make(map[string]bool, len(antigravityHiddenModels))
	for _, name := range antigravityHiddenModels {
		antigravityHiddenSet[name] = true
	}
}

// =============================================================================
// Public API
// =============================================================================

// GetGeminiModelsForProvider returns Gemini models for the specified provider type.
// Valid provider types: "gemini", "vertex", "gemini-cli", "aistudio"
// Models are cloned with the Type field set to the provider type.
func GetGeminiModelsForProvider(providerType string) []*ModelInfo {
	var models []*ModelInfo

	// Clone base Gemini models with provider-specific type
	for _, m := range geminiModels {
		clone := cloneModelWithType(m, providerType)
		models = append(models, clone)
	}

	// Add Claude via Antigravity models only for gemini-cli
	if providerType == "gemini-cli" {
		for _, m := range claudeViaAntigravityModels {
			clone := cloneModelWithType(m, providerType)
			models = append(models, clone)
		}
	}

	return models
}

// GetAntigravityFallbackModels returns static fallback models for antigravity provider.
// Unlike GetGeminiModelsForProvider("gemini-cli"), this function:
// 1. Preserves "antigravity" as the provider Type
// 2. Applies antigravityHiddenSet filter to exclude hidden models
// This is used when dynamic fetch from Antigravity API fails.
func GetAntigravityFallbackModels() []*ModelInfo {
	var models []*ModelInfo

	// Clone geminiModels with antigravity type
	for _, m := range geminiModels {
		// Skip hidden models for antigravity
		if antigravityHiddenSet[m.ID] {
			continue
		}
		clone := cloneModelWithType(m, "antigravity")
		models = append(models, clone)
	}

	// Add Claude via Antigravity models (these are not hidden)
	for _, m := range claudeViaAntigravityModels {
		clone := cloneModelWithType(m, "antigravity")
		models = append(models, clone)
	}

	return models
}

// cloneModelWithType creates a deep copy of a ModelInfo with a new Type.
func cloneModelWithType(src *ModelInfo, providerType string) *ModelInfo {
	clone := &ModelInfo{
		ID:                         src.ID,
		Object:                     src.Object,
		Created:                    src.Created,
		OwnedBy:                    src.OwnedBy,
		Type:                       providerType,
		CanonicalID:                src.CanonicalID,
		Name:                       src.Name,
		Version:                    src.Version,
		DisplayName:                src.DisplayName,
		Description:                src.Description,
		InputTokenLimit:            src.InputTokenLimit,
		OutputTokenLimit:           src.OutputTokenLimit,
		ContextLength:              src.ContextLength,
		MaxCompletionTokens:        src.MaxCompletionTokens,
		SupportedGenerationMethods: src.SupportedGenerationMethods,
		SupportedParameters:        src.SupportedParameters,
		UpstreamName:               src.UpstreamName,
		Hidden:                     src.Hidden,
		Priority:                   src.Priority,
	}
	if src.Thinking != nil {
		clone.Thinking = &ThinkingSupport{
			Min:            src.Thinking.Min,
			Max:            src.Thinking.Max,
			ZeroAllowed:    src.Thinking.ZeroAllowed,
			DynamicAllowed: src.Thinking.DynamicAllowed,
		}
	}
	return clone
}

// GeminiUpstreamToID converts upstream name to user-facing ID.
func GeminiUpstreamToID(upstreamName string, hiddenSet map[string]bool) string {
	if hiddenSet != nil && hiddenSet[upstreamName] {
		return ""
	}
	if id, ok := geminiUpstreamToID[upstreamName]; ok {
		return id
	}
	return upstreamName
}

// GeminiIDToUpstream converts user-facing ID to upstream name.
func GeminiIDToUpstream(modelID string) string {
	if upstream, ok := geminiIDToUpstream[modelID]; ok {
		return upstream
	}
	return modelID
}

// ApplyGeminiMeta applies shared metadata to a ModelInfo.
func ApplyGeminiMeta(info *ModelInfo) bool {
	meta := geminiMetaByID[info.ID]
	if meta == nil {
		return false
	}
	if meta.DisplayName != "" {
		info.DisplayName = meta.DisplayName
	}
	if meta.Description != "" {
		info.Description = meta.Description
	}
	if meta.Thinking != nil {
		info.Thinking = meta.Thinking
	}
	if meta.InputTokenLimit > 0 {
		info.InputTokenLimit = meta.InputTokenLimit
	}
	if meta.OutputTokenLimit > 0 {
		info.OutputTokenLimit = meta.OutputTokenLimit
	}
	if meta.UpstreamName != "" {
		info.UpstreamName = meta.UpstreamName
	}
	return true
}

// =============================================================================
// Antigravity Functions
// =============================================================================

// AntigravityUpstreamToID converts upstream name to ID for Antigravity provider.
func AntigravityUpstreamToID(upstreamName string) string {
	return GeminiUpstreamToID(upstreamName, antigravityHiddenSet)
}

// AntigravityIDToUpstream converts ID to upstream name for Antigravity provider.
func AntigravityIDToUpstream(modelID string) string {
	return GeminiIDToUpstream(modelID)
}
