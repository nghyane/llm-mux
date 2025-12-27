// Package executor provides runtime execution capabilities for various AI service providers.
// This file contains shared constants used across all executors.
package executor

import "time"

// DefaultStreamBufferSize is 20MB - maximum buffer for SSE stream scanning.
// This size accommodates large tool call responses from LLM providers.
const DefaultStreamBufferSize = 20 * 1024 * 1024

// DefaultScannerBufferSize is 64KB - initial buffer size for scanner buffer pool.
// Each streaming request reuses a 64KB buffer instead of allocating new ones.
const DefaultScannerBufferSize = 64 * 1024

const (
	// DefaultClaudeUserAgent is the User-Agent header for Claude CLI requests.
	DefaultClaudeUserAgent = "claude-cli/1.0.83 (external, cli)"

	// DefaultCodexUserAgent is the User-Agent header for OpenAI Codex CLI requests.
	DefaultCodexUserAgent = "codex_cli_rs/1.104.1 (Mac OS 26.0.1; arm64) Apple_Terminal/464"

	// DefaultAntigravityUserAgent is the User-Agent header for Antigravity (Gemini CLI) requests.
	DefaultAntigravityUserAgent = "antigravity/1.11.5 windows/amd64"

	// DefaultQwenUserAgent is the User-Agent header for Qwen requests.
	DefaultQwenUserAgent = "google-api-nodejs-client/9.15.1"

	// DefaultIFlowUserAgent is the User-Agent header for iFlow requests.
	DefaultIFlowUserAgent = "iFlow-Cli"

	// DefaultCopilotUserAgent is the User-Agent header for GitHub Copilot requests.
	DefaultCopilotUserAgent = "GithubCopilot/1.0"
)

const (
	// ClaudeDefaultBaseURL is the default API endpoint for Anthropic Claude.
	ClaudeDefaultBaseURL = "https://api.anthropic.com"

	// CodexDefaultBaseURL is the default API endpoint for OpenAI Codex.
	CodexDefaultBaseURL = "https://chatgpt.com/backend-api/codex"

	// QwenDefaultBaseURL is the default API endpoint for Qwen.
	QwenDefaultBaseURL = "https://portal.qwen.ai/v1"

	// ClineDefaultBaseURL is the default API endpoint for Cline.
	ClineDefaultBaseURL = "https://api.cline.bot"

	// GeminiDefaultBaseURL is the default API endpoint for Google Gemini.
	GeminiDefaultBaseURL = "https://generativelanguage.googleapis.com"

	// AntigravityBaseURLDaily is the daily/sandbox endpoint for Antigravity.
	AntigravityBaseURLDaily = "https://daily-cloudcode-pa.sandbox.googleapis.com"

	// AntigravityBaseURLProd is the production endpoint for Antigravity.
	AntigravityBaseURLProd = "https://cloudcode-pa.googleapis.com"

	// GitHubCopilotDefaultBaseURL is the default API endpoint for GitHub Copilot.
	GitHubCopilotDefaultBaseURL = "https://api.githubcopilot.com"

	// GitHubCopilotChatPath is the chat completions endpoint path for GitHub Copilot.
	GitHubCopilotChatPath = "/chat/completions"

	// GitHubCopilotAuthType is the authentication type identifier for GitHub Copilot.
	GitHubCopilotAuthType = "github-copilot"

	// CopilotEditorVersion is the editor version header for GitHub Copilot requests.
	CopilotEditorVersion = "vscode/1.104.1"

	// CopilotPluginVersion is the plugin version header for GitHub Copilot requests.
	CopilotPluginVersion = "copilot/1.300.0"

	// CopilotIntegrationID is the integration ID header for GitHub Copilot requests.
	CopilotIntegrationID = "vscode-chat"

	// CopilotOpenAIIntent is the OpenAI intent header for GitHub Copilot requests.
	CopilotOpenAIIntent = "conversation-panel"

	// KiroDefaultBaseURL is the default API endpoint for Kiro (Amazon Q).
	KiroDefaultBaseURL = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"

	// IFlowDefaultEndpoint is the default endpoint path for iFlow API requests.
	IFlowDefaultEndpoint = "/chat/completions"
)

const (
	// DefaultHTTPTimeout is the default timeout for HTTP requests.
	// Used as fallback when no specific timeout is configured.
	DefaultHTTPTimeout = 60 * time.Second

	// DefaultRefreshSkew is the time buffer before token expiry to trigger refresh.
	// Tokens are refreshed this many seconds before they actually expire.
	DefaultRefreshSkew = 3000 * time.Second

	// KiroRefreshSkew is the time buffer before token expiry for Kiro provider.
	KiroRefreshSkew = 5 * time.Minute

	// KiroRequestTimeout is the timeout for Kiro API requests.
	KiroRequestTimeout = 120 * time.Second

	// GitHubCopilotTokenCacheTTL is the cache duration for GitHub Copilot tokens.
	GitHubCopilotTokenCacheTTL = 25 * time.Minute

	// TokenExpiryBuffer is the buffer time before token expiry for cache invalidation.
	TokenExpiryBuffer = 5 * time.Minute
)

const (
	// RateLimitBaseDelay is the initial delay for rate limit retries.
	// Exponential backoff: 1s, 2s, 4s, 8s, 16s = ~31s total.
	RateLimitBaseDelay = 1 * time.Second

	// RateLimitMaxDelay is the maximum delay between retry attempts.
	RateLimitMaxDelay = 20 * time.Second

	// AntigravityRetryBaseDelay is the base delay for Antigravity retries.
	AntigravityRetryBaseDelay = 2 * time.Second

	// AntigravityRetryMaxDelay is the maximum delay for Antigravity retries.
	AntigravityRetryMaxDelay = 30 * time.Second
)

const (
	// Provider-specific metadata constants

	// GeminiGLAPIVersion is the API version used for Gemini requests.
	GeminiGLAPIVersion = "v1beta"

	// QwenXGoogAPIClient is the X-Goog-Api-Client header value for Qwen requests.
	QwenXGoogAPIClient = "gl-node/22.17.0"

	// QwenClientMetadataValue is the Client-Metadata header value for Qwen requests.
	QwenClientMetadataValue = "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
)
