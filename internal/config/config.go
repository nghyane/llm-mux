// Package config provides configuration management for the CLI Proxy API server.
// It handles loading and parsing YAML configuration files, and provides structured
// access to application settings including server port, authentication directory,
// debug settings, proxy configuration, and API keys.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"syscall"

	"github.com/nghyane/llm-mux/internal/translator/ir"
	"gopkg.in/yaml.v3"
)

// SDKConfig represents the SDK-level configuration, loaded from a YAML file.
// This is the base configuration that can be embedded in the full Config.
type SDKConfig struct {
	// ProxyURL is the URL of an optional proxy server to use for outbound requests.
	ProxyURL string `yaml:"proxy-url" json:"proxy-url"`

	// RequestLog enables or disables detailed request logging functionality.
	RequestLog bool `yaml:"request-log" json:"request-log"`

	// APIKeys is a list of keys for authenticating clients to this proxy server.
	APIKeys []string `yaml:"api-keys" json:"api-keys"`

	// Access holds request authentication provider configuration.
	Access AccessConfig `yaml:"auth,omitempty" json:"auth,omitempty"`

	// ShowProviderPrefixes enables visual provider prefixes in model IDs (e.g., "[Gemini CLI] gemini-2.5-pro").
	// This is purely cosmetic and does not affect actual model routing to providers.
	ShowProviderPrefixes bool `yaml:"show-provider-prefixes" json:"show-provider-prefixes"`
}

// AccessConfig groups request authentication providers.
type AccessConfig struct {
	// Providers lists configured authentication providers.
	Providers []AccessProvider `yaml:"providers,omitempty" json:"providers,omitempty"`
}

// AccessProvider describes a request authentication provider entry.
type AccessProvider struct {
	// Name is the instance identifier for the provider.
	Name string `yaml:"name" json:"name"`

	// Type selects the provider implementation registered via the SDK.
	Type string `yaml:"type" json:"type"`

	// SDK optionally names a third-party SDK module providing this provider.
	SDK string `yaml:"sdk,omitempty" json:"sdk,omitempty"`

	// APIKeys lists inline keys for providers that require them.
	APIKeys []string `yaml:"api-keys,omitempty" json:"api-keys,omitempty"`

	// Config passes provider-specific options to the implementation.
	Config map[string]any `yaml:"config,omitempty" json:"config,omitempty"`
}

const (
	// AccessProviderTypeConfigAPIKey is the built-in provider validating inline API keys.
	AccessProviderTypeConfigAPIKey = "config-api-key"

	// DefaultAccessProviderName is applied when no provider name is supplied.
	DefaultAccessProviderName = "config-inline"
)

// ConfigAPIKeyProvider returns the first inline API key provider if present.
func (c *SDKConfig) ConfigAPIKeyProvider() *AccessProvider {
	if c == nil {
		return nil
	}
	for i := range c.Access.Providers {
		if c.Access.Providers[i].Type == AccessProviderTypeConfigAPIKey {
			if c.Access.Providers[i].Name == "" {
				c.Access.Providers[i].Name = DefaultAccessProviderName
			}
			return &c.Access.Providers[i]
		}
	}
	return nil
}

// MakeInlineAPIKeyProvider constructs an inline API key provider configuration.
// It returns nil when no keys are supplied.
func MakeInlineAPIKeyProvider(keys []string) *AccessProvider {
	if len(keys) == 0 {
		return nil
	}
	provider := &AccessProvider{
		Name:    DefaultAccessProviderName,
		Type:    AccessProviderTypeConfigAPIKey,
		APIKeys: append([]string(nil), keys...),
	}
	return provider
}

