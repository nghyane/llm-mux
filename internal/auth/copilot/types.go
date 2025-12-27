// Package copilot provides authentication for GitHub Copilot API.
package copilot

import (
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"os"
	"path/filepath"

	"github.com/nghyane/llm-mux/internal/misc"
)

// CopilotCredentials stores OAuth2 token information for GitHub Copilot.
type CopilotCredentials struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
	Username    string `json:"username"`
	Type        string `json:"type"`
}

// DeviceCodeResponse represents GitHub's device code response.
type DeviceCodeResponse struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// CopilotAPIToken represents the Copilot API token response.
type CopilotAPIToken struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Endpoints struct {
		API           string `json:"api"`
		Proxy         string `json:"proxy"`
		OriginTracker string `json:"origin-tracker"`
		Telemetry     string `json:"telemetry"`
	} `json:"endpoints,omitempty"`
}

// SaveToFile serializes credentials to a JSON file.
func (c *CopilotCredentials) SaveToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	c.Type = "github-copilot"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.OpenFile(authFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() { _ = f.Close() }()

	if err = json.NewEncoder(f).Encode(c); err != nil {
		return fmt.Errorf("failed to write token: %w", err)
	}
	return nil
}
