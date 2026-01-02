package management

import (
	"context"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nghyane/llm-mux/internal/auth"
	"github.com/nghyane/llm-mux/internal/auth/claude"
	"github.com/nghyane/llm-mux/internal/auth/codex"
	"github.com/nghyane/llm-mux/internal/auth/copilot"
	"github.com/nghyane/llm-mux/internal/auth/iflow"
	"github.com/nghyane/llm-mux/internal/auth/qwen"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/misc"
	"github.com/nghyane/llm-mux/internal/oauth"
	"github.com/nghyane/llm-mux/internal/provider"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
)

// oauthService is the shared OAuth service instance for the unified API.
var oauthService = oauth.NewService()

// Device flow timeout duration
const (
	deviceFlowTimeout    = 10 * time.Minute
	callbackPollInterval = 2 * time.Second
)

// emailReplacer is reused for converting email to filename-safe format.
var emailReplacer = strings.NewReplacer("@", "_", ".", "_")

// OAuthStartRequest represents the request body for starting an OAuth flow.
type OAuthStartRequest struct {
	Provider  string `json:"provider" binding:"required"`
	ProjectID string `json:"project_id,omitempty"`
}

// OAuthStartResponse represents the response for starting an OAuth flow.
type OAuthStartResponse struct {
	Status        string `json:"status"`
	AuthURL       string `json:"auth_url,omitempty"`
	State         string `json:"state,omitempty"`
	ID            string `json:"id,omitempty"`
	Error         string `json:"error,omitempty"`
	CodeVerifier  string `json:"code_verifier,omitempty"`  // For PKCE providers
	CodeChallenge string `json:"code_challenge,omitempty"` // For PKCE providers
	// Device flow fields
	FlowType        string `json:"flow_type,omitempty"`        // "oauth" or "device"
	UserCode        string `json:"user_code,omitempty"`        // Device flow user code
	VerificationURL string `json:"verification_url,omitempty"` // Device flow verification URL
	ExpiresIn       int    `json:"expires_in,omitempty"`       // Device code expiry in seconds
	Interval        int    `json:"interval,omitempty"`         // Polling interval in seconds
}

// oauthCallbackData represents parsed callback file content.
type oauthCallbackData struct {
	Code        string `json:"code"`
	RedirectURI string `json:"redirect_uri"`
	Error       string `json:"error"`
}

// OAuthStart handles POST /v0/management/oauth/start
// Initiates an OAuth flow for the specified provider.
// Supports: OAuth (claude, codex, gemini, antigravity, iflow), Device Flow (qwen, copilot)
func (h *Handler) OAuthStart(c *gin.Context) {
	var req OAuthStartRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, OAuthStartResponse{
			Status: "error",
			Error:  "Invalid request body: provider is required",
		})
		return
	}

	// Normalize provider name
	providerName := normalizeProvider(req.Provider)

	// Handle device flow providers separately
	switch providerName {
	case "qwen":
		h.startQwenDeviceFlow(c)
		return
	case "copilot":
		h.startCopilotDeviceFlow(c)
		return
	}

	// Build auth URL for OAuth providers
	authURL, state, codeVerifier, err := h.buildProviderAuthURL(providerName)
	if err != nil {
		c.JSON(http.StatusBadRequest, OAuthStartResponse{
			Status: "error",
			Error:  err.Error(),
		})
		return
	}

	// Register OAuth request with codeVerifier for PKCE providers
	oauthReq := oauthService.Registry().Create(state, providerName, oauth.ModeWebUI)
	oauthReq.CodeVerifier = codeVerifier

	// Start callback forwarder for WebUI mode
	if targetURL, errTarget := h.managementCallbackURL("/" + providerName + "/callback"); errTarget == nil {
		if port := oauth.GetCallbackPort(providerName); port > 0 {
			_, _ = startCallbackForwarder(port, providerName, targetURL)
		}
	}

	// Start background polling goroutine
	ctx, cancel := context.WithTimeout(context.Background(), deviceFlowTimeout)
	go h.pollOAuthCallback(ctx, cancel, providerName, state)

	c.JSON(http.StatusOK, OAuthStartResponse{
		Status:       "ok",
		FlowType:     "oauth",
		AuthURL:      authURL,
		State:        state,
		ID:           state,
		CodeVerifier: codeVerifier,
	})
}

