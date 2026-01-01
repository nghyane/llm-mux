package claude

import "github.com/nghyane/llm-mux/internal/oauth/pkce"

type PKCECodes = pkce.Codes

func GeneratePKCECodes() (*PKCECodes, error) {
	return pkce.Generate()
}

type ClaudeTokenData struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Email        string `json:"email"`
	Expire       string `json:"expired"`
}

type ClaudeAuthBundle struct {
	APIKey      string          `json:"api_key"`
	TokenData   ClaudeTokenData `json:"token_data"`
	LastRefresh string          `json:"last_refresh"`
}
