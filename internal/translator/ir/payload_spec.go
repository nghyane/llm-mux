// Package ir provides payload specification and validation for provider-specific payloads.
// This file defines whitelist-based field specifications for Vertex AI providers.
//
// Architecture:
//
//	PayloadSpec (interface)
//	    ├── VertexGeminiSpec - For native Gemini models (gemini-2.x, gemini-3.x)
//	    └── VertexClaudeSpec - For Claude models via Vertex/Antigravity
//
// The specs define:
//   - Allowed fields at each nesting level
//   - Required vs optional fields
//   - Whether null values are permitted
//   - Nested field specifications for complex objects
package ir

// FieldType represents the expected JSON type of a field.
type FieldType string

const (
	FieldTypeString  FieldType = "string"
	FieldTypeNumber  FieldType = "number"
	FieldTypeBoolean FieldType = "boolean"
	FieldTypeObject  FieldType = "object"
	FieldTypeArray   FieldType = "array"
	FieldTypeAny     FieldType = "any" // Accepts any type
)

// FieldSpec defines the specification for a single field.
type FieldSpec struct {
	Type     FieldType             // Expected type
	Required bool                  // Whether field is required
	Nullable bool                  // Whether null is a valid value
	Children map[string]*FieldSpec // Nested fields for object types
	Items    *FieldSpec            // Item spec for array types
}

// PayloadSpec defines the complete specification for a payload.
type PayloadSpec struct {
	Name   string                // Human-readable name (e.g., "VertexGemini")
	Fields map[string]*FieldSpec // Top-level allowed fields
}

// IsAllowed checks if a field is in the whitelist.
func (s *PayloadSpec) IsAllowed(fieldName string) bool {
	if s == nil || s.Fields == nil {
		return false
	}
	_, ok := s.Fields[fieldName]
	return ok
}

// GetFieldSpec returns the spec for a field, or nil if not allowed.
func (s *PayloadSpec) GetFieldSpec(fieldName string) *FieldSpec {
	if s == nil || s.Fields == nil {
		return nil
	}
	return s.Fields[fieldName]
}

// Helper to create a simple field spec
func stringField(required bool) *FieldSpec {
	return &FieldSpec{Type: FieldTypeString, Required: required}
}

func numberField(required bool) *FieldSpec {
	return &FieldSpec{Type: FieldTypeNumber, Required: required}
}

func boolField(required bool) *FieldSpec {
	return &FieldSpec{Type: FieldTypeBoolean, Required: required}
}

func objectField(required bool, children map[string]*FieldSpec) *FieldSpec {
	return &FieldSpec{Type: FieldTypeObject, Required: required, Children: children}
}

func arrayField(required bool, items *FieldSpec) *FieldSpec {
	return &FieldSpec{Type: FieldTypeArray, Required: required, Items: items}
}

func anyField(required bool) *FieldSpec {
	return &FieldSpec{Type: FieldTypeAny, Required: required}
}

// ============================================================================
// Gemini CLI Request Spec (wrapped format for Antigravity)
// Format: { "project": "", "model": "", "request": { ... gemini payload ... } }
// ============================================================================

// geminiContentsPartSpec defines allowed fields in content parts.
var geminiContentsPartSpec = map[string]*FieldSpec{
	"text":             stringField(false),
	"thought":          boolField(false),
	"thoughtSignature": stringField(false),
	"inlineData":       objectField(false, map[string]*FieldSpec{"mimeType": stringField(true), "data": stringField(true)}),
	"fileData":         objectField(false, map[string]*FieldSpec{"mimeType": stringField(false), "fileUri": stringField(true)}),
	"functionCall":     objectField(false, map[string]*FieldSpec{"name": stringField(true), "args": anyField(false), "id": stringField(false)}),
	"functionResponse": objectField(false, map[string]*FieldSpec{"name": stringField(true), "id": stringField(false), "response": anyField(false)}),
	"executableCode":   objectField(false, map[string]*FieldSpec{"language": stringField(false), "code": stringField(true)}),
	"codeExecutionResult": objectField(false, map[string]*FieldSpec{
		"outcome": stringField(true),
		"output":  stringField(false),
	}),
}

