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
	"github.com/nghyane/llm-mux/internal/auth/claude"
	"github.com/nghyane/llm-mux/internal/auth/codex"
	"github.com/nghyane/llm-mux/internal/auth/copilot"
	"github.com/nghyane/llm-mux/internal/auth/iflow"
	"github.com/nghyane/llm-mux/internal/auth/qwen"
	"github.com/nghyane/llm-mux/internal/misc"
	"github.com/nghyane/llm-mux/internal/oauth"
	"github.com/nghyane/llm-mux/internal/util"
	coreauth "github.com/nghyane/llm-mux/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
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
	provider := normalizeProvider(req.Provider)

	// Handle device flow providers separately
	switch provider {
	case "qwen":
		h.startQwenDeviceFlow(c)
		return
	case "copilot":
		h.startCopilotDeviceFlow(c)
		return
	}

	// Build auth URL for OAuth providers
	authURL, state, codeVerifier, err := h.buildProviderAuthURL(provider)
	if err != nil {
		c.JSON(http.StatusBadRequest, OAuthStartResponse{
			Status: "error",
			Error:  err.Error(),
		})
		return
	}

	// Register OAuth request with codeVerifier for PKCE providers
	oauthReq := oauthService.Registry().Create(state, provider, oauth.ModeWebUI)
	oauthReq.CodeVerifier = codeVerifier

	// Start callback forwarder for WebUI mode
	if targetURL, errTarget := h.managementCallbackURL("/" + provider + "/callback"); errTarget == nil {
		if port := oauth.GetCallbackPort(provider); port > 0 {
			_, _ = startCallbackForwarder(port, provider, targetURL)
		}
	}

	// Start background polling goroutine
	ctx, cancel := context.WithTimeout(context.Background(), deviceFlowTimeout)
	go h.pollOAuthCallback(ctx, cancel, provider, state)

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
func (h *Handler) pollOAuthCallback(ctx context.Context, cancel context.CancelFunc, provider, state string) {
	defer cancel()

	log.WithFields(log.Fields{"state": state, "provider": provider}).Info("Waiting for OAuth callback...")

	callback, err := h.waitForCallbackFile(ctx, provider, state)
	if err != nil {
		if ctx.Err() != nil {
			oauthService.Registry().Cancel(state)
			log.WithField("state", state).Infof("%s OAuth cancelled or timed out", provider)
		} else {
			oauthService.Registry().Fail(state, err.Error())
		}
		return
	}

	log.WithFields(log.Fields{"state": state, "provider": provider}).Info("Exchanging code for tokens...")

	record, err := h.exchangeOAuthCode(ctx, provider, state, callback)
	if err != nil {
		oauthService.Registry().Fail(state, fmt.Sprintf("Token exchange failed: %v", err))
		log.WithError(err).WithField("provider", provider).Error("Token exchange failed")
		return
	}

	savedPath, err := h.saveTokenRecord(ctx, record)
	if err != nil {
		oauthService.Registry().Fail(state, fmt.Sprintf("Failed to save: %v", err))
		return
	}

	oauthService.Registry().Complete(state, &oauth.OAuthResult{State: state, Code: "success"})
	log.WithFields(log.Fields{"state": state, "path": savedPath, "provider": provider}).Infof("%s authentication successful", provider)
}