// Config represents the application's configuration, loaded from a YAML file.
type Config struct {
	SDKConfig        `yaml:",inline"`
	Port             int              `yaml:"port" json:"-"`
	TLS              TLSConfig        `yaml:"tls" json:"tls"`
	RemoteManagement RemoteManagement `yaml:"remote-management" json:"-"`
	AuthDir          string           `yaml:"auth-dir" json:"-"`
	Debug            bool             `yaml:"debug" json:"debug"`
	LoggingToFile    bool             `yaml:"logging-to-file" json:"logging-to-file"`

	Usage            UsageConfig   `yaml:"usage" json:"usage"`
	DisableCooling   bool          `yaml:"disable-cooling" json:"disable-cooling"`
	RequestRetry     int           `yaml:"request-retry" json:"request-retry"`
	MaxRetryInterval int           `yaml:"max-retry-interval" json:"max-retry-interval"`
	StreamTimeout    int           `yaml:"stream-timeout" json:"stream-timeout"`
	QuotaWindow      int           `yaml:"quota-window" json:"quota-window"`
	QuotaExceeded    QuotaExceeded `yaml:"quota-exceeded" json:"quota-exceeded"`

	WebsocketAuth bool `yaml:"ws-auth" json:"ws-auth"`
	DisableAuth   bool `yaml:"disable-auth" json:"disable-auth"`

	// Providers is the unified provider configuration.
	Providers []Provider `yaml:"providers,omitempty" json:"providers,omitempty"`

	// VertexCompatAPIKey is the configuration for Vertex AI-compatible API keys.
	VertexCompatAPIKey []VertexCompatKey `yaml:"vertex-api-key,omitempty" json:"vertex-api-key,omitempty"`

	AmpCode             AmpCode             `yaml:"ampcode" json:"ampcode"`
	OAuthExcludedModels map[string][]string `yaml:"oauth-excluded-models,omitempty" json:"oauth-excluded-models,omitempty"`
	Payload             PayloadConfig       `yaml:"payload" json:"payload"`
	Routing             RoutingConfig       `yaml:"routing,omitempty" json:"routing,omitempty"`

	// UseCanonicalTranslator enables the unified IR translator architecture (default: true).
	UseCanonicalTranslator bool `yaml:"use-canonical-translator" json:"use-canonical-translator" default:"true"`
}

// TLSConfig holds HTTPS server settings.
type TLSConfig struct {
	Enable bool   `yaml:"enable" json:"enable"`
	Cert   string `yaml:"cert" json:"cert"`
	Key    string `yaml:"key" json:"key"`
}

// RemoteManagement holds management API configuration under 'remote-management'.
type RemoteManagement struct {
	AllowRemote bool `yaml:"allow-remote"`
}

// QuotaExceeded defines the behavior when API quota limits are exceeded.
type QuotaExceeded struct {
	SwitchProject      bool `yaml:"switch-project" json:"switch-project"`
	SwitchPreviewModel bool `yaml:"switch-preview-model" json:"switch-preview-model"`
}

// UsageConfig defines usage tracking and persistence settings.
type UsageConfig struct {
	// DSN specifies the database connection using URI scheme:
	//   sqlite:///path/to/db.sqlite (or sqlite://~/relative/path)
	//   postgres://user:pass@host:port/dbname?sslmode=disable
	// Empty string disables usage persistence.
	DSN string `yaml:"dsn" json:"dsn"`

	// BatchSize defines records to batch before writing. Default: 100.
	BatchSize int `yaml:"batch-size" json:"batch-size"`

	// FlushInterval defines how often to flush pending writes.
	// Accepts duration string (e.g., "5s", "1m"). Default: "5s".
	FlushInterval string `yaml:"flush-interval" json:"flush-interval"`

	// RetentionDays defines how many days of records to keep. Default: 30.
	RetentionDays int `yaml:"retention-days" json:"retention-days"`
}

// AmpModelMapping defines a model name mapping for Amp CLI requests.
// When Amp requests a model that isn't available locally, this mapping
// allows routing to an alternative model that IS available.
type AmpModelMapping struct {
	// From is the model name that Amp CLI requests (e.g., "claude-opus-4.5").
	From string `yaml:"from" json:"from"`

	// To is the target model name to route to (e.g., "claude-sonnet-4").
	// The target model must have available providers in the registry.
	To string `yaml:"to" json:"to"`
}

// AmpCode groups Amp CLI integration settings including upstream routing and model fallback mappings.
type AmpCode struct {
	UpstreamURL                   string            `yaml:"upstream-url" json:"upstream-url"`
	UpstreamAPIKey                string            `yaml:"upstream-api-key" json:"upstream-api-key"`
	RestrictManagementToLocalhost bool              `yaml:"restrict-management-to-localhost" json:"restrict-management-to-localhost"`
	ModelMappings                 []AmpModelMapping `yaml:"model-mappings" json:"model-mappings"`
}

