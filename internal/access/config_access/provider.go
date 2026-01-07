package configaccess

import (
	"context"
	"net/http"
	"strings"
	"sync"

	internalaccess "github.com/nghyane/llm-mux/internal/access"
	"github.com/nghyane/llm-mux/internal/config"
)

var doRegister = sync.OnceFunc(func() {
	internalaccess.RegisterProvider(config.AccessProviderTypeConfigAPIKey, newProvider)
})

// Register ensures the config-access provider is available to the access manager.
func Register() {
	doRegister()
}

type provider struct {
	name string
	keys map[string]struct{}
}

func newProvider(cfg *config.AccessProvider, _ *config.SDKConfig) (internalaccess.Provider, error) {
	name := cfg.Name
	if name == "" {
		name = config.DefaultAccessProviderName
	}
	keys := make(map[string]struct{}, len(cfg.APIKeys))
	for _, key := range cfg.APIKeys {
		if key == "" {
			continue
		}
		keys[key] = struct{}{}
	}
	return &provider{name: name, keys: keys}, nil
}

func (p *provider) Identifier() string {
	if p == nil || p.name == "" {
		return config.DefaultAccessProviderName
	}
	return p.name
}

func (p *provider) Authenticate(_ context.Context, r *http.Request) (*internalaccess.Result, error) {
	if p == nil {
		return nil, internalaccess.ErrNotHandled
	}
	if len(p.keys) == 0 {
		return nil, internalaccess.ErrNotHandled
	}
	authHeader := r.Header.Get("Authorization")
	authHeaderGoogle := r.Header.Get("X-Goog-Api-Key")
	authHeaderAnthropic := r.Header.Get("X-Api-Key")
	queryKey := ""
	queryAuthToken := ""
	if r.URL != nil {
		queryKey = r.URL.Query().Get("key")
		queryAuthToken = r.URL.Query().Get("auth_token")
	}
	if authHeader == "" && authHeaderGoogle == "" && authHeaderAnthropic == "" && queryKey == "" && queryAuthToken == "" {
		return nil, internalaccess.ErrNoCredentials
	}

	apiKey := extractBearerToken(authHeader)

	candidates := []struct {
		value  string
		source string
	}{
		{apiKey, "authorization"},
		{authHeaderGoogle, "x-goog-api-key"},
		{authHeaderAnthropic, "x-api-key"},
		{queryKey, "query-key"},
		{queryAuthToken, "query-auth-token"},
	}

	for _, candidate := range candidates {
		if candidate.value == "" {
			continue
		}
		if _, ok := p.keys[candidate.value]; ok {
			return &internalaccess.Result{
				Provider:  p.Identifier(),
				Principal: candidate.value,
				Metadata: map[string]string{
					"source": candidate.source,
				},
			}, nil
		}
	}

	return nil, internalaccess.ErrInvalidCredential
}

func extractBearerToken(header string) string {
	if header == "" {
		return ""
	}
	parts := strings.SplitN(header, " ", 2)
	if len(parts) != 2 {
		return header
	}
	if strings.ToLower(parts[0]) != "bearer" {
		return header
	}
	return strings.TrimSpace(parts[1])
}
