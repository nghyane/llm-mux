// Package executor provides common utilities for executor implementations.
package executor

import (
	"strings"

	"github.com/nghyane/llm-mux/sdk/cliproxy/auth"
)

// CredExtractorConfig configures credential extraction behavior.
// This allows different providers to customize how credentials are extracted from auth objects.
type CredExtractorConfig struct {
	// MetadataTokenKey specifies the key in auth.Metadata to extract token from.
	// Common values: "access_token", "api_key"
	MetadataTokenKey string

	// MetadataURLKey specifies the key in auth.Metadata to extract base URL from.
	// Common values: "base_url", "resource_url" (optional)
	MetadataURLKey string

	// TokenPrefix is prepended to extracted tokens (e.g., "workos:" for Cline).
	TokenPrefix string

	// URLTransformFunc transforms the extracted URL if needed.
	// Example: resource_url -> "https://<url>/v1" for Qwen
	URLTransformFunc func(string) string

	// CheckNestedTokenMap enables checking auth.Metadata["token"].(map[string]any)["access_token"]
	// Used by Gemini which stores tokens in a nested map structure.
	CheckNestedTokenMap bool

	// TrimWhitespace enables strings.TrimSpace() on extracted values.
	// Used by iFlow provider.
	TrimWhitespace bool
}

// ExtractCreds extracts credentials from an auth object using the provided configuration.
// Returns (token, url) where token may include prefix and url may be transformed.
//
// This centralizes credential extraction logic across all executor implementations,
// eliminating duplicate extraction patterns while allowing provider-specific customization.
func ExtractCreds(a *auth.Auth, cfg CredExtractorConfig) (token, url string) {
	if a == nil {
		return "", ""
	}

	trim := func(s string) string {
		if cfg.TrimWhitespace {
			return strings.TrimSpace(s)
		}
		return s
	}

	// 1. Extract from Attributes first (highest priority)
	if a.Attributes != nil {
		token = trim(a.Attributes["api_key"])
		url = trim(a.Attributes["base_url"])
	}

	// 2. Fallback to Metadata for token
	if token == "" && a.Metadata != nil {
		// Check direct metadata key first (higher priority)
		if v, ok := a.Metadata[cfg.MetadataTokenKey].(string); ok {
			token = trim(v)
		}

		// Fallback to nested token map if configured and direct key didn't provide token
		if token == "" && cfg.CheckNestedTokenMap {
			if tokenMap, ok := a.Metadata["token"].(map[string]any); ok {
				if v, ok := tokenMap["access_token"].(string); ok && v != "" {
					token = trim(v)
				}
			}
		}
	}

	// 3. Fallback to Metadata for URL
	if url == "" && a.Metadata != nil && cfg.MetadataURLKey != "" {
		if v, ok := a.Metadata[cfg.MetadataURLKey].(string); ok {
			url = trim(v)
			// Apply URL transformation if configured
			if cfg.URLTransformFunc != nil {
				url = cfg.URLTransformFunc(url)
			}
		}
	}

	// 4. Apply token prefix if configured
	if token != "" && cfg.TokenPrefix != "" {
		token = cfg.TokenPrefix + token
	}

	return token, url
}

// Predefined configurations for each provider.
// These encapsulate the specific credential extraction logic for each provider.

var (
	// ClaudeCredsConfig extracts credentials for Claude API.
	// Uses standard access_token from metadata.
	ClaudeCredsConfig = CredExtractorConfig{
		MetadataTokenKey: "access_token",
	}

	// CodexCredsConfig extracts credentials for Codex API.
	// Uses standard access_token from metadata.
	CodexCredsConfig = CredExtractorConfig{
		MetadataTokenKey: "access_token",
	}

	// ClineCredsConfig extracts credentials for Cline API.
	// Uses access_token from metadata and adds "workos:" prefix.
	ClineCredsConfig = CredExtractorConfig{
		MetadataTokenKey: "access_token",
		TokenPrefix:      "workos:",
	}

	// QwenCredsConfig extracts credentials for Qwen API.
	// Uses access_token from metadata and transforms resource_url to full API URL.
	QwenCredsConfig = CredExtractorConfig{
		MetadataTokenKey: "access_token",
		MetadataURLKey:   "resource_url",
		URLTransformFunc: func(url string) string {
			return "https://" + url + "/v1"
		},
	}

	// IFlowCredsConfig extracts credentials for iFlow API.
	// Uses api_key from metadata and enables whitespace trimming.
	IFlowCredsConfig = CredExtractorConfig{
		MetadataTokenKey: "api_key",
		MetadataURLKey:   "base_url",
		TrimWhitespace:   true,
	}

	// GeminiCredsConfig extracts credentials for Gemini API.
	// Uses access_token from metadata and checks nested token map structure.
	GeminiCredsConfig = CredExtractorConfig{
		MetadataTokenKey:    "access_token",
		CheckNestedTokenMap: true,
	}
)