// PayloadConfig defines default and override parameter rules applied to provider payloads.
type PayloadConfig struct {
	Default  []PayloadRule `yaml:"default" json:"default"`
	Override []PayloadRule `yaml:"override" json:"override"`
}

// PayloadRule describes a single rule targeting a list of models with parameter updates.
type PayloadRule struct {
	Models []PayloadModelRule `yaml:"models" json:"models"`
	Params map[string]any     `yaml:"params" json:"params"`
}

// PayloadModelRule ties a model name pattern to a specific translator protocol.
type PayloadModelRule struct {
	Name     string `yaml:"name" json:"name"`
	Protocol string `yaml:"protocol" json:"protocol"`
}

// RoutingConfig defines provider routing and priority settings.
type RoutingConfig struct {
	// ProviderPriority maps provider names to their routing priority.
	// Lower values have higher priority (1 = highest).
	// Provider names must match executor identifiers exactly:
	// claude, antigravity, gemini-cli, vertex, aistudio, codex,
	// github-copilot, qwen, iflow, cline, kiro, gemini
	ProviderPriority map[string]int `yaml:"provider-priority,omitempty" json:"provider-priority,omitempty"`

	// Aliases maps user-facing model names to canonical internal names.
	// Handles naming inconsistencies across providers (e.g., "." vs "-").
	// Example: "claude-sonnet-4.5" -> "claude-sonnet-4-5"
	Aliases map[string]string `yaml:"aliases,omitempty" json:"aliases,omitempty"`

	// Fallbacks defines ordered fallback chains when a model is unavailable.
	// Supports tier downgrades and cross-vendor fallbacks.
	// Example: "claude-opus-4-5" -> ["claude-sonnet-4-5", "gpt-4o"]
	Fallbacks map[string][]string `yaml:"fallbacks,omitempty" json:"fallbacks,omitempty"`

	hasAliases   bool
	hasFallbacks bool
	hasPriority  bool
}

func (r *RoutingConfig) Init() {
	if r == nil {
		return
	}
	r.hasAliases = len(r.Aliases) > 0
	r.hasFallbacks = len(r.Fallbacks) > 0
	r.hasPriority = len(r.ProviderPriority) > 0
}

// ResolveModelAlias returns the canonical model name for the given input.
// If no alias is defined, returns the original model name.
func (r *RoutingConfig) ResolveModelAlias(model string) string {
	if r == nil || !r.hasAliases {
		return model
	}
	if alias, ok := r.Aliases[model]; ok {
		return alias
	}
	return model
}

// GetFallbackChain returns the fallback models for the given model.
// Returns nil if no fallbacks are defined.
func (r *RoutingConfig) GetFallbackChain(model string) []string {
	if r == nil || !r.hasFallbacks {
		return nil
	}
	return r.Fallbacks[model]
}

// HasProviderPriority returns true if provider priority is configured.
func (r *RoutingConfig) HasProviderPriority() bool {
	return r != nil && r.hasPriority
}

// NewDefaultConfig creates a new Config with sensible defaults.
// This allows the server to run without a config file using OAuth credentials only.
func NewDefaultConfig() *Config {
	return &Config{
		Port:                   8317,
		AuthDir:                "$XDG_CONFIG_HOME/llm-mux/auth",
		DisableAuth:            true,
		RequestRetry:           3,
		MaxRetryInterval:       30,
		StreamTimeout:          300,
		QuotaWindow:            300,
		UseCanonicalTranslator: true,
		Usage: UsageConfig{
			DSN:           "", // Disabled by default, user must opt-in
			BatchSize:     100,
			FlushInterval: "5s",
			RetentionDays: 30,
		},
		QuotaExceeded: QuotaExceeded{
			SwitchProject:      true,
			SwitchPreviewModel: true,
		},
		AmpCode: AmpCode{
			RestrictManagementToLocalhost: true,
		},
	}
}

// GenerateDefaultConfigYAML generates a minimal default config YAML from NewDefaultConfig().
// This replaces the embedded config.example.yaml file.
func GenerateDefaultConfigYAML() []byte {
	cfg := NewDefaultConfig()
	data, err := yaml.Marshal(cfg)
	if err != nil {
		// Fallback to minimal config if marshaling fails
		return []byte("port: 8317\nauth-dir: \"$XDG_CONFIG_HOME/llm-mux/auth\"\ndisable-auth: true\n")
	}
	return data
}