// normalizeProvider converts provider aliases to canonical names.
func normalizeProvider(provider string) string {
	switch provider {
	case "claude", "anthropic":
		return "claude"
	case "gemini", "gemini-cli":
		return "gemini"
	case "copilot", "github-copilot":
		return "copilot"
	default:
		return provider
	}
}

// pollOAuthCallback is a unified poller for all OAuth providers.
// It polls the callback file and dispatches to provider-specific token exchange.
func (h *Handler) pollOAuthCallback(ctx context.Context, cancel context.CancelFunc, providerName, state string) {
	defer cancel()

	log.WithFields(log.Fields{"state": state, "provider": providerName}).Info("Waiting for OAuth callback...")

	callback, err := h.waitForCallbackFile(ctx, providerName, state)
	if err != nil {
		if ctx.Err() != nil {
			oauthService.Registry().Cancel(state)
			log.WithField("state", state).Infof("%s OAuth cancelled or timed out", providerName)
		} else {
			oauthService.Registry().Fail(state, err.Error())
		}
		return
	}

	log.WithFields(log.Fields{"state": state, "provider": providerName}).Info("Exchanging code for tokens...")

	record, err := h.exchangeOAuthCode(ctx, providerName, state, callback)
	if err != nil {
		oauthService.Registry().Fail(state, fmt.Sprintf("Token exchange failed: %v", err))
		log.WithError(err).WithField("provider", providerName).Error("Token exchange failed")
		return
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		oauthService.Registry().Fail(state, fmt.Sprintf("Failed to save: %v", err))
		return
	}

	oauthService.Registry().Complete(state, &oauth.OAuthResult{State: state, Code: "success"})
	log.WithFields(log.Fields{"state": state, "path": savedPath, "provider": providerName}).Infof("%s authentication successful", providerName)
}

// waitForCallbackFile polls for the OAuth callback file and returns parsed data.
func (h *Handler) waitForCallbackFile(ctx context.Context, providerName, state string) (*oauthCallbackData, error) {
	callbackFile := fmt.Sprintf("%s/.oauth-%s-%s.oauth", h.cfg.AuthDir, providerName, state)
	ticker := time.NewTicker(callbackPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			data, err := os.ReadFile(callbackFile)
			if err != nil {
				continue
			}

			var callback oauthCallbackData
			if json.Unmarshal(data, &callback) != nil {
				continue
			}
			_ = os.Remove(callbackFile)

			if callback.Error != "" {
				return nil, fmt.Errorf("oauth error: %s", callback.Error)
			}
			if callback.Code == "" {
				continue
			}
			return &callback, nil
		}
	}
}

// exchangeOAuthCode dispatches to provider-specific token exchange logic.
func (h *Handler) exchangeOAuthCode(ctx context.Context, providerName, state string, callback *oauthCallbackData) (*provider.Auth, error) {
	switch providerName {
	case "gemini", "antigravity":
		return h.exchangeGoogleCode(ctx, providerName, callback.Code)
	case "claude":
		return h.exchangeClaudeCode(ctx, state, callback.Code)
	case "codex":
		return h.exchangeCodexCode(ctx, state, callback.Code)
	case "iflow":
		return h.exchangeIFlowCode(ctx, callback)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", providerName)
	}
}

// startQwenDeviceFlow initiates Qwen device authorization flow.
func (h *Handler) startQwenDeviceFlow(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), deviceFlowTimeout)

	qwenAuth := qwen.NewQwenAuth(h.cfg)
	deviceFlow, err := qwenAuth.InitiateDeviceFlow(ctx)
	if err != nil {
		cancel()
		c.JSON(http.StatusInternalServerError, OAuthStartResponse{
			Status: "error",
			Error:  fmt.Sprintf("Failed to initiate device flow: %v", err),
		})
		return
	}

	state := fmt.Sprintf("qwen-%d", time.Now().UnixNano())
	oauthService.Registry().Create(state, "qwen", oauth.ModeWebUI)

	go h.pollQwenToken(ctx, cancel, qwenAuth, deviceFlow, state)

	c.JSON(http.StatusOK, OAuthStartResponse{
		Status:          "ok",
		FlowType:        "device",
		State:           state,
		ID:              state,
		UserCode:        deviceFlow.UserCode,
		AuthURL:         deviceFlow.VerificationURIComplete,
		VerificationURL: deviceFlow.VerificationURI,
		ExpiresIn:       deviceFlow.ExpiresIn,
		Interval:        deviceFlow.Interval,
	})
}

