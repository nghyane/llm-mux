// Package translator provides the IR translation layer for format conversion.
// This file implements a registry pattern for translator discovery and dispatch.
package translator

import (
	"fmt"
	"sync"

	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

// ToIRParser parses input format into IR (Intermediate Representation).
// Implementations handle format-specific parsing (OpenAI, Claude, Gemini, etc.)
type ToIRParser interface {
	// Parse converts raw JSON payload to UnifiedChatRequest.
	Parse(payload []byte) (*ir.UnifiedChatRequest, error)

	// ParseResponse converts raw JSON response to Messages and Usage.
	ParseResponse(payload []byte) ([]ir.Message, *ir.Usage, error)

	// ParseChunk converts a streaming chunk to UnifiedEvents.
	ParseChunk(payload []byte) ([]ir.UnifiedEvent, error)

	// Format returns the format identifier this parser handles.
	Format() string
}

// FromIRConverter converts IR to provider-specific format.
// Implementations handle provider-specific payload generation.
type FromIRConverter interface {
	// ConvertRequest converts UnifiedChatRequest to provider payload.
	ConvertRequest(req *ir.UnifiedChatRequest) ([]byte, error)

	// ToResponse converts Messages and Usage to provider response format.
	ToResponse(messages []ir.Message, usage *ir.Usage, model string) ([]byte, error)

	// ToChunk converts UnifiedEvent to provider streaming chunk format.
	ToChunk(event ir.UnifiedEvent, model string) ([]byte, error)

	// Provider returns the provider identifier this converter handles.
	Provider() string
}

// Registry manages translator registration and lookup.
type Registry struct {
	mu         sync.RWMutex
	toIR       map[string]ToIRParser      // format string -> parser
	fromIR     map[string]FromIRConverter // provider string -> converter
	formatToIR map[provider.Format]ToIRParser
}

var getGlobalRegistry = sync.OnceValue(func() *Registry {
	return &Registry{
		toIR:       make(map[string]ToIRParser),
		fromIR:     make(map[string]FromIRConverter),
		formatToIR: make(map[provider.Format]ToIRParser),
	}
})

func GetRegistry() *Registry {
	return getGlobalRegistry()
}

// RegisterToIR registers a ToIR parser for a format.
// This is typically called from init() functions in to_ir/*.go files.
func (r *Registry) RegisterToIR(format string, parser ToIRParser) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.toIR[format] = parser
	r.formatToIR[provider.FromString(format)] = parser
}

// RegisterFromIR registers a FromIR converter for a provider.
// This is typically called from init() functions in from_ir/*.go files.
func (r *Registry) RegisterFromIR(providerName string, converter FromIRConverter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fromIR[providerName] = converter
}

// GetToIR returns the parser for the given format string.
func (r *Registry) GetToIR(format string) (ToIRParser, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.toIR[format]
	return p, ok
}

// GetToIRByFormat returns the parser for the given provider.Format.
func (r *Registry) GetToIRByFormat(format provider.Format) (ToIRParser, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.formatToIR[format]
	return p, ok
}

// GetFromIR returns the converter for the given provider.
func (r *Registry) GetFromIR(providerName string) (FromIRConverter, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.fromIR[providerName]
	return c, ok
}

// MustGetToIR returns the parser for the given format, panicking if not found.
func (r *Registry) MustGetToIR(format string) ToIRParser {
	p, ok := r.GetToIR(format)
	if !ok {
		panic(fmt.Sprintf("translator: no ToIR parser registered for format %q", format))
	}
	return p
}

// MustGetFromIR returns the converter for the given provider, panicking if not found.
func (r *Registry) MustGetFromIR(providerName string) FromIRConverter {
	c, ok := r.GetFromIR(providerName)
	if !ok {
		panic(fmt.Sprintf("translator: no FromIR converter registered for provider %q", providerName))
	}
	return c
}

// ListToIRFormats returns all registered ToIR format names.
func (r *Registry) ListToIRFormats() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	formats := make([]string, 0, len(r.toIR))
	for f := range r.toIR {
		formats = append(formats, f)
	}
	return formats
}

// ListFromIRProviders returns all registered FromIR provider names.
func (r *Registry) ListFromIRProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	providers := make([]string, 0, len(r.fromIR))
	for p := range r.fromIR {
		providers = append(providers, p)
	}
	return providers
}

// Package-level convenience functions

// RegisterToIR registers a ToIR parser in the global registry.
func RegisterToIR(format string, parser ToIRParser) {
	GetRegistry().RegisterToIR(format, parser)
}

// RegisterFromIR registers a FromIR converter in the global registry.
func RegisterFromIR(providerName string, converter FromIRConverter) {
	GetRegistry().RegisterFromIR(providerName, converter)
}

// ParseRequest parses a request payload using the appropriate ToIR parser.
func ParseRequest(format string, payload []byte) (*ir.UnifiedChatRequest, error) {
	parser, ok := GetRegistry().GetToIR(format)
	if !ok {
		return nil, fmt.Errorf("unsupported source format: %s", format)
	}
	return parser.Parse(payload)
}

// ConvertRequest converts an IR request using the appropriate FromIR converter.
func ConvertRequest(providerName string, req *ir.UnifiedChatRequest) ([]byte, error) {
	converter, ok := GetRegistry().GetFromIR(providerName)
	if !ok {
		return nil, fmt.Errorf("unsupported target provider: %s", providerName)
	}
	return converter.ConvertRequest(req)
}
