package executor

import (
	"testing"

	cliproxyauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
)

func TestExtractCreds_NilAuth(t *testing.T) {
	token, url := ExtractCreds(nil, ClaudeCredsConfig)
	if token != "" || url != "" {
		t.Errorf("Expected empty strings for nil auth, got token=%q, url=%q", token, url)
	}
}

func TestExtractCreds_Claude(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "attr-key",
			"base_url": "attr-url",
		},
	}

	token, url := ExtractCreds(auth, ClaudeCredsConfig)
	if token != "attr-key" || url != "attr-url" {
		t.Errorf("Expected token=%q, url=%q, got token=%q, url=%q", "attr-key", "attr-url", token, url)
	}
}

func TestExtractCreds_Claude_MetadataFallback(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"base_url": "attr-url",
		},
		Metadata: map[string]any{
			"access_token": "meta-token",
		},
	}

	token, url := ExtractCreds(auth, ClaudeCredsConfig)
	if token != "meta-token" || url != "attr-url" {
		t.Errorf("Expected token=%q, url=%q, got token=%q, url=%q", "meta-token", "attr-url", token, url)
	}
}

func TestExtractCreds_Cline_TokenPrefix(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "cline-token",
		},
	}

	token, url := ExtractCreds(auth, ClineCredsConfig)
	if token != "workos:cline-token" || url != "" {
		t.Errorf("Expected token=%q, url=%q, got token=%q, url=%q", "workos:cline-token", "", token, url)
	}
}

func TestExtractCreds_Qwen_URLTransform(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "qwen-token",
			"resource_url": "api.qwen.com",
		},
	}

	token, url := ExtractCreds(auth, QwenCredsConfig)
	expectedURL := "https://api.qwen.com/v1"
	if token != "qwen-token" || url != expectedURL {
		t.Errorf("Expected token=%q, url=%q, got token=%q, url=%q", "qwen-token", expectedURL, token, url)
	}
}

func TestExtractCreds_IFlow_TrimWhitespace(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":  "  key-with-spaces  ",
			"base_url": "  url-with-spaces  ",
		},
		Metadata: map[string]any{
			"api_key":  "  meta-key  ",
			"base_url": "  meta-url  ",
		},
	}

	token, url := ExtractCreds(auth, IFlowCredsConfig)
	if token != "key-with-spaces" || url != "url-with-spaces" {
		t.Errorf("Expected trimmed token=%q, url=%q, got token=%q, url=%q", "key-with-spaces", "url-with-spaces", token, url)
	}
}

func TestExtractCreds_Gemini_NestedTokenMap(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"token": map[string]any{
				"access_token": "nested-token",
			},
		},
	}

	token, url := ExtractCreds(auth, GeminiCredsConfig)
	if token != "nested-token" || url != "" {
		t.Errorf("Expected token=%q, url=%q, got token=%q, url=%q", "nested-token", "", token, url)
	}
}

func TestExtractCreds_Gemini_Priority(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Metadata: map[string]any{
			"access_token": "direct-token",
			"token": map[string]any{
				"access_token": "nested-token",
			},
		},
	}

	token, url := ExtractCreds(auth, GeminiCredsConfig)
	// Direct access_token should take priority over nested
	if token != "direct-token" || url != "" {
		t.Errorf("Expected direct token priority, got token=%q", token)
	}
}