// pollQwenToken polls for Qwen token in background.
func (h *Handler) pollQwenToken(ctx context.Context, cancel context.CancelFunc, qwenAuth *qwen.QwenAuth, deviceFlow *qwen.DeviceFlow, state string) {
	defer cancel()

	log.WithField("state", state).Info("Waiting for Qwen authentication...")

	tokenData, err := qwenAuth.PollForToken(ctx, deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
	if err != nil {
		h.handlePollError(ctx, state, "Qwen", err)
		return
	}

	storage := qwenAuth.CreateTokenStorage(tokenData)
	storage.Email = fmt.Sprintf("qwen-%d", time.Now().UnixMilli())

	record := &provider.Auth{
		ID:       fmt.Sprintf("qwen-%s.json", storage.Email),
		Provider: "qwen",
		FileName: fmt.Sprintf("qwen-%s.json", storage.Email),
		Storage:  storage,
		Metadata: map[string]any{"email": storage.Email},
	}

	h.finishAuthFlow(ctx, state, record)
}

// startCopilotDeviceFlow initiates GitHub Copilot device authorization flow.
func (h *Handler) startCopilotDeviceFlow(c *gin.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), deviceFlowTimeout)

	copilotAuth := copilot.NewCopilotAuth(h.cfg)
	deviceCode, err := copilotAuth.StartDeviceFlow(ctx)
	if err != nil {
		cancel()
		c.JSON(http.StatusInternalServerError, OAuthStartResponse{
			Status: "error",
			Error:  fmt.Sprintf("Failed to start device flow: %v", err),
		})
		return
	}

	state := fmt.Sprintf("copilot-%s", deviceCode.DeviceCode[:8])
	oauthService.Registry().Create(state, "copilot", oauth.ModeWebUI)

	go h.pollCopilotToken(ctx, cancel, copilotAuth, deviceCode, state)

	c.JSON(http.StatusOK, OAuthStartResponse{
		Status:          "ok",
		FlowType:        "device",
		State:           state,
		ID:              state,
		UserCode:        deviceCode.UserCode,
		AuthURL:         deviceCode.VerificationURI,
		VerificationURL: deviceCode.VerificationURI,
		ExpiresIn:       deviceCode.ExpiresIn,
		Interval:        deviceCode.Interval,
	})
}

// pollCopilotToken polls for GitHub Copilot token in background.
func (h *Handler) pollCopilotToken(ctx context.Context, cancel context.CancelFunc, copilotAuth *copilot.CopilotAuth, deviceCode *copilot.DeviceCodeResponse, state string) {
	defer cancel()

	log.WithField("state", state).Info("Waiting for GitHub Copilot authentication...")

	creds, err := copilotAuth.WaitForAuthorization(ctx, deviceCode)
	if err != nil {
		h.handlePollError(ctx, state, "Copilot", err)
		return
	}

	// Verify Copilot API access
	if _, err = copilotAuth.GetCopilotAPIToken(ctx, creds.AccessToken); err != nil {
		oauthService.Registry().Fail(state, fmt.Sprintf("Failed to verify Copilot access: %v", err))
		log.WithError(err).WithField("state", state).Error("Failed to verify Copilot access")
		return
	}

	fileName := fmt.Sprintf("github-copilot-%s.json", creds.Username)
	record := &provider.Auth{
		ID:       fileName,
		Provider: "github-copilot",
		FileName: fileName,
		Label:    creds.Username,
		Metadata: map[string]any{
			"type":         "github-copilot",
			"access_token": creds.AccessToken,
			"token_type":   creds.TokenType,
			"scope":        creds.Scope,
			"username":     creds.Username,
			"timestamp":    time.Now().UnixMilli(),
		},
	}

	h.finishAuthFlow(ctx, state, record)
}

func (h *Handler) handlePollError(ctx context.Context, state, providerName string, err error) {
	if ctx.Err() != nil {
		oauthService.Registry().Cancel(state)
		log.WithField("state", state).Infof("%s authentication cancelled or timed out", providerName)
	} else {
		oauthService.Registry().Fail(state, fmt.Sprintf("Authentication failed: %v", err))
		log.WithError(err).WithField("state", state).Errorf("%s authentication failed", providerName)
	}
}