// LoadConfig reads a YAML configuration file from the given path,
// unmarshals it into a Config struct, applies environment variable overrides,
// and returns it.
// Parameters:
//   - configFile: The path to the YAML configuration file
//
// Returns:
//   - *Config: The loaded configuration
//   - error: An error if the configuration could not be loaded
func LoadConfig(configFile string) (*Config, error) {
	return LoadConfigOptional(configFile, false)
}

// LoadConfigOptional reads YAML from configFile.
// If optional is true and the file is missing, it returns a default Config.
// If optional is true and the file is empty or invalid, it returns a default Config.
func LoadConfigOptional(configFile string, optional bool) (*Config, error) {
	// Read the entire configuration file into memory.
	data, err := os.ReadFile(configFile)
	if err != nil {
		if optional {
			if os.IsNotExist(err) || errors.Is(err, syscall.EISDIR) {
				// Missing and optional: return default config (works without config file).
				return NewDefaultConfig(), nil
			}
		}
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// In cloud deploy mode (optional=true), if file is empty or contains only whitespace, return default config.
	if optional && len(data) == 0 {
		return NewDefaultConfig(), nil
	}

	// Unmarshal the YAML data into the Config struct.
	// Start with defaults so absent keys keep sensible values.
	cfg := *NewDefaultConfig()
	if err = yaml.Unmarshal(data, &cfg); err != nil {
		if optional {
			// In cloud deploy mode, if YAML parsing fails, return default config instead of error.
			return NewDefaultConfig(), nil
		}
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Sync request authentication providers with inline API keys for backwards compatibility.
	syncInlineAccessProvider(&cfg)

	cfg.Providers = SanitizeProviders(cfg.Providers)

	// Normalize OAuth provider model exclusion map.
	cfg.OAuthExcludedModels = NormalizeOAuthExcludedModels(cfg.OAuthExcludedModels)

	cfg.Routing.Init()

	// Return the populated configuration struct.
	return &cfg, nil
}

func syncInlineAccessProvider(cfg *Config) {
	if cfg == nil {
		return
	}
	if len(cfg.APIKeys) == 0 {
		if provider := cfg.ConfigAPIKeyProvider(); provider != nil && len(provider.APIKeys) > 0 {
			cfg.APIKeys = append([]string(nil), provider.APIKeys...)
		}
	}
	cfg.Access.Providers = nil
}

// NormalizeHeaders trims header keys and values and removes empty pairs.
func NormalizeHeaders(headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	clean := make(map[string]string, len(headers))
	for k, v := range headers {
		key := strings.TrimSpace(k)
		val := strings.TrimSpace(v)
		if key == "" || val == "" {
			continue
		}
		clean[key] = val
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

// NormalizeExcludedModels trims, lowercases, and deduplicates model exclusion patterns.
// It preserves the order of first occurrences and drops empty entries.
func NormalizeExcludedModels(models []string) []string {
	if len(models) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(models))
	out := make([]string, 0, len(models))
	for _, raw := range models {
		trimmed := strings.ToLower(strings.TrimSpace(raw))
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// NormalizeOAuthExcludedModels cleans provider -> excluded models mappings by normalizing provider keys
// and applying model exclusion normalization to each entry.
func NormalizeOAuthExcludedModels(entries map[string][]string) map[string][]string {
	if len(entries) == 0 {
		return nil
	}
	out := make(map[string][]string, len(entries))
	for provider, models := range entries {
		key := strings.ToLower(strings.TrimSpace(provider))
		if key == "" {
			continue
		}
		normalized := NormalizeExcludedModels(models)
		if len(normalized) == 0 {
			continue
		}
		out[key] = normalized
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// SaveConfigPreserveComments writes the config back to YAML while preserving existing comments
// and key ordering by loading the original file into a yaml.Node tree and updating values in-place.
func SaveConfigPreserveComments(configFile string, cfg *Config) error {
	persistCfg := sanitizeConfigForPersist(cfg)
	// Load original YAML as a node tree to preserve comments and ordering.
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}

	var original yaml.Node
	if err = yaml.Unmarshal(data, &original); err != nil {
		return err
	}
	if original.Kind != yaml.DocumentNode || len(original.Content) == 0 {
		return fmt.Errorf("invalid yaml document structure")
	}
	if original.Content[0] == nil || original.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("expected root mapping node")
	}

	// Marshal the current cfg to YAML, then unmarshal to a yaml.Node we can merge from.
	rendered, err := yaml.Marshal(persistCfg)
	if err != nil {
		return err
	}
	var generated yaml.Node
	if err = yaml.Unmarshal(rendered, &generated); err != nil {
		return err
	}
	if generated.Kind != yaml.DocumentNode || len(generated.Content) == 0 || generated.Content[0] == nil {
		return fmt.Errorf("invalid generated yaml structure")
	}
	if generated.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("expected generated root mapping node")
	}

	pruneMappingToGeneratedKeys(original.Content[0], generated.Content[0], "oauth-excluded-models")

	// Merge generated into original in-place, preserving comments/order of existing nodes.
	mergeMappingPreserve(original.Content[0], generated.Content[0])
	normalizeCollectionNodeStyles(original.Content[0])

	// Write back.
	f, err := os.Create(configFile)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	buf := ir.GetBuffer()
	defer ir.PutBuffer(buf)
	enc := yaml.NewEncoder(buf)
	enc.SetIndent(2)
	if err = enc.Encode(&original); err != nil {
		_ = enc.Close()
		return err
	}
	if err = enc.Close(); err != nil {
		return err
	}
	data = NormalizeCommentIndentation(buf.Bytes())
	_, err = f.Write(data)
	return err
}

func sanitizeConfigForPersist(cfg *Config) *Config {
	if cfg == nil {
		return nil
	}
	clone := *cfg
	clone.SDKConfig = cfg.SDKConfig
	clone.SDKConfig.Access = AccessConfig{}
	return &clone
}

// SaveConfigPreserveCommentsUpdateNestedScalar updates a nested scalar key path like ["a","b"]
// while preserving comments and positions.
func SaveConfigPreserveCommentsUpdateNestedScalar(configFile string, path []string, value string) error {
	data, err := os.ReadFile(configFile)
	if err != nil {
		return err
	}
	var root yaml.Node
	if err = yaml.Unmarshal(data, &root); err != nil {
		return err
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return fmt.Errorf("invalid yaml document structure")
	}
	node := root.Content[0]
	// descend mapping nodes following path
	for i, key := range path {
		if i == len(path)-1 {
			v := getOrCreateMapValue(node, key)
			v.Kind = yaml.ScalarNode
			v.Tag = "!!str"
			v.Value = value
		} else {
			next := getOrCreateMapValue(node, key)
			if next.Kind != yaml.MappingNode {
				next.Kind = yaml.MappingNode
				next.Tag = "!!map"
			}
			node = next
		}
	}
	f, err := os.Create(configFile)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	buf := ir.GetBuffer()
	defer ir.PutBuffer(buf)
	enc := yaml.NewEncoder(buf)
	enc.SetIndent(2)
	if err = enc.Encode(&root); err != nil {
		_ = enc.Close()
		return err
	}
	if err = enc.Close(); err != nil {
		return err
	}
	data = NormalizeCommentIndentation(buf.Bytes())
	_, err = f.Write(data)
	return err
}

// NormalizeCommentIndentation removes indentation from standalone YAML comment lines to keep them left aligned.
func NormalizeCommentIndentation(data []byte) []byte {
	lines := bytes.Split(data, []byte("\n"))
	changed := false
	for i, line := range lines {
		trimmed := bytes.TrimLeft(line, " \t")
		if len(trimmed) == 0 || trimmed[0] != '#' {
			continue
		}
		if len(trimmed) == len(line) {
			continue
		}
		lines[i] = append([]byte(nil), trimmed...)
		changed = true
	}
	if !changed {
		return data
	}
	return bytes.Join(lines, []byte("\n"))
}

// getOrCreateMapValue finds the value node for a given key in a mapping node.
// If not found, it appends a new key/value pair and returns the new value node.
func getOrCreateMapValue(mapNode *yaml.Node, key string) *yaml.Node {
	if mapNode.Kind != yaml.MappingNode {
		mapNode.Kind = yaml.MappingNode
		mapNode.Tag = "!!map"
		mapNode.Content = nil
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		k := mapNode.Content[i]
		if k.Value == key {
			return mapNode.Content[i+1]
		}
	}
	// append new key/value
	mapNode.Content = append(mapNode.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key})
	val := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: ""}
	mapNode.Content = append(mapNode.Content, val)
	return val
}

// mergeMappingPreserve merges keys from src into dst mapping node while preserving
// key order and comments of existing keys in dst. Unknown keys from src are appended
// to dst at the end, copying their node structure from src.
func mergeMappingPreserve(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	if dst.Kind != yaml.MappingNode || src.Kind != yaml.MappingNode {
		// If kinds do not match, prefer replacing dst with src semantics in-place
		// but keep dst node object to preserve any attached comments at the parent level.
		copyNodeShallow(dst, src)
		return
	}
	// Build a lookup of existing keys in dst
	for i := 0; i+1 < len(src.Content); i += 2 {
		sk := src.Content[i]
		sv := src.Content[i+1]
		idx := findMapKeyIndex(dst, sk.Value)
		if idx >= 0 {
			// Merge into existing value node
			dv := dst.Content[idx+1]
			mergeNodePreserve(dv, sv)
		} else {
			if shouldSkipEmptyCollectionOnPersist(sk.Value, sv) {
				continue
			}
			// Append new key/value pair by deep-copying from src
			dst.Content = append(dst.Content, deepCopyNode(sk), deepCopyNode(sv))
		}
	}
}

// mergeNodePreserve merges src into dst for scalars, mappings and sequences while
// reusing destination nodes to keep comments and anchors. For sequences, it updates
// in-place by index.
func mergeNodePreserve(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	switch src.Kind {
	case yaml.MappingNode:
		if dst.Kind != yaml.MappingNode {
			copyNodeShallow(dst, src)
		}
		mergeMappingPreserve(dst, src)
	case yaml.SequenceNode:
		// Preserve explicit null style if dst was null and src is empty sequence
		if dst.Kind == yaml.ScalarNode && dst.Tag == "!!null" && len(src.Content) == 0 {
			// Keep as null to preserve original style
			return
		}
		if dst.Kind != yaml.SequenceNode {
			dst.Kind = yaml.SequenceNode
			dst.Tag = "!!seq"
			dst.Content = nil
		}
		reorderSequenceForMerge(dst, src)
		// Update elements in place
		minContent := len(dst.Content)
		if len(src.Content) < minContent {
			minContent = len(src.Content)
		}
		for i := range minContent {
			if dst.Content[i] == nil {
				dst.Content[i] = deepCopyNode(src.Content[i])
				continue
			}
			mergeNodePreserve(dst.Content[i], src.Content[i])
			if dst.Content[i] != nil && src.Content[i] != nil &&
				dst.Content[i].Kind == yaml.MappingNode && src.Content[i].Kind == yaml.MappingNode {
				pruneMissingMapKeys(dst.Content[i], src.Content[i])
			}
		}
		// Append any extra items from src
		for i := len(dst.Content); i < len(src.Content); i++ {
			dst.Content = append(dst.Content, deepCopyNode(src.Content[i]))
		}
		// Truncate if dst has extra items not in src
		if len(src.Content) < len(dst.Content) {
			dst.Content = dst.Content[:len(src.Content)]
		}
	case yaml.ScalarNode, yaml.AliasNode:
		// For scalars, update Tag and Value but keep Style from dst to preserve quoting
		dst.Kind = src.Kind
		dst.Tag = src.Tag
		dst.Value = src.Value
		// Keep dst.Style as-is intentionally
	case 0:
		// Unknown/empty kind; do nothing
	default:
		// Fallback: replace shallowly
		copyNodeShallow(dst, src)
	}
}

// findMapKeyIndex returns the index of key node in dst mapping (index of key, not value).
// Returns -1 when not found.
func findMapKeyIndex(mapNode *yaml.Node, key string) int {
	if mapNode == nil || mapNode.Kind != yaml.MappingNode {
		return -1
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		if mapNode.Content[i] != nil && mapNode.Content[i].Value == key {
			return i
		}
	}
	return -1
}

func shouldSkipEmptyCollectionOnPersist(key string, node *yaml.Node) bool {
	switch key {
	case "generative-language-api-key",
		"gemini-api-key",
		"vertex-api-key",
		"claude-api-key",
		"codex-api-key",
		"openai-compatibility":
		return isEmptyCollectionNode(node)
	default:
		return false
	}
}

func isEmptyCollectionNode(node *yaml.Node) bool {
	if node == nil {
		return true
	}
	switch node.Kind {
	case yaml.SequenceNode:
		return len(node.Content) == 0
	case yaml.ScalarNode:
		return node.Tag == "!!null"
	default:
		return false
	}
}

// deepCopyNode creates a deep copy of a yaml.Node graph.
func deepCopyNode(n *yaml.Node) *yaml.Node {
	if n == nil {
		return nil
	}
	cp := *n
	if len(n.Content) > 0 {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i := range n.Content {
			cp.Content[i] = deepCopyNode(n.Content[i])
		}
	}
	return &cp
}

// copyNodeShallow copies type/tag/value and resets content to match src, but
// keeps the same destination node pointer to preserve parent relations/comments.
func copyNodeShallow(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	dst.Kind = src.Kind
	dst.Tag = src.Tag
	dst.Value = src.Value
	// Replace content with deep copy from src
	if len(src.Content) > 0 {
		dst.Content = make([]*yaml.Node, len(src.Content))
		for i := range src.Content {
			dst.Content[i] = deepCopyNode(src.Content[i])
		}
	} else {
		dst.Content = nil
	}
}

func reorderSequenceForMerge(dst, src *yaml.Node) {
	if dst == nil || src == nil {
		return
	}
	if len(dst.Content) == 0 {
		return
	}
	if len(src.Content) == 0 {
		return
	}
	original := append([]*yaml.Node(nil), dst.Content...)
	used := make([]bool, len(original))
	ordered := make([]*yaml.Node, len(src.Content))
	for i := range src.Content {
		if idx := matchSequenceElement(original, used, src.Content[i]); idx >= 0 {
			ordered[i] = original[idx]
			used[idx] = true
		}
	}
	dst.Content = ordered
}

func matchSequenceElement(original []*yaml.Node, used []bool, target *yaml.Node) int {
	if target == nil {
		return -1
	}
	switch target.Kind {
	case yaml.MappingNode:
		id := sequenceElementIdentity(target)
		if id != "" {
			for i := range original {
				if used[i] || original[i] == nil || original[i].Kind != yaml.MappingNode {
					continue
				}
				if sequenceElementIdentity(original[i]) == id {
					return i
				}
			}
		}
	case yaml.ScalarNode:
		val := strings.TrimSpace(target.Value)
		if val != "" {
			for i := range original {
				if used[i] || original[i] == nil || original[i].Kind != yaml.ScalarNode {
					continue
				}
				if strings.TrimSpace(original[i].Value) == val {
					return i
				}
			}
		}
	default:
	}
	// Fallback to structural equality to preserve nodes lacking explicit identifiers.
	for i := range original {
		if used[i] || original[i] == nil {
			continue
		}
		if nodesStructurallyEqual(original[i], target) {
			return i
		}
	}
	return -1
}

func sequenceElementIdentity(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	identityKeys := []string{"id", "name", "alias", "api-key", "api_key", "apikey", "key", "provider", "model"}
	for _, k := range identityKeys {
		if v := mappingScalarValue(node, k); v != "" {
			return k + "=" + v
		}
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		if keyNode == nil || valNode == nil || valNode.Kind != yaml.ScalarNode {
			continue
		}
		val := strings.TrimSpace(valNode.Value)
		if val != "" {
			return strings.ToLower(strings.TrimSpace(keyNode.Value)) + "=" + val
		}
	}
	return ""
}

func mappingScalarValue(node *yaml.Node, key string) string {
	if node == nil || node.Kind != yaml.MappingNode {
		return ""
	}
	lowerKey := strings.ToLower(key)
	for i := 0; i+1 < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		if keyNode == nil || valNode == nil || valNode.Kind != yaml.ScalarNode {
			continue
		}
		if strings.ToLower(strings.TrimSpace(keyNode.Value)) == lowerKey {
			return strings.TrimSpace(valNode.Value)
		}
	}
	return ""
}

func nodesStructurallyEqual(a, b *yaml.Node) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case yaml.MappingNode:
		if len(a.Content) != len(b.Content) {
			return false
		}
		for i := 0; i+1 < len(a.Content); i += 2 {
			if !nodesStructurallyEqual(a.Content[i], b.Content[i]) {
				return false
			}
			if !nodesStructurallyEqual(a.Content[i+1], b.Content[i+1]) {
				return false
			}
		}
		return true
	case yaml.SequenceNode:
		if len(a.Content) != len(b.Content) {
			return false
		}
		for i := range a.Content {
			if !nodesStructurallyEqual(a.Content[i], b.Content[i]) {
				return false
			}
		}
		return true
	case yaml.ScalarNode:
		return strings.TrimSpace(a.Value) == strings.TrimSpace(b.Value)
	case yaml.AliasNode:
		return nodesStructurallyEqual(a.Alias, b.Alias)
	default:
		return strings.TrimSpace(a.Value) == strings.TrimSpace(b.Value)
	}
}

