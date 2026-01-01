package config

type VertexCompatKey struct {
	APIKey   string              `yaml:"api-key" json:"api-key"`
	BaseURL  string              `yaml:"base-url,omitempty" json:"base-url,omitempty"`
	ProxyURL string              `yaml:"proxy-url,omitempty" json:"proxy-url,omitempty"`
	Headers  map[string]string   `yaml:"headers,omitempty" json:"headers,omitempty"`
	Models   []VertexCompatModel `yaml:"models,omitempty" json:"models,omitempty"`
}

type VertexCompatModel struct {
	Name  string `yaml:"name" json:"name"`
	Alias string `yaml:"alias" json:"alias"`
}