// finishAuthFlow saves the auth record and completes the OAuth flow.
func (h *Handler) finishAuthFlow(ctx context.Context, state string, record *provider.Auth) {
	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		oauthService.Registry().Fail(state, fmt.Sprintf("Failed to save tokens: %v", err))
		log.WithError(err).WithField("state", state).Error("Failed to save tokens")
		return
	}

	oauthService.Registry().Complete(state, &oauth.OAuthResult{State: state, Code: "success"})
	log.WithFields(log.Fields{"state": state, "path": savedPath}).Infof("%s authentication successful", record.Provider)
}

// OAuthStatus handles GET /v0/management/oauth/status/:state
func (h *Handler) OAuthStatus(c *gin.Context) {
	state := c.Param("state")
	if state == "" {
		respondBadRequest(c, "state parameter is required")
		return
	}

	resp, err := oauthService.GetStatus(state)
	if err != nil {
		respondNotFound(c, "OAuth state not found or expired")
		return
	}

	c.JSON(http.StatusOK, resp)
}

// OAuthCancel handles POST /v0/management/oauth/cancel/:state
func (h *Handler) OAuthCancel(c *gin.Context) {
	state := c.Param("state")
	if state == "" {
		respondBadRequest(c, "state parameter is required")
		return
	}

	if err := oauthService.Cancel(state); err != nil {
		respondNotFound(c, "OAuth state not found or already completed")
		return
	}

	respondOK(c, gin.H{"status": "ok"})
}

// GetOAuthService returns the shared OAuth service instance.
func GetOAuthService() *oauth.Service {
	return oauthService
}

// =============================================================================
// Provider Auth URL Builders
// =============================================================================

// buildProviderAuthURL builds the authorization URL for a provider.
func (h *Handler) buildProviderAuthURL(providerName string) (authURL, state, codeVerifier string, err error) {
	state, err = misc.GenerateRandomState()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate state: %w", err)
	}

	switch providerName {
	case "claude":
		return h.buildClaudeAuthURL(state)
	case "codex":
		return h.buildCodexAuthURL(state)
	case "gemini", "antigravity":
		return h.buildGoogleAuthURL(providerName, state)
	case "iflow":
		return h.buildIFlowAuthURL(state)
	default:
		return "", "", "", fmt.Errorf("unsupported OAuth provider: %s", providerName)
	}
}

func (h *Handler) buildClaudeAuthURL(state string) (string, string, string, error) {
	pkceCodes, err := claude.GeneratePKCECodes()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate PKCE codes: %w", err)
	}

	claudeAuth := claude.NewClaudeAuth(h.cfg)
	authURL, _, err := claudeAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate auth URL: %w", err)
	}

	return authURL, state, pkceCodes.CodeVerifier, nil
}

func (h *Handler) buildCodexAuthURL(state string) (string, string, string, error) {
	pkceCodes, err := codex.GeneratePKCECodes()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate PKCE codes: %w", err)
	}

	codexAuth := codex.NewCodexAuth(h.cfg)
	authURL, err := codexAuth.GenerateAuthURL(state, pkceCodes)
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate auth URL: %w", err)
	}

	return authURL, state, pkceCodes.CodeVerifier, nil
}

func (h *Handler) buildGoogleAuthURL(providerName, state string) (string, string, string, error) {
	cfg, ok := googleOAuthConfigs[providerName]
	if !ok {
		return "", "", "", fmt.Errorf("unknown Google OAuth provider: %s", providerName)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/%s", oauth.GetCallbackPort(providerName), cfg.CallbackPath)

	conf := &oauth2.Config{
		ClientID:     cfg.ClientID,
		ClientSecret: cfg.ClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       cfg.Scopes,
		Endpoint:     google.Endpoint,
	}

	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	return authURL, state, "", nil
}

func (h *Handler) buildIFlowAuthURL(state string) (string, string, string, error) {
	iflowAuth := iflow.NewIFlowAuth(h.cfg)
	authURL, _ := iflowAuth.AuthorizationURL(state, iflow.CallbackPort)
	return authURL, state, "", nil
}

// =============================================================================
// Token Exchange Functions
// =============================================================================

type googleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	CallbackPath string
	FetchProject bool
	Scopes       []string
}

