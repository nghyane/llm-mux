// Package util provides utility functions for provider detection and header masking.
package util

import (
	"net/url"
	"strings"

	"github.com/nghyane/llm-mux/internal/registry"
	log "github.com/sirupsen/logrus"
)

// GetProviderName determines all AI service providers capable of serving a registered model.
// It queries the model registry and returns providers ordered by preference.
func GetProviderName(modelName string) []string {
	if modelName == "" {
		log.Debugf("GetProviderName: empty modelName")
		return nil
	}

	// Normalize model ID using centralized normalizer
	normalizer := registry.NewModelIDNormalizer()
	cleanModelName := normalizer.NormalizeModelID(modelName)
	log.Debugf("GetProviderName: modelName=%s, cleanModelName=%s", modelName, cleanModelName)

	modelProviders := registry.GetGlobalRegistry().GetModelProviders(cleanModelName)
	log.Debugf("GetProviderName: modelProviders=%v", modelProviders)

	return modelProviders
}

// NormalizeIncomingModelID normalizes model IDs from client requests.
// Examples: "[Gemini CLI] model" -> "model", "[Antigravity] model" -> "model"
func NormalizeIncomingModelID(modelID string) string {
	normalizer := registry.NewModelIDNormalizer()
	return normalizer.NormalizeModelID(modelID)
}

// ExtractProviderFromPrefixedModelID extracts the provider type from a prefixed model ID.
// Examples: "[Gemini CLI] model" -> "gemini-cli", "model" -> ""
func ExtractProviderFromPrefixedModelID(modelID string) string {
	normalizer := registry.NewModelIDNormalizer()
	return normalizer.ExtractProviderFromPrefixedID(modelID)
}

// ResolveAutoModel resolves the "auto" model name to an actual available model.
func ResolveAutoModel(modelName string) string {
	if modelName != "auto" {
		return modelName
	}

	// Use empty string as handler type to get any available model
	firstModel, err := registry.GetGlobalRegistry().GetFirstAvailableModel("")
	if err != nil {
		log.Warnf("Failed to resolve 'auto' model: %v, falling back to original model name", err)
		return modelName
	}

	log.Infof("Resolved 'auto' model to: %s", firstModel)
	return firstModel
}


// HideAPIKey obscures an API key for logging, showing only first and last few characters.
func HideAPIKey(apiKey string) string {
	if len(apiKey) > 8 {
		return apiKey[:4] + "..." + apiKey[len(apiKey)-4:]
	} else if len(apiKey) > 4 {
		return apiKey[:2] + "..." + apiKey[len(apiKey)-2:]
	} else if len(apiKey) > 2 {
		return apiKey[:1] + "..." + apiKey[len(apiKey)-1:]
	}
	return apiKey
}

// MaskAuthorizationHeader masks the Authorization header value while preserving the auth type prefix.
func MaskAuthorizationHeader(value string) string {
	parts := strings.SplitN(strings.TrimSpace(value), " ", 2)
	if len(parts) < 2 {
		return HideAPIKey(value)
	}
	return parts[0] + " " + HideAPIKey(parts[1])
}

// MaskSensitiveHeaderValue masks sensitive header values while preserving expected formats.
// Handles Authorization headers (preserves auth type prefix) and API key headers.
func MaskSensitiveHeaderValue(key, value string) string {
	lowerKey := strings.ToLower(strings.TrimSpace(key))
	switch {
	case strings.Contains(lowerKey, "authorization"):
		return MaskAuthorizationHeader(value)
	case strings.Contains(lowerKey, "api-key"),
		strings.Contains(lowerKey, "apikey"),
		strings.Contains(lowerKey, "token"),
		strings.Contains(lowerKey, "secret"):
		return HideAPIKey(value)
	default:
		return value
	}
}

// MaskSensitiveQuery masks sensitive query parameters, e.g. auth_token, within the raw query string.
func MaskSensitiveQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	changed := false
	for i, part := range parts {
		if part == "" {
			continue
		}
		keyPart := part
		valuePart := ""
		if idx := strings.Index(part, "="); idx >= 0 {
			keyPart = part[:idx]
			valuePart = part[idx+1:]
		}
		decodedKey, err := url.QueryUnescape(keyPart)
		if err != nil {
			decodedKey = keyPart
		}
		if !shouldMaskQueryParam(decodedKey) {
			continue
		}
		decodedValue, err := url.QueryUnescape(valuePart)
		if err != nil {
			decodedValue = valuePart
		}
		masked := HideAPIKey(strings.TrimSpace(decodedValue))
		parts[i] = keyPart + "=" + url.QueryEscape(masked)
		changed = true
	}
	if !changed {
		return raw
	}
	return strings.Join(parts, "&")
}

func shouldMaskQueryParam(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return false
	}
	key = strings.TrimSuffix(key, "[]")
	if key == "key" || strings.Contains(key, "api-key") || strings.Contains(key, "apikey") || strings.Contains(key, "api_key") {
		return true
	}
	if strings.Contains(key, "token") || strings.Contains(key, "secret") {
		return true
	}
	return false
}