// waitForCallbackFile polls for the OAuth callback file and returns parsed data.
func (h *Handler) waitForCallbackFile(ctx context.Context, provider, state string) (*oauthCallbackData, error) {
	callbackFile := fmt.Sprintf("%s/.oauth-%s-%s.oauth", h.cfg.AuthDir, provider, state)
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
func (h *Handler) exchangeOAuthCode(ctx context.Context, provider, state string, callback *oauthCallbackData) (*coreauth.Auth, error) {
	switch provider {
	case "gemini", "antigravity":
		return h.exchangeGoogleCode(ctx, provider, callback.Code)
	case "claude":
		return h.exchangeClaudeCode(ctx, state, callback.Code)
	case "codex":
		return h.exchangeCodexCode(ctx, state, callback.Code)
	case "iflow":
		return h.exchangeIFlowCode(ctx, callback)
	default:
		return nil, fmt.Errorf("unsupported provider: %s", provider)
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

	tokenData, err := qwenAuth.PollForToken(deviceFlow.DeviceCode, deviceFlow.CodeVerifier)
	if err != nil {
		if ctx.Err() != nil {
			oauthService.Registry().Cancel(state)
			log.WithField("state", state).Info("Qwen authentication cancelled or timed out")
		} else {
			oauthService.Registry().Fail(state, fmt.Sprintf("Authentication failed: %v", err))
			log.WithError(err).WithField("state", state).Error("Qwen authentication failed")
		}
		return
	}

	storage := qwenAuth.CreateTokenStorage(tokenData)
	storage.Email = fmt.Sprintf("qwen-%d", time.Now().UnixMilli())

	record := &coreauth.Auth{
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
		if ctx.Err() != nil {
			oauthService.Registry().Cancel(state)
			log.WithField("state", state).Info("Copilot authentication cancelled or timed out")
		} else {
			oauthService.Registry().Fail(state, fmt.Sprintf("Authentication failed: %v", err))
			log.WithError(err).WithField("state", state).Error("Copilot authentication failed")
		}
		return
	}

	// Verify Copilot API access
	if _, err = copilotAuth.GetCopilotAPIToken(ctx, creds.AccessToken); err != nil {
		oauthService.Registry().Fail(state, fmt.Sprintf("Failed to verify Copilot access: %v", err))
		log.WithError(err).WithField("state", state).Error("Failed to verify Copilot access")
		return
	}

	fileName := fmt.Sprintf("github-copilot-%s.json", creds.Username)
	record := &coreauth.Auth{
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

// finishAuthFlow saves the auth record and completes the OAuth flow.
func (h *Handler) finishAuthFlow(ctx context.Context, state string, record *coreauth.Auth) {
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
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "state parameter is required"})
		return
	}

	resp, err := oauthService.GetStatus(state)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "OAuth state not found or expired"})
		return
	}

	c.JSON(http.StatusOK, resp)
}

// OAuthCancel handles POST /v0/management/oauth/cancel/:state
func (h *Handler) OAuthCancel(c *gin.Context) {
	state := c.Param("state")
	if state == "" {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "state parameter is required"})
		return
	}

	if err := oauthService.Cancel(state); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "OAuth state not found or already completed"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetOAuthService returns the shared OAuth service instance.
func GetOAuthService() *oauth.Service {
	return oauthService
}

// =============================================================================
// Provider Auth URL Builders
// =============================================================================