var googleOAuthConfigs = map[string]googleOAuthConfig{
	"gemini": {
		ClientID:     oauth.GeminiClientID,
		ClientSecret: oauth.GeminiClientSecret,
		CallbackPath: "oauth2callback",
		FetchProject: false,
		Scopes:       []string{"openid", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/cloud-platform"},
	},
	"antigravity": {
		ClientID:     oauth.AntigravityClientID,
		ClientSecret: oauth.AntigravityClientSecret,
		CallbackPath: "oauth-callback",
		FetchProject: true,
		Scopes: []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
			"https://www.googleapis.com/auth/cclog",
			"https://www.googleapis.com/auth/experimentsandconfigs",
		},
	},
}

func (h *Handler) exchangeGoogleCode(ctx context.Context, providerName, code string) (*provider.Auth, error) {
	cfg, ok := googleOAuthConfigs[providerName]
	if !ok {
		return nil, fmt.Errorf("unknown Google OAuth provider: %s", providerName)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/%s", oauth.GetCallbackPort(providerName), cfg.CallbackPath)
	httpClient := h.getHTTPClient()

	tokenResp, err := exchangeGoogleOAuthCode(ctx, code, redirectURI, cfg.ClientID, cfg.ClientSecret, httpClient)
	if err != nil {
		return nil, err
	}

	// Fetch user email
	var email string
	if tokenResp.AccessToken != "" {
		if info, _ := fetchGoogleUserInfo(ctx, tokenResp.AccessToken, httpClient); info != nil {
			email = strings.TrimSpace(info.Email)
		}
	}

	// Fetch project ID (antigravity only)
	var projectID string
	if cfg.FetchProject && tokenResp.AccessToken != "" {
		projectID, _ = fetchAntigravityProjectID(ctx, tokenResp.AccessToken, httpClient)
	}

	return buildGoogleAuthRecord(providerName, tokenResp, email, projectID), nil
}

func (h *Handler) exchangeClaudeCode(ctx context.Context, state, code string) (*provider.Auth, error) {
	oauthReq := oauthService.Registry().Get(state)
	if oauthReq == nil || oauthReq.CodeVerifier == "" {
		return nil, fmt.Errorf("codeVerifier not found in registry")
	}

	claudeAuth := claude.NewClaudeAuth(h.cfg)
	pkceCodes := &claude.PKCECodes{CodeVerifier: oauthReq.CodeVerifier}

	bundle, err := claudeAuth.ExchangeCodeForTokens(ctx, code, state, pkceCodes)
	if err != nil {
		return nil, err
	}

	storage := claudeAuth.CreateTokenStorage(bundle)
	email := strings.TrimSpace(storage.Email)

	return buildAuthRecordWithEmail("claude", email, storage, nil), nil
}

func (h *Handler) exchangeCodexCode(ctx context.Context, state, code string) (*provider.Auth, error) {
	oauthReq := oauthService.Registry().Get(state)
	if oauthReq == nil || oauthReq.CodeVerifier == "" {
		return nil, fmt.Errorf("codeVerifier not found in registry")
	}

	codexAuth := codex.NewCodexAuth(h.cfg)
	pkceCodes := &codex.PKCECodes{CodeVerifier: oauthReq.CodeVerifier}

	bundle, err := codexAuth.ExchangeCodeForTokens(ctx, code, pkceCodes)
	if err != nil {
		return nil, err
	}

	storage := codexAuth.CreateTokenStorage(bundle)
	email := strings.TrimSpace(storage.Email)

	return buildAuthRecordWithEmail("codex", email, storage, map[string]any{"account_id": storage.AccountID}), nil
}

func (h *Handler) exchangeIFlowCode(ctx context.Context, callback *oauthCallbackData) (*provider.Auth, error) {
	redirectURI := callback.RedirectURI
	if redirectURI == "" {
		redirectURI = fmt.Sprintf("http://localhost:%d/oauth2callback", iflow.CallbackPort)
	}

	iflowAuth := iflow.NewIFlowAuth(h.cfg)
	tokenData, err := iflowAuth.ExchangeCodeForTokens(ctx, callback.Code, redirectURI)
	if err != nil {
		return nil, err
	}

	storage := iflowAuth.CreateTokenStorage(tokenData)
	email := strings.TrimSpace(storage.Email)

	return buildAuthRecordWithEmail("iflow", email, storage, map[string]any{"api_key": storage.APIKey}), nil
}

// =============================================================================
// Helper Functions
// =============================================================================

func buildAuthRecordWithEmail(providerName, email string, storage auth.TokenStorage, extraMeta map[string]any) *provider.Auth {
	fileName := providerName + ".json"
	label := providerName
	if email != "" {
		fileName = fmt.Sprintf("%s-%s.json", providerName, emailReplacer.Replace(email))
		label = email
	}

	metadata := map[string]any{"email": email}
	for k, v := range extraMeta {
		metadata[k] = v
	}

	return &provider.Auth{
		ID:       fileName,
		Provider: providerName,
		FileName: fileName,
		Label:    label,
		Storage:  storage,
		Metadata: metadata,
	}
}

// buildGoogleAuthRecord creates auth record for Google OAuth providers.
func buildGoogleAuthRecord(providerType string, tokenResp *googleTokenResponse, email, projectID string) *provider.Auth {
	now := time.Now()
	var metadata map[string]any
	var fileName string

	if providerType == "gemini" {
		tokenData := map[string]any{
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"token_type":    tokenResp.TokenType,
			"expiry":        now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		}
		metadata = map[string]any{
			"type": "gemini", "token": tokenData, "email": email,
			"project_id": "", "auto": true, "checked": false,
		}
		fileName = "gemini.json"
		if email != "" {
			fileName = fmt.Sprintf("gemini-%s-all.json", email)
		}
	} else {
		metadata = map[string]any{
			"type":          providerType,
			"access_token":  tokenResp.AccessToken,
			"refresh_token": tokenResp.RefreshToken,
			"expires_in":    tokenResp.ExpiresIn,
			"timestamp":     now.UnixMilli(),
			"expired":       now.Add(time.Duration(tokenResp.ExpiresIn) * time.Second).Format(time.RFC3339),
		}
		if email != "" {
			metadata["email"] = email
		}
		if projectID != "" {
			metadata["project_id"] = projectID
		}
		fileName = providerType + ".json"
		if email != "" {
			fileName = fmt.Sprintf("%s-%s.json", providerType, emailReplacer.Replace(email))
		}
	}

	label := email
	if label == "" {
		label = providerType
	}

	return &provider.Auth{
		ID:       fileName,
		Provider: providerType,
		FileName: fileName,
		Label:    label,
		Metadata: metadata,
	}
}

// googleTokenResponse represents the token response from Google OAuth.
type googleTokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
	TokenType    string `json:"token_type"`
}

