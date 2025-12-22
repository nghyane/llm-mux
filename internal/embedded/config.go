package embedded

import _ "embed"

// DefaultConfigTemplate contains the embedded config.example.yaml template.
// To update: copy config.example.yaml from project root to this directory.
//go:embed config.example.yaml
var DefaultConfigTemplate []byte