// buildProviderAuthURL builds the authorization URL for a provider.
func (h *Handler) buildProviderAuthURL(provider string) (authURL, state, codeVerifier string, err error) {
	state, err = misc.GenerateRandomState()
	if err != nil {
		return "", "", "", fmt.Errorf("failed to generate state: %w", err)
	}

	switch provider {
	case "claude":
		return h.buildClaudeAuthURL(state)
	case "codex":
		return h.buildCodexAuthURL(state)
	case "gemini":
		return h.buildGeminiAuthURL(state)
	case "antigravity":
		return h.buildAntigravityAuthURL(state)
	case "iflow":
		return h.buildIFlowAuthURL(state)
	default:
		return "", "", "", fmt.Errorf("unsupported OAuth provider: %s", provider)
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

func (h *Handler) buildGeminiAuthURL(state string) (string, string, string, error) {
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth2callback", oauth.GetCallbackPort("gemini"))

	conf := &oauth2.Config{
		ClientID:     oauth.GeminiClientID,
		ClientSecret: oauth.GeminiClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       []string{"openid", "https://www.googleapis.com/auth/userinfo.email", "https://www.googleapis.com/auth/cloud-platform"},
		Endpoint:     google.Endpoint,
	}

	authURL := conf.AuthCodeURL(state, oauth2.AccessTypeOffline, oauth2.SetAuthURLParam("prompt", "consent"))
	return authURL, state, "", nil
}

func (h *Handler) buildAntigravityAuthURL(state string) (string, string, string, error) {
	redirectURI := fmt.Sprintf("http://localhost:%d/oauth-callback", oauth.GetCallbackPort("antigravity"))

	conf := &oauth2.Config{
		ClientID:     oauth.AntigravityClientID,
		ClientSecret: oauth.AntigravityClientSecret,
		RedirectURL:  redirectURI,
		Scopes: []string{
			"https://www.googleapis.com/auth/cloud-platform",
			"https://www.googleapis.com/auth/userinfo.email",
			"https://www.googleapis.com/auth/userinfo.profile",
			"https://www.googleapis.com/auth/cclog",
			"https://www.googleapis.com/auth/experimentsandconfigs",
		},
		Endpoint: google.Endpoint,
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

// googleOAuthConfig holds provider-specific configuration for Google OAuth.
type googleOAuthConfig struct {
	ClientID     string
	ClientSecret string
	CallbackPath string
	FetchProject bool
}

var googleOAuthConfigs = map[string]googleOAuthConfig{
	"gemini": {
		ClientID:     oauth.GeminiClientID,
		ClientSecret: oauth.GeminiClientSecret,
		CallbackPath: "oauth2callback",
		FetchProject: false,
	},
	"antigravity": {
		ClientID:     oauth.AntigravityClientID,
		ClientSecret: oauth.AntigravityClientSecret,
		CallbackPath: "oauth-callback",
		FetchProject: true,
	},
}

func (h *Handler) exchangeGoogleCode(ctx context.Context, provider, code string) (*coreauth.Auth, error) {
	cfg, ok := googleOAuthConfigs[provider]
	if !ok {
		return nil, fmt.Errorf("unknown Google OAuth provider: %s", provider)
	}

	redirectURI := fmt.Sprintf("http://localhost:%d/%s", oauth.GetCallbackPort(provider), cfg.CallbackPath)
	httpClient := util.SetProxy(&h.cfg.SDKConfig, &http.Client{})

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

	return buildGoogleAuthRecord(provider, tokenResp, email, projectID), nil
}

func (h *Handler) exchangeClaudeCode(ctx context.Context, state, code string) (*coreauth.Auth, error) {
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

	fileName := "claude.json"
	label := "claude"
	if email != "" {
		fileName = fmt.Sprintf("claude-%s.json", emailReplacer.Replace(email))
		label = email
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "claude",
		FileName: fileName,
		Label:    label,
		Storage:  storage,
		Metadata: map[string]any{"email": email},
	}, nil
}

func (h *Handler) exchangeCodexCode(ctx context.Context, state, code string) (*coreauth.Auth, error) {
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

	fileName := "codex.json"
	label := "codex"
	if email != "" {
		fileName = fmt.Sprintf("codex-%s.json", emailReplacer.Replace(email))
		label = email
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Label:    label,
		Storage:  storage,
		Metadata: map[string]any{"email": email, "account_id": storage.AccountID},
	}, nil
}

func (h *Handler) exchangeIFlowCode(ctx context.Context, callback *oauthCallbackData) (*coreauth.Auth, error) {
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

	fileName := "iflow.json"
	label := "iflow"
	if email != "" {
		fileName = fmt.Sprintf("iflow-%s.json", emailReplacer.Replace(email))
		label = email
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: "iflow",
		FileName: fileName,
		Label:    label,
		Storage:  storage,
		Metadata: map[string]any{"email": email, "api_key": storage.APIKey},
	}, nil
}

// =============================================================================
// Helper Functions
// =============================================================================

// buildGoogleAuthRecord creates auth record for Google OAuth providers.
func buildGoogleAuthRecord(provider string, tokenResp *googleTokenResponse, email, projectID string) *coreauth.Auth {
	now := time.Now()
	var metadata map[string]any
	var fileName string

	if provider == "gemini" {
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
			"type":          provider,
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
		fileName = provider + ".json"
		if email != "" {
			fileName = fmt.Sprintf("%s-%s.json", provider, emailReplacer.Replace(email))
		}
	}

	label := email
	if label == "" {
		label = provider
	}

	return &coreauth.Auth{
		ID:       fileName,
		Provider: provider,
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
