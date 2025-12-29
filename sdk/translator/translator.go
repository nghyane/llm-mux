// Package translator provides format definitions for request/response translation.
package translator

// Format identifies a request/response schema used inside the proxy.
type Format string

// FromString converts an arbitrary identifier to a translator format.
func FromString(v string) Format {
	return Format(v)
}

// String returns the raw schema identifier.
func (f Format) String() string {
	return string(f)
}

// Common format identifiers.
const (
	FormatOpenAI      Format = "openai"
	FormatClaude      Format = "claude"
	FormatGemini      Format = "gemini"
	FormatGeminiCLI   Format = "gemini-cli"
	FormatCodex       Format = "codex"
	FormatAntigravity Format = "antigravity"
)
