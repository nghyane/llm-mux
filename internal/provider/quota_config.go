package provider

import "time"

type QuotaType int

const (
	QuotaTypeRequests QuotaType = iota
	QuotaTypeTokens
)

func (q QuotaType) String() string {
	switch q {
	case QuotaTypeRequests:
		return "requests"
	case QuotaTypeTokens:
		return "tokens"
	default:
		return "unknown"
	}
}

type ProviderQuotaConfig struct {
	Provider       string
	WindowDuration time.Duration
	QuotaType      QuotaType
	EstimatedLimit int64
	GroupResolver  QuotaGroupResolver
	StaggerBucket  time.Duration
	StickyEnabled  bool
}

var defaultProviderQuotaConfigs = map[string]*ProviderQuotaConfig{
	"antigravity": {
		Provider:       "antigravity",
		WindowDuration: 5 * time.Hour,
		QuotaType:      QuotaTypeTokens,
		EstimatedLimit: 1_000_000,
		GroupResolver:  AntigravityQuotaGroupResolver,
		StaggerBucket:  30 * time.Minute,
		StickyEnabled:  false,
	},
	"claude": {
		Provider:       "claude",
		WindowDuration: 5 * time.Hour,
		QuotaType:      QuotaTypeTokens,
		EstimatedLimit: 500_000,
		GroupResolver:  nil,
		StaggerBucket:  30 * time.Minute,
		StickyEnabled:  true,
	},
	"copilot": {
		Provider:       "copilot",
		WindowDuration: 24 * time.Hour,
		QuotaType:      QuotaTypeRequests,
		EstimatedLimit: 10_000,
		GroupResolver:  nil,
		StaggerBucket:  2 * time.Hour,
		StickyEnabled:  true,
	},
	"gemini": {
		Provider:       "gemini",
		WindowDuration: 1 * time.Minute,
		QuotaType:      QuotaTypeRequests,
		EstimatedLimit: 60,
		GroupResolver:  nil,
		StaggerBucket:  10 * time.Second,
		StickyEnabled:  true,
	},
}

var defaultFallback = ProviderQuotaConfig{
	WindowDuration: 5 * time.Hour,
	QuotaType:      QuotaTypeTokens,
	EstimatedLimit: 500_000,
	StaggerBucket:  30 * time.Minute,
	StickyEnabled:  true,
}

func GetProviderQuotaConfig(provider string) *ProviderQuotaConfig {
	if cfg, ok := defaultProviderQuotaConfigs[provider]; ok {
		return cfg
	}
	fallback := defaultFallback
	fallback.Provider = provider
	return &fallback
}
