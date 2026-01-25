package executor

import "time"

const DefaultStreamBufferSize = 2 * 1024 * 1024 // 2MB - reduced from 20MB for better memory efficiency

const DefaultScannerBufferSize = 256 * 1024 // 256KB - increased from 64KB for better streaming performance

// DefaultStreamIdleTimeout is the maximum time to wait without receiving any data.
// This is a safety net for detecting stalled upstream connections.
// Set high enough to accommodate reasoning models that may "think" for extended periods.
// The watchdog checks periodically (every 30s max), so this won't affect normal operation.
const DefaultStreamIdleTimeout = 5 * time.Minute

// DefaultMaxResponseSize is the maximum response body size to read into memory.
// Used with io.LimitReader to prevent OOM from unexpectedly large responses.
const DefaultMaxResponseSize = 100 * 1024 * 1024 // 100MB

const (
	DefaultClaudeUserAgent      = "claude-cli/1.0.83 (external, cli)"
	DefaultCodexUserAgent       = "codex_cli_rs/1.104.1 (Mac OS 26.0.1; arm64) Apple_Terminal/464"
	DefaultAntigravityUserAgent = "antigravity/1.11.9 windows/amd64"
	DefaultQwenUserAgent        = "google-api-nodejs-client/9.15.1"
	DefaultIFlowUserAgent       = "iFlow-Cli"
	DefaultCopilotUserAgent     = "GithubCopilot/1.0"
)

const (
	ClaudeDefaultBaseURL        = "https://api.anthropic.com"
	CodexDefaultBaseURL         = "https://chatgpt.com/backend-api/codex"
	QwenDefaultBaseURL          = "https://portal.qwen.ai/v1"
	ClineDefaultBaseURL         = "https://api.cline.bot"
	GeminiDefaultBaseURL        = "https://generativelanguage.googleapis.com"
	AntigravityBaseURLDaily     = "https://daily-cloudcode-pa.googleapis.com"
	AntigravityBaseURLProd      = "https://cloudcode-pa.googleapis.com"
	GitHubCopilotDefaultBaseURL = "https://api.githubcopilot.com"
	GitHubCopilotChatPath       = "/chat/completions"
	GitHubCopilotAuthType       = "github-copilot"
	CopilotEditorVersion        = "vscode/1.104.1"
	CopilotPluginVersion        = "copilot/1.300.0"
	CopilotIntegrationID        = "vscode-chat"
	CopilotOpenAIIntent         = "conversation-panel"
	KiroDefaultBaseURL          = "https://codewhisperer.us-east-1.amazonaws.com/generateAssistantResponse"
	IFlowDefaultEndpoint        = "/chat/completions"
)

const (
	DefaultHTTPTimeout         = 60 * time.Second
	DefaultRefreshSkew         = 300 * time.Second
	KiroRefreshSkew            = 5 * time.Minute
	KiroRequestTimeout         = 120 * time.Second
	GitHubCopilotTokenCacheTTL = 25 * time.Minute
	TokenExpiryBuffer          = 5 * time.Minute
)

const (
	RateLimitBaseDelay        = 1 * time.Second
	RateLimitMaxDelay         = 5 * time.Second
	AntigravityRetryBaseDelay = 5 * time.Second
	AntigravityRetryMaxDelay  = 30 * time.Second
	MaxQuotaRetryDelay        = 30 * time.Second
)

const (
	GeminiGLAPIVersion      = "v1beta"
	QwenXGoogAPIClient      = "gl-node/22.17.0"
	QwenClientMetadataValue = "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
)
