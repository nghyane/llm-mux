/**
 * @file Token storage implementation for Cline authentication
 * @description Provides persistent storage for Cline OAuth tokens including access tokens,
 * refresh tokens, and user account information. Implements the TokenStorage interface
 * for seamless integration with the authentication system. Tokens are stored in JSON format
 * with proper directory structure and file permissions for security.
 */

package cline

import (
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"os"
	"path/filepath"

	"github.com/nghyane/llm-mux/internal/misc"
)

// ClineTokenStorage stores OAuth2 token information for Cline API authentication.
type ClineTokenStorage struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	LastRefresh  string `json:"last_refresh"`
	Email        string `json:"email"`
	Type         string `json:"type"`
	Expire       string `json:"expired"`
}

// SaveTokenToFile serializes the Cline token storage to a JSON file.
// This method creates the necessary directory structure and writes the token
// data in JSON format to the specified file path for persistent storage.
// Parameters:
//   - authFilePath: The full path where the token file should be saved
//
// Returns:
//   - error: An error if the operation fails, nil otherwise
func (ts *ClineTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	ts.Type = "cline"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %v", err)
	}

	f, err := os.OpenFile(authFilePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()

	if err = json.NewEncoder(f).Encode(ts); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}
