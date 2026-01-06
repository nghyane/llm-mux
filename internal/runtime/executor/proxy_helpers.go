package executor

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"golang.org/x/net/proxy"
)

func NewProxyAwareHTTPClient(ctx context.Context, cfg *config.Config, auth *provider.Auth, timeout time.Duration) *http.Client {
	httpClient := AcquireHTTPClient()
	if timeout > 0 {
		httpClient.Timeout = timeout
	}

	var proxyURL string
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}

	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}

	if proxyURL != "" {
		// Use cached transport for proxy URLs to enable connection pooling
		transport := getCachedTransport(proxyURL)
		if transport != nil {
			httpClient.Transport = transport
			return httpClient
		}
		log.Debugf("failed to setup proxy from URL: %s, falling back to context transport", proxyURL)
	}

	if rt, ok := ctx.Value("cliproxy.roundtripper").(http.RoundTripper); ok && rt != nil {
		httpClient.Transport = rt
		return httpClient
	}

	httpClient.Transport = SharedTransport
	return httpClient
}

func buildProxyTransport(proxyURLStr string) *http.Transport {
	if proxyURLStr == "" {
		return nil
	}

	parsedURL, errParse := url.Parse(proxyURLStr)
	if errParse != nil {
		log.Errorf("parse proxy URL failed: %v", errParse)
		return nil
	}

	switch parsedURL.Scheme {
	case "socks5":
		var proxyAuth *proxy.Auth
		if parsedURL.User != nil {
			username := parsedURL.User.Username()
			password, _ := parsedURL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		dialer, errSOCKS5 := proxy.SOCKS5("tcp", parsedURL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			log.Errorf("create SOCKS5 dialer failed: %v", errSOCKS5)
			return nil
		}
		return SOCKS5Transport(dialer.Dial)
	case "http", "https":
		return ProxyTransport(parsedURL)
	default:
		log.Errorf("unsupported proxy scheme: %s", parsedURL.Scheme)
		return nil
	}
}
