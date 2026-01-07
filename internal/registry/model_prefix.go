package registry

import (
	"strings"
)

type ModelIDNormalizer struct{}

func NewModelIDNormalizer() *ModelIDNormalizer {
	return &ModelIDNormalizer{}
}

func (n *ModelIDNormalizer) NormalizeModelID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if strings.HasPrefix(modelID, "[") {
		if idx := strings.Index(modelID, "] "); idx != -1 {
			return strings.TrimSpace(modelID[idx+2:])
		}
	}
	return modelID
}

func (n *ModelIDNormalizer) ExtractProviderFromPrefixedID(modelID string) string {
	modelID = strings.TrimSpace(modelID)
	if strings.HasPrefix(modelID, "[") {
		if idx := strings.Index(modelID, "] "); idx != -1 {
			prefix := strings.TrimSpace(modelID[1:idx])
			providerMap := map[string]string{
				"Gemini CLI":  "gemini-cli",
				"Gemini":      "gemini",
				"Vertex AI":   "vertex",
				"AI Studio":   "aistudio",
				"Antigravity": "antigravity",
				"Claude":      "claude",
				"Codex":       "codex",
				"Qwen":        "qwen",
				"iFlow":       "iflow",
				"Cline":       "cline",
				"Kiro":        "kiro",
				"OpenAI":      "openai",
				"Anthropic":   "anthropic",
				"Google":      "google",
			}
			if provider, exists := providerMap[prefix]; exists {
				return provider
			}
		}
	}
	return ""
}

func formatProviderPrefix(showPrefixes bool, modelType string) string {
	if !showPrefixes || modelType == "" {
		return ""
	}

	providerNames := map[string]string{
		"gemini-cli":  "Gemini CLI",
		"gemini":      "Gemini",
		"vertex":      "Vertex AI",
		"aistudio":    "AI Studio",
		"claude":      "Claude",
		"codex":       "Codex",
		"qwen":        "Qwen",
		"iflow":       "iFlow",
		"cline":       "Cline",
		"kiro":        "Kiro",
		"antigravity": "Antigravity",
		"openai":      "OpenAI",
		"anthropic":   "Anthropic",
		"google":      "Google",
	}

	typeLower := strings.ToLower(strings.TrimSpace(modelType))
	if displayName, exists := providerNames[typeLower]; exists {
		return "[" + displayName + "] "
	}

	if len(typeLower) > 0 {
		return "[" + strings.ToUpper(typeLower[:1]) + typeLower[1:] + "] "
	}

	return ""
}

func (r *ModelRegistry) convertModelToMap(model *ModelInfo, handlerType string) map[string]any {
	s := r.snapshot()
	return r.convertModelToMapWithState(s, model, handlerType)
}

func (r *ModelRegistry) convertModelToMapWithState(s *registryState, model *ModelInfo, handlerType string) map[string]any {
	if model == nil {
		return nil
	}

	prefix := formatProviderPrefix(s.showProviderPrefixes, model.Type)

	switch handlerType {
	case "openai":
		result := map[string]any{
			"id":       prefix + model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created"] = model.Created
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		}
		if model.Version != "" {
			result["version"] = model.Version
		}
		if model.Description != "" {
			result["description"] = model.Description
		}
		if model.ContextLength > 0 {
			result["context_length"] = model.ContextLength
		}
		if model.MaxCompletionTokens > 0 {
			result["max_completion_tokens"] = model.MaxCompletionTokens
		}
		if len(model.SupportedParameters) > 0 {
			result["supported_parameters"] = model.SupportedParameters
		}
		return result

	case "claude":
		result := map[string]any{
			"id":       prefix + model.ID,
			"object":   "model",
			"owned_by": model.OwnedBy,
		}
		if model.Created > 0 {
			result["created"] = model.Created
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.DisplayName != "" {
			result["display_name"] = model.DisplayName
		}
		return result

	case "gemini":
		result := map[string]any{}
		name := model.ID
		if !strings.HasPrefix(name, "models/") {
			name = "models/" + name
		}
		result["name"] = prefix + name
		if model.Version != "" {
			result["version"] = model.Version
		}
		if model.DisplayName != "" {
			result["displayName"] = model.DisplayName
		}
		if model.Description != "" {
			result["description"] = model.Description
		}
		if model.InputTokenLimit > 0 {
			result["inputTokenLimit"] = model.InputTokenLimit
		}
		if model.OutputTokenLimit > 0 {
			result["outputTokenLimit"] = model.OutputTokenLimit
		}
		if len(model.SupportedGenerationMethods) > 0 {
			result["supportedGenerationMethods"] = model.SupportedGenerationMethods
		}
		return result

	default:
		result := map[string]any{
			"id":     prefix + model.ID,
			"object": "model",
		}
		if model.OwnedBy != "" {
			result["owned_by"] = model.OwnedBy
		}
		if model.Type != "" {
			result["type"] = model.Type
		}
		if model.Created != 0 {
			result["created"] = model.Created
		}
		return result
	}
}
