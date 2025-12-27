package copilot

import (
	"context"
	"fmt"
	"github.com/nghyane/llm-mux/internal/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	"github.com/nghyane/llm-mux/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	copilotClientID      = "Iv1.b507a08c87ecfe98"
	copilotDeviceCodeURL = "https://github.com/login/device/code"
	copilotTokenURL      = "https://github.com/login/oauth/access_token"
	copilotUserInfoURL   = "https://api.github.com/user"
	copilotAPITokenURL   = "https://api.github.com/copilot_internal/v2/token"
	copilotAPIEndpoint   = "https://api.githubcopilot.com"
	copilotUserAgent     = "GithubCopilot/1.0"
	copilotEditorVersion = "vscode/1.100.0"
	copilotPluginVersion = "copilot/1.300.0"

	defaultPollInterval = 5 * time.Second
	maxPollDuration     = 15 * time.Minute
)

// CopilotAuth handles GitHub Copilot authentication flow.
type CopilotAuth struct {
	httpClient *http.Client
	cfg        *config.Config
}

// NewCopilotAuth creates a new CopilotAuth service instance.
func NewCopilotAuth(cfg *config.Config) *CopilotAuth {
	client := &http.Client{Timeout: 30 * time.Second}
	if cfg != nil {
		client = util.SetProxy(&cfg.SDKConfig, client)
	}
	return &CopilotAuth{httpClient: client, cfg: cfg}
}

// StartDeviceFlow initiates the device flow authentication.
func (c *CopilotAuth) StartDeviceFlow(ctx context.Context) (*DeviceCodeResponse, error) {
	data := url.Values{}
	data.Set("client_id", copilotClientID)
	data.Set("scope", "user:email")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, copilotDeviceCodeURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to create device code request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: device code request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("copilot: device code failed with status %d: %s", resp.StatusCode, string(body))
	}

	var deviceCode DeviceCodeResponse
	if err = json.NewDecoder(resp.Body).Decode(&deviceCode); err != nil {
		return nil, fmt.Errorf("copilot: failed to decode device code: %w", err)
	}
	return &deviceCode, nil
}

// WaitForAuthorization polls for user authorization and returns credentials.
func (c *CopilotAuth) WaitForAuthorization(ctx context.Context, deviceCode *DeviceCodeResponse) (*CopilotCredentials, error) {
	if deviceCode == nil {
		return nil, fmt.Errorf("copilot: device code is nil")
	}

	interval := time.Duration(deviceCode.Interval) * time.Second
	if interval < defaultPollInterval {
		interval = defaultPollInterval
	}

	deadline := time.Now().Add(maxPollDuration)
	if deviceCode.ExpiresIn > 0 {
		codeDeadline := time.Now().Add(time.Duration(deviceCode.ExpiresIn) * time.Second)
		if codeDeadline.Before(deadline) {
			deadline = codeDeadline
		}
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("copilot: authorization cancelled: %w", ctx.Err())
		case <-ticker.C:
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("copilot: device code expired")
			}

			creds, status, err := c.exchangeDeviceCode(ctx, deviceCode.DeviceCode)
			if err != nil {
				return nil, err
			}

			switch status {
			case "authorization_pending":
				continue
			case "slow_down":
				interval += 5 * time.Second
				ticker.Reset(interval)
				continue
			case "":
				// Success - fetch username
				username, errUser := c.fetchUsername(ctx, creds.AccessToken)
				if errUser != nil {
					log.Warnf("copilot: failed to fetch username: %v", errUser)
					username = "unknown"
				}
				creds.Username = username
				creds.Type = "github-copilot"
				return creds, nil
			default:
				return nil, fmt.Errorf("copilot: %s", status)
			}
		}
	}
}

func (c *CopilotAuth) exchangeDeviceCode(ctx context.Context, deviceCode string) (*CopilotCredentials, string, error) {
	data := url.Values{}
	data.Set("client_id", copilotClientID)
	data.Set("device_code", deviceCode)
	data.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, copilotTokenURL, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, "", fmt.Errorf("copilot: failed to create token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("copilot: token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var result struct {
		Error       string `json:"error"`
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		Scope       string `json:"scope"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", fmt.Errorf("copilot: failed to decode token response: %w", err)
	}

	if result.Error != "" {
		return nil, result.Error, nil
	}

	if result.AccessToken == "" {
		return nil, "", fmt.Errorf("copilot: empty access token")
	}

	return &CopilotCredentials{
		AccessToken: result.AccessToken,
		TokenType:   result.TokenType,
		Scope:       result.Scope,
	}, "", nil
}

func (c *CopilotAuth) fetchUsername(ctx context.Context, accessToken string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotUserInfoURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "llm-mux")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("status %d", resp.StatusCode)
	}

	var userInfo struct {
		Login string `json:"login"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&userInfo); err != nil {
		return "", err
	}
	return userInfo.Login, nil
}

// GetCopilotAPIToken exchanges a GitHub access token for a Copilot API token.
func (c *CopilotAuth) GetCopilotAPIToken(ctx context.Context, githubAccessToken string) (*CopilotAPIToken, error) {
	if githubAccessToken == "" {
		return nil, fmt.Errorf("copilot: github access token is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotAPITokenURL, nil)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to create API token request: %w", err)
	}

	req.Header.Set("Authorization", "token "+githubAccessToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", copilotUserAgent)
	req.Header.Set("Editor-Version", copilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", copilotPluginVersion)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("copilot: API token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("copilot: failed to read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("copilot: API token failed with status %d: %s", resp.StatusCode, string(body))
	}

	var apiToken CopilotAPIToken
	if err = json.Unmarshal(body, &apiToken); err != nil {
		return nil, fmt.Errorf("copilot: failed to decode API token: %w", err)
	}

	if apiToken.Token == "" {
		return nil, fmt.Errorf("copilot: empty API token in response")
	}

	return &apiToken, nil
}

// GetAPIEndpoint returns the Copilot API endpoint URL.
func (c *CopilotAuth) GetAPIEndpoint() string {
	return copilotAPIEndpoint
}