// geminiContentsSpec defines the contents array structure.
var geminiContentsSpec = &FieldSpec{
	Type:     FieldTypeArray,
	Required: true,
	Items: objectField(false, map[string]*FieldSpec{
		"role":  stringField(true),
		"parts": arrayField(true, objectField(false, geminiContentsPartSpec)),
	}),
}

// geminiGenerationConfigSpec defines generationConfig fields.
var geminiGenerationConfigSpec = map[string]*FieldSpec{
	"temperature":        numberField(false),
	"topP":               numberField(false),
	"topK":               numberField(false),
	"maxOutputTokens":    numberField(false),
	"candidateCount":     numberField(false),
	"stopSequences":      arrayField(false, stringField(false)),
	"presencePenalty":    numberField(false),
	"frequencyPenalty":   numberField(false),
	"responseMimeType":   stringField(false),
	"responseJsonSchema": anyField(false),
	"responseLogprobs":   boolField(false),
	"logprobs":           numberField(false),
	"seed":               numberField(false),
	"responseModalities": arrayField(false, stringField(false)),
	"thinkingConfig": objectField(false, map[string]*FieldSpec{
		"thinkingBudget":  numberField(false),
		"includeThoughts": boolField(false),
		"thinkingLevel":   stringField(false),
	}),
	"imageConfig": objectField(false, map[string]*FieldSpec{
		"aspectRatio": stringField(false),
		"imageSize":   stringField(false),
	}),
}

// geminiSafetySettingsSpec defines safetySettings array items.
var geminiSafetySettingsSpec = objectField(false, map[string]*FieldSpec{
	"category":  stringField(true),
	"threshold": stringField(true),
})

// geminiFunctionDeclarationSpec defines a single function declaration.
var geminiFunctionDeclarationSpec = map[string]*FieldSpec{
	"name":                 stringField(true),
	"description":          stringField(false),
	"parameters":           anyField(false), // JSON Schema
	"parametersJsonSchema": anyField(false), // Alternative field name
}

// geminiToolsSpec defines the tools array structure.
var geminiToolsSpec = &FieldSpec{
	Type:     FieldTypeArray,
	Required: false,
	Items: objectField(false, map[string]*FieldSpec{
		"functionDeclarations": arrayField(false, objectField(false, geminiFunctionDeclarationSpec)),
		"googleSearch":         anyField(false),
		"googleSearchRetrieval": objectField(false, map[string]*FieldSpec{
			"dynamicRetrievalConfig": objectField(false, map[string]*FieldSpec{
				"mode":             stringField(false),
				"dynamicThreshold": numberField(false),
			}),
		}),
		"codeExecution": anyField(false),
		"urlContext":    anyField(false),
		"fileSearch":    anyField(false),
	}),
}

// geminiToolConfigSpec defines toolConfig structure.
var geminiToolConfigSpec = map[string]*FieldSpec{
	"functionCallingConfig": objectField(false, map[string]*FieldSpec{
		"mode":                        stringField(false),
		"allowedFunctionNames":        arrayField(false, stringField(false)),
		"streamFunctionCallArguments": boolField(false),
	}),
}

// geminiSystemInstructionSpec defines systemInstruction structure.
var geminiSystemInstructionSpec = map[string]*FieldSpec{
	"role":  stringField(false),
	"parts": arrayField(false, objectField(false, map[string]*FieldSpec{"text": stringField(false)})),
}

// VertexGeminiRequestSpec is the whitelist for Gemini models on Vertex/Antigravity.
// This is the inner "request" object in Gemini CLI format.
var VertexGeminiRequestSpec = &PayloadSpec{
	Name: "VertexGeminiRequest",
	Fields: map[string]*FieldSpec{
		"contents":          geminiContentsSpec,
		"systemInstruction": objectField(false, geminiSystemInstructionSpec),
		"generationConfig":  objectField(false, geminiGenerationConfigSpec),
		"safetySettings":    arrayField(false, geminiSafetySettingsSpec),
		"tools":             geminiToolsSpec,
		"toolConfig":        objectField(false, geminiToolConfigSpec),
		"cachedContent":     stringField(false),
		"labels":            anyField(false),
		// Session management (added by geminiToAntigravity)
		"sessionId": stringField(false),
	},
}

