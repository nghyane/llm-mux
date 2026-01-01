// Package pkce provides PKCE (Proof Key for Code Exchange) utilities
// for OAuth 2.0 authorization code flows as specified in RFC 7636.
package pkce

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
)

// Codes holds the verification codes for the OAuth2 PKCE flow.
// PKCE is an extension to the Authorization Code flow to prevent
// CSRF and authorization code injection attacks.
type Codes struct {
	// CodeVerifier is the cryptographically random string used to correlate
	// the authorization request to the token request.
	CodeVerifier string `json:"code_verifier"`
	// CodeChallenge is the SHA256 hash of the code verifier, base64url-encoded.
	CodeChallenge string `json:"code_challenge"`
}

// Generate creates a new pair of PKCE codes.
// It generates a cryptographically random code verifier and its corresponding
// SHA256 code challenge, as specified in RFC 7636.
func Generate() (*Codes, error) {
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		return nil, fmt.Errorf("failed to generate code verifier: %w", err)
	}

	codeChallenge := generateCodeChallenge(codeVerifier)

	return &Codes{
		CodeVerifier:  codeVerifier,
		CodeChallenge: codeChallenge,
	}, nil
}

// generateCodeVerifier creates a cryptographically secure random string
// of 128 characters using URL-safe base64 encoding.
func generateCodeVerifier() (string, error) {
	// Generate 96 random bytes (will result in 128 base64 characters)
	bytes := make([]byte, 96)
	_, err := rand.Read(bytes)
	if err != nil {
		return "", fmt.Errorf("failed to generate random bytes: %w", err)
	}

	// Encode to URL-safe base64 without padding
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(bytes), nil
}

// generateCodeChallenge creates a SHA256 hash of the code verifier
// and encodes it using URL-safe base64 encoding without padding.
func generateCodeChallenge(codeVerifier string) string {
	hash := sha256.Sum256([]byte(codeVerifier))
	return base64.URLEncoding.WithPadding(base64.NoPadding).EncodeToString(hash[:])
}
