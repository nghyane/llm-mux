package oauth

// credentials.go consolidates PUBLIC OAuth client credentials for installed applications.
// IMPORTANT: These are NOT sensitive secrets. They are PUBLIC client IDs and secrets
// intentionally distributed with the application for installed/CLI use.
// OAuth 2.0 installed applications use public (non-confidential) clients where the
// client secret cannot be kept secure (e.g., in compiled binaries, CLI tools, mobile apps).
// These credentials are visible in the binary and are meant for:
// - Google Cloud Code / Antigravity API (Cloud Assist endpoint)
// - Google Gemini CLI (Legacy endpoint)
// Do not move these to user configuration files. They are intentionally embedded.

const (
	// GeminiClientID is the PUBLIC OAuth client ID for Google Gemini CLI.
	// Used for Cloud Code Assist endpoint authentication.
	GeminiClientID = "681255809395-oo8ft2oprdrnp9e3aqf6av3hmdib135j.apps.googleusercontent.com"

	// GeminiClientSecret is the PUBLIC OAuth client secret for Google Gemini CLI.
	// This is not a sensitive value - it's part of the installed app OAuth flow.
	GeminiClientSecret = "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"

	// AntigravityClientID is the PUBLIC OAuth client ID for Google Antigravity / Cloud Code API.
	// Used for Antigravity upstream communication.
	AntigravityClientID = "1071006060591-tmhssin2h21lcre235vtolojh4g403ep.apps.googleusercontent.com"

	// AntigravityClientSecret is the PUBLIC OAuth client secret for Google Antigravity.
	// This is not a sensitive value - it's part of the installed app OAuth flow.
	AntigravityClientSecret = "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"
)
