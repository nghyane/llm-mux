package executor

import (
	"net/http"

	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/util"
)

// applyGeminiHeaders applies custom headers from auth attributes for Gemini requests.
func applyGeminiHeaders(req *http.Request, auth *provider.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
}
