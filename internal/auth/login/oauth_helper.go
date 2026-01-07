package login

import (
	"context"
	"fmt"

	"github.com/nghyane/llm-mux/internal/browser"
	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/misc"
	"github.com/nghyane/llm-mux/internal/util"
)

// OAuthFlowParams holds parameters for executing a standard OAuth flow.
// This struct encapsulates the common inputs needed across different providers.
type OAuthFlowParams struct {
	// ProviderName is the display name for user-facing messages (e.g., "Claude", "Codex").
	ProviderName string

	// CallbackPort is the local port for the OAuth callback server.
	// If zero, SSH tunnel instructions are skipped.
	CallbackPort int

	// AuthURL is the authorization URL to open in the browser.
	AuthURL string

	// NoBrowser indicates whether to skip automatic browser opening.
	NoBrowser bool
}

// ValidateLoginInputs performs standard validation of Login method inputs.
// Returns normalized context and options, or an error if validation fails.
func ValidateLoginInputs(ctx context.Context, cfg *config.Config, opts *LoginOptions) (context.Context, *LoginOptions, error) {
	if cfg == nil {
		return nil, nil, fmt.Errorf("cliproxy auth: configuration is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if opts == nil {
		opts = &LoginOptions{}
	}
	return ctx, opts, nil
}

// GenerateOAuthState creates a cryptographically secure random state parameter
// for OAuth CSRF protection. This is a convenience wrapper around misc.GenerateRandomState
// with provider-specific error formatting.
func GenerateOAuthState(providerName string) (string, error) {
	state, err := misc.GenerateRandomState()
	if err != nil {
		return "", fmt.Errorf("%s state generation failed: %w", providerName, err)
	}
	return state, nil
}

// OpenBrowserForAuth handles the browser opening logic for OAuth flows.
// It handles three scenarios:
//   - NoBrowser=true: prints URL for manual opening
//   - Browser unavailable: prints URL with warning
//   - Browser available: attempts to open automatically, falls back to manual on error
//
// The callbackPort is used for SSH tunnel instructions when browser cannot be opened.
// If callbackPort is 0, SSH tunnel instructions are skipped.
func OpenBrowserForAuth(params OAuthFlowParams) {
	if !params.NoBrowser {
		fmt.Printf("Opening browser for %s authentication\n", params.ProviderName)
		if !browser.IsAvailable() {
			log.Warn("No browser available; please open the URL manually")
			if params.CallbackPort > 0 {
				util.PrintSSHTunnelInstructions(params.CallbackPort)
			}
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", params.AuthURL)
		} else if err := browser.OpenURL(params.AuthURL); err != nil {
			log.Warnf("Failed to open browser automatically: %v", err)
			if params.CallbackPort > 0 {
				util.PrintSSHTunnelInstructions(params.CallbackPort)
			}
			fmt.Printf("Visit the following URL to continue authentication:\n%s\n", params.AuthURL)
		}
	} else {
		if params.CallbackPort > 0 {
			util.PrintSSHTunnelInstructions(params.CallbackPort)
		}
		fmt.Printf("Visit the following URL to continue authentication:\n%s\n", params.AuthURL)
	}
}

// PrintWaitingMessage prints a standardized "Waiting for callback" message.
func PrintWaitingMessage(providerName string) {
	fmt.Printf("Waiting for %s authentication callback...\n", providerName)
}

// PrintAuthSuccess prints a standardized success message after authentication.
func PrintAuthSuccess(providerName string) {
	fmt.Printf("%s authentication successful\n", providerName)
}