// googleUserInfo represents user info from Google.
type googleUserInfo struct {
	Email string `json:"email"`
}

func exchangeGoogleOAuthCode(ctx context.Context, code, redirectURI, clientID, clientSecret string, httpClient *http.Client) (*googleTokenResponse, error) {
	data := url.Values{
		"code":          {code},
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"redirect_uri":  {redirectURI},
		"grant_type":    {"authorization_code"},
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("token exchange failed: %d - %s", resp.StatusCode, string(body))
	}

	var token googleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, err
	}
	return &token, nil
}

func fetchGoogleUserInfo(ctx context.Context, accessToken string, httpClient *http.Client) (*googleUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v1/userinfo?alt=json", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("userinfo request failed: %d", resp.StatusCode)
	}

	var info googleUserInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, err
	}
	return &info, nil
}

func fetchAntigravityProjectID(ctx context.Context, accessToken string, httpClient *http.Client) (string, error) {
	loadReqBody := map[string]any{
		"metadata": map[string]string{
			"ideType": "IDE_UNSPECIFIED", "platform": "PLATFORM_UNSPECIFIED", "pluginType": "GEMINI",
		},
	}

	rawBody, err := json.Marshal(loadReqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", strings.NewReader(string(rawBody)))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "google-api-nodejs-client/9.15.1")
	req.Header.Set("X-Goog-Api-Client", "google-cloud-sdk vscode_cloudshelleditor/0.1")
	req.Header.Set("Client-Metadata", `{"ideType":"IDE_UNSPECIFIED","platform":"PLATFORM_UNSPECIFIED","pluginType":"GEMINI"}`)

	resp, err := httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("request failed with status %d", resp.StatusCode)
	}

	var loadResp map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&loadResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	// Extract projectID from response
	if id, ok := loadResp["cloudaicompanionProject"].(string); ok && id != "" {
		return strings.TrimSpace(id), nil
	}
	if projectMap, ok := loadResp["cloudaicompanionProject"].(map[string]any); ok {
		if id, ok := projectMap["id"].(string); ok && id != "" {
			return strings.TrimSpace(id), nil
		}
	}

	return "", fmt.Errorf("no cloudaicompanionProject in response")
}