func removeMapKey(mapNode *yaml.Node, key string) {
	if mapNode == nil || mapNode.Kind != yaml.MappingNode || key == "" {
		return
	}
	for i := 0; i+1 < len(mapNode.Content); i += 2 {
		if mapNode.Content[i] != nil && mapNode.Content[i].Value == key {
			mapNode.Content = append(mapNode.Content[:i], mapNode.Content[i+2:]...)
			return
		}
	}
}

func pruneMappingToGeneratedKeys(dstRoot, srcRoot *yaml.Node, key string) {
	if key == "" || dstRoot == nil || srcRoot == nil {
		return
	}
	if dstRoot.Kind != yaml.MappingNode || srcRoot.Kind != yaml.MappingNode {
		return
	}
	dstIdx := findMapKeyIndex(dstRoot, key)
	if dstIdx < 0 || dstIdx+1 >= len(dstRoot.Content) {
		return
	}
	srcIdx := findMapKeyIndex(srcRoot, key)
	if srcIdx < 0 {
		removeMapKey(dstRoot, key)
		return
	}
	if srcIdx+1 >= len(srcRoot.Content) {
		return
	}
	srcVal := srcRoot.Content[srcIdx+1]
	dstVal := dstRoot.Content[dstIdx+1]
	if srcVal == nil {
		dstRoot.Content[dstIdx+1] = nil
		return
	}
	if srcVal.Kind != yaml.MappingNode {
		dstRoot.Content[dstIdx+1] = deepCopyNode(srcVal)
		return
	}
	if dstVal == nil || dstVal.Kind != yaml.MappingNode {
		dstRoot.Content[dstIdx+1] = deepCopyNode(srcVal)
		return
	}
	pruneMissingMapKeys(dstVal, srcVal)
}