// ============================================================================
// Claude on Vertex/Antigravity Spec
// Claude via Antigravity uses Gemini format BUT with restrictions
// ============================================================================

// claudeGenerationConfigSpec - subset of Gemini config that Claude supports.
var claudeGenerationConfigSpec = map[string]*FieldSpec{
	"temperature":     numberField(false),
	"topP":            numberField(false),
	"topK":            numberField(false),
	"maxOutputTokens": numberField(false),
	"stopSequences":   arrayField(false, stringField(false)),
	// Claude on Antigravity supports thinking via thinkingConfig
	"thinkingConfig": objectField(false, map[string]*FieldSpec{
		"thinkingBudget":  numberField(false),
		"includeThoughts": boolField(false),
	}),
	// These are NOT supported by Claude on Vertex:
	// - candidateCount
	// - presencePenalty
	// - frequencyPenalty
	// - responseMimeType
	// - responseJsonSchema
	// - responseLogprobs
	// - logprobs
	// - seed
	// - responseModalities (limited support)
	// - imageConfig
}

// claudeToolsSpec - Claude uses Gemini function format but stricter schema.
var claudeToolsSpec = &FieldSpec{
	Type:     FieldTypeArray,
	Required: false,
	Items: objectField(false, map[string]*FieldSpec{
		"functionDeclarations": arrayField(false, objectField(false, map[string]*FieldSpec{
			"name":        stringField(true),
			"description": stringField(false),
			"parameters":  anyField(false), // Cleaned by CleanToolsForAntigravityClaude
		})),
		// Claude does NOT support these Gemini-specific tools:
		// - googleSearch
		// - googleSearchRetrieval
		// - codeExecution
		// - urlContext
		// - fileSearch
	}),
}

// VertexClaudeRequestSpec is the whitelist for Claude models on Vertex/Antigravity.
var VertexClaudeRequestSpec = &PayloadSpec{
	Name: "VertexClaudeRequest",
	Fields: map[string]*FieldSpec{
		"contents":          geminiContentsSpec,
		"systemInstruction": objectField(false, geminiSystemInstructionSpec),
		"generationConfig":  objectField(false, claudeGenerationConfigSpec),
		"tools":             claudeToolsSpec,
		// toolConfig with mode:"VALIDATED" is NOT supported by Claude on Antigravity
		// Session management
		"sessionId": stringField(false),
		// These are explicitly NOT allowed for Claude:
		// - safetySettings (Gemini-specific)
		// - cachedContent (Gemini caching format)
		// - labels (Gemini-specific)
		// - toolConfig (mode:"VALIDATED" not supported)
	},
}

// ============================================================================
// Antigravity Wrapper Spec
// The outer envelope for both Gemini and Claude requests
// ============================================================================

// AntigravityWrapperSpec is the whitelist for the Antigravity envelope.
var AntigravityWrapperSpec = &PayloadSpec{
	Name: "AntigravityWrapper",
	Fields: map[string]*FieldSpec{
		"model":     stringField(true),
		"userAgent": stringField(false),
		"project":   stringField(false),
		"requestId": stringField(false),
		"request":   objectField(true, nil), // Inner request validated separately
	},
}

// GetRequestSpecForModel returns the appropriate request spec based on model name.
func GetRequestSpecForModel(modelName string) *PayloadSpec {
	if isClaudeModel(modelName) {
		return VertexClaudeRequestSpec
	}
	return VertexGeminiRequestSpec
}

// isClaudeModel checks if a model name indicates a Claude model.
func isClaudeModel(modelName string) bool {
	// Check for common Claude model patterns
	for _, pattern := range []string{"claude", "anthropic"} {
		for i := 0; i <= len(modelName)-len(pattern); i++ {
			match := true
			for j := 0; j < len(pattern); j++ {
				c := modelName[i+j]
				// Case-insensitive comparison
				if c != pattern[j] && c != pattern[j]-32 && c != pattern[j]+32 {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
	}
	return false
}
