package codex

import "github.com/nghyane/llm-mux/internal/oauth/pkce"

type PKCECodes = pkce.Codes

func GeneratePKCECodes() (*PKCECodes, error) {
	return pkce.Generate()
}

type CodexTokenData struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
	Email        string `json:"email"`
	Expire       string `json:"expired"`
}

type CodexAuthBundle struct {
	APIKey      string         `json:"api_key"`
	TokenData   CodexTokenData `json:"token_data"`
	LastRefresh string         `json:"last_refresh"`
}