func pruneMissingMapKeys(dstMap, srcMap *yaml.Node) {
	if dstMap == nil || srcMap == nil || dstMap.Kind != yaml.MappingNode || srcMap.Kind != yaml.MappingNode {
		return
	}
	keep := make(map[string]struct{}, len(srcMap.Content)/2)
	for i := 0; i+1 < len(srcMap.Content); i += 2 {
		keyNode := srcMap.Content[i]
		if keyNode == nil {
			continue
		}
		key := strings.TrimSpace(keyNode.Value)
		if key == "" {
			continue
		}
		keep[key] = struct{}{}
	}
	for i := 0; i+1 < len(dstMap.Content); {
		keyNode := dstMap.Content[i]
		if keyNode == nil {
			i += 2
			continue
		}
		key := strings.TrimSpace(keyNode.Value)
		if _, ok := keep[key]; !ok {
			dstMap.Content = append(dstMap.Content[:i], dstMap.Content[i+2:]...)
			continue
		}
		i += 2
	}
}

// normalizeCollectionNodeStyles forces YAML collections to use block notation, keeping
// lists and maps readable. Empty sequences retain flow style ([]) so empty list markers
// remain compact.
func normalizeCollectionNodeStyles(node *yaml.Node) {
	if node == nil {
		return
	}
	switch node.Kind {
	case yaml.MappingNode:
		node.Style = 0
		for i := range node.Content {
			normalizeCollectionNodeStyles(node.Content[i])
		}
	case yaml.SequenceNode:
		if len(node.Content) == 0 {
			node.Style = yaml.FlowStyle
		} else {
			node.Style = 0
		}
		for i := range node.Content {
			normalizeCollectionNodeStyles(node.Content[i])
		}
	default:
		// Scalars keep their existing style to preserve quoting
	}
}
