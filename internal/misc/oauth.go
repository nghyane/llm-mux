package misc

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
)

// GenerateRandomState generates a cryptographically secure random state parameter
// for OAuth2 flows to prevent CSRF attacks.
// Returns:
//   - string: A hexadecimal encoded random state string
//   - error: An error if the random generation fails, nil otherwise
func GenerateRandomState() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
