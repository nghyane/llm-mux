package ir

import (
	"testing"
)

func TestCleanJsonSchemaForGemini_RemovesExclusiveMinMax(t *testing.T) {
	// Test case based on the bug:
	// Unknown name "exclusiveMaximum" at 'request.tools[0].function_declarations[43].parameters.properties[0].value'
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"page": map[string]any{
				"type":             "integer",
				"description":      "Page number for pagination",
				"minimum":          1,
				"maximum":          10,
				"exclusiveMinimum": 0,
				"exclusiveMaximum": 11,
			},
			"limit": map[string]any{
				"type":             "number",
				"exclusiveMinimum": 0,
				"exclusiveMaximum": 100,
			},
		},
		"required": []any{"page"},
	}

	result := CleanJsonSchemaForGemini(schema)

	// Check top-level structure preserved
	if result["type"] != "object" {
		t.Errorf("Expected type 'object', got %v", result["type"])
	}

	props := result["properties"].(map[string]any)

	// Check 'page' property
	pageProp := props["page"].(map[string]any)
	if _, exists := pageProp["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from 'page' property")
	}
	if _, exists := pageProp["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from 'page' property")
	}
	if _, exists := pageProp["minimum"]; exists {
		t.Error("minimum should be removed from 'page' property")
	}
	if _, exists := pageProp["maximum"]; exists {
		t.Error("maximum should be removed from 'page' property")
	}
	// Description should be preserved
	if pageProp["description"] != "Page number for pagination" {
		t.Errorf("description should be preserved, got %v", pageProp["description"])
	}

	// Check 'limit' property
	limitProp := props["limit"].(map[string]any)
	if _, exists := limitProp["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from 'limit' property")
	}
	if _, exists := limitProp["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from 'limit' property")
	}

	// Required should be preserved
	if result["required"] == nil {
		t.Error("required field should be preserved")
	}
}

func TestCleanJsonSchemaForGemini_DeeplyNestedProperties(t *testing.T) {
	// Test deeply nested schema (3 levels)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"config": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"settings": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"value": map[string]any{
								"type":             "integer",
								"exclusiveMinimum": 0,
								"exclusiveMaximum": 100,
							},
						},
					},
				},
			},
		},
	}

	result := CleanJsonSchemaForGemini(schema)

	// Navigate to deeply nested property
	props := result["properties"].(map[string]any)
	config := props["config"].(map[string]any)
	configProps := config["properties"].(map[string]any)
	settings := configProps["settings"].(map[string]any)
	settingsProps := settings["properties"].(map[string]any)
	value := settingsProps["value"].(map[string]any)

	if _, exists := value["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from deeply nested 'value' property")
	}
	if _, exists := value["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from deeply nested 'value' property")
	}
	if value["type"] != "integer" {
		t.Errorf("type should be preserved, got %v", value["type"])
	}
}

func TestCleanJsonSchemaForGemini_ArrayItems(t *testing.T) {
	// Test schema with array items containing unsupported fields
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"numbers": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type":             "integer",
					"exclusiveMinimum": 0,
					"exclusiveMaximum": 100,
					"minimum":          1,
					"maximum":          99,
				},
				"minItems": 1,
				"maxItems": 10,
			},
		},
	}

	result := CleanJsonSchemaForGemini(schema)

	props := result["properties"].(map[string]any)
	numbers := props["numbers"].(map[string]any)

	// Check array-level fields removed
	if _, exists := numbers["minItems"]; exists {
		t.Error("minItems should be removed")
	}
	if _, exists := numbers["maxItems"]; exists {
		t.Error("maxItems should be removed")
	}

	// Check items schema cleaned
	items := numbers["items"].(map[string]any)
	if _, exists := items["exclusiveMinimum"]; exists {
		t.Error("exclusiveMinimum should be removed from items")
	}
	if _, exists := items["exclusiveMaximum"]; exists {
		t.Error("exclusiveMaximum should be removed from items")
	}
	if _, exists := items["minimum"]; exists {
		t.Error("minimum should be removed from items")
	}
	if _, exists := items["maximum"]; exists {
		t.Error("maximum should be removed from items")
	}
}

func TestCleanJsonSchemaForGemini_PreservesValidFields(t *testing.T) {
	schema := map[string]any{
		"type":        "object",
		"description": "A test schema",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "The name field",
			},
			"age": map[string]any{
				"type":        "integer",
				"description": "The age field",
			},
		},
		"required": []any{"name"},
	}

	result := CleanJsonSchemaForGemini(schema)

	if result["type"] != "object" {
		t.Errorf("type should be preserved")
	}
	if result["description"] != "A test schema" {
		t.Errorf("description should be preserved")
	}
	if result["required"] == nil {
		t.Errorf("required should be preserved")
	}

	props := result["properties"].(map[string]any)
	nameProp := props["name"].(map[string]any)
	if nameProp["type"] != "string" {
		t.Errorf("name.type should be preserved")
	}
	if nameProp["description"] != "The name field" {
		t.Errorf("name.description should be preserved")
	}
}

func TestCleanJsonSchemaForGemini_NilSchema(t *testing.T) {
	result := CleanJsonSchemaForGemini(nil)
	if result != nil {
		t.Errorf("Expected nil for nil input, got %v", result)
	}
}

func TestCleanJsonSchemaForGemini_RemovesRefAndDefs(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"$ref": "#/$defs/MyType",
		"$defs": map[string]any{
			"MyType": map[string]any{
				"type": "string",
			},
		},
		"properties": map[string]any{
			"field": map[string]any{
				"$ref": "#/$defs/MyType",
			},
		},
	}

	result := CleanJsonSchemaForGemini(schema)

	if _, exists := result["$ref"]; exists {
		t.Error("$ref should be removed at top level")
	}
	if _, exists := result["$defs"]; exists {
		t.Error("$defs should be removed at top level")
	}

	props := result["properties"].(map[string]any)
	field := props["field"].(map[string]any)
	if _, exists := field["$ref"]; exists {
		t.Error("$ref should be removed from nested property")
	}
}

// ==================== FinishReason Mapping Tests ====================

func TestMapGeminiFinishReason(t *testing.T) {
	tests := []struct {
		input    string
		expected FinishReason
	}{
		// Standard stop reasons
		{"STOP", FinishReasonStop},
		{"stop", FinishReasonStop}, // case insensitive
		{"FINISH_REASON_UNSPECIFIED", FinishReasonStop},
		{"UNKNOWN", FinishReasonStop},

		// Token limit
		{"MAX_TOKENS", FinishReasonMaxTokens},
		{"LENGTH", FinishReasonMaxTokens},

		// Tool calls
		{"TOOL_CALLS", FinishReasonToolCalls},
		{"FUNCTION_CALL", FinishReasonToolCalls},
		{"MALFORMED_FUNCTION_CALL", FinishReasonToolCalls},

		// Content filtering
		{"SAFETY", FinishReasonContentFilter},
		{"OTHER", FinishReasonContentFilter},

		// New Gemini 2025 values
		{"BLOCKLIST", FinishReasonBlocklist},
		{"PROHIBITED_CONTENT", FinishReasonProhibitedContent},
		{"SPII", FinishReasonSPII},
		{"IMAGE_SAFETY", FinishReasonImageSafety},
		{"RECITATION", FinishReasonRecitation},

		// Unknown values
		{"SOME_FUTURE_REASON", FinishReasonUnknown},
		{"", FinishReasonUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := MapGeminiFinishReason(tt.input)
			if result != tt.expected {
				t.Errorf("MapGeminiFinishReason(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMapClaudeFinishReason(t *testing.T) {
	tests := []struct {
		input    string
		expected FinishReason
	}{
		{"end_turn", FinishReasonStop},
		{"stop_sequence", FinishReasonStopSequence},
		{"max_tokens", FinishReasonMaxTokens},
		{"tool_use", FinishReasonToolCalls},
		{"unknown_value", FinishReasonUnknown},
		{"", FinishReasonUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := MapClaudeFinishReason(tt.input)
			if result != tt.expected {
				t.Errorf("MapClaudeFinishReason(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMapOpenAIFinishReason(t *testing.T) {
	tests := []struct {
		input    string
		expected FinishReason
	}{
		{"stop", FinishReasonStop},
		{"length", FinishReasonMaxTokens},
		{"tool_calls", FinishReasonToolCalls},
		{"function_call", FinishReasonToolCalls},
		{"content_filter", FinishReasonContentFilter},
		{"unknown_value", FinishReasonUnknown},
		{"", FinishReasonUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := MapOpenAIFinishReason(tt.input)
			if result != tt.expected {
				t.Errorf("MapOpenAIFinishReason(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMapFinishReasonToOpenAI(t *testing.T) {
	tests := []struct {
		input    FinishReason
		expected string
	}{
		{FinishReasonStop, "stop"},
		{FinishReasonStopSequence, "stop"},
		{FinishReasonMaxTokens, "length"},
		{FinishReasonToolCalls, "tool_calls"},
		{FinishReasonContentFilter, "content_filter"},
		{FinishReasonBlocklist, "content_filter"},
		{FinishReasonProhibitedContent, "content_filter"},
		{FinishReasonSPII, "content_filter"},
		{FinishReasonImageSafety, "content_filter"},
		{FinishReasonRecitation, "content_filter"},
		{FinishReasonError, "error"},
		{FinishReasonUnknown, "stop"},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			result := MapFinishReasonToOpenAI(tt.input)
			if result != tt.expected {
				t.Errorf("MapFinishReasonToOpenAI(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestMapFinishReasonToClaude(t *testing.T) {
	tests := []struct {
		input    FinishReason
		expected string
	}{
		{FinishReasonStop, "end_turn"},
		{FinishReasonMaxTokens, "max_tokens"},
		{FinishReasonToolCalls, "tool_use"},
		{FinishReasonStopSequence, "stop_sequence"},
		{FinishReasonContentFilter, "end_turn"},
		{FinishReasonBlocklist, "end_turn"},
		{FinishReasonProhibitedContent, "end_turn"},
		{FinishReasonSPII, "end_turn"},
		{FinishReasonImageSafety, "end_turn"},
		{FinishReasonRecitation, "end_turn"},
		{FinishReasonUnknown, "end_turn"},
	}

	for _, tt := range tests {
		t.Run(string(tt.input), func(t *testing.T) {
			result := MapFinishReasonToClaude(tt.input)
			if result != tt.expected {
				t.Errorf("MapFinishReasonToClaude(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ==================== FinishReason Round-Trip Tests ====================

func TestFinishReasonRoundTrip_OpenAI(t *testing.T) {
	// Test that common OpenAI reasons round-trip correctly
	tests := []string{"stop", "length", "tool_calls", "content_filter"}

	for _, openaiReason := range tests {
		t.Run(openaiReason, func(t *testing.T) {
			ir := MapOpenAIFinishReason(openaiReason)
			backToOpenAI := MapFinishReasonToOpenAI(ir)
			if backToOpenAI != openaiReason {
				t.Errorf("Round-trip failed: %q -> %q -> %q", openaiReason, ir, backToOpenAI)
			}
		})
	}
}

func TestFinishReasonRoundTrip_Claude(t *testing.T) {
	// Test that common Claude reasons round-trip correctly
	tests := []string{"end_turn", "max_tokens", "tool_use", "stop_sequence"}

	for _, claudeReason := range tests {
		t.Run(claudeReason, func(t *testing.T) {
			ir := MapClaudeFinishReason(claudeReason)
			backToClaude := MapFinishReasonToClaude(ir)
			if backToClaude != claudeReason {
				t.Errorf("Round-trip failed: %q -> %q -> %q", claudeReason, ir, backToClaude)
			}
		})
	}
}

// ==================== ParseMalformedFunctionCall Tests ====================

func TestParseMalformedFunctionCall(t *testing.T) {
	tests := []struct {
		name         string
		input        string
		expectedName string
		expectedArgs string
		expectedOK   bool
	}{
		{
			name:         "standard format",
			input:        `call:default_api:my_tool{"arg1":"value1"}`,
			expectedName: "my_tool",
			expectedArgs: `{"arg1":"value1"}`,
			expectedOK:   true,
		},
		{
			name:         "with error prefix",
			input:        `error: call:default_api:run_command{"cmd":"ls"}`,
			expectedName: "run_command",
			expectedArgs: `{"cmd":"ls"}`,
			expectedOK:   true,
		},
		{
			name:         "empty args",
			input:        `call:default_api:empty_tool{}`,
			expectedName: "empty_tool",
			expectedArgs: `{}`,
			expectedOK:   true,
		},
		{
			name:         "no call prefix",
			input:        `some random message`,
			expectedName: "",
			expectedArgs: "",
			expectedOK:   false,
		},
		{
			name:         "no args",
			input:        `call:default_api:no_args`,
			expectedName: "",
			expectedArgs: "",
			expectedOK:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args, ok := ParseMalformedFunctionCall(tt.input)
			if ok != tt.expectedOK {
				t.Errorf("ParseMalformedFunctionCall(%q) ok = %v, want %v", tt.input, ok, tt.expectedOK)
			}
			if name != tt.expectedName {
				t.Errorf("ParseMalformedFunctionCall(%q) name = %q, want %q", tt.input, name, tt.expectedName)
			}
			if ok && args != tt.expectedArgs {
				t.Errorf("ParseMalformedFunctionCall(%q) args = %q, want %q", tt.input, args, tt.expectedArgs)
			}
		})
	}
}

// ==================== MapStandardRole Tests ====================

func TestMapStandardRole(t *testing.T) {
	tests := []struct {
		input    string
		expected Role
	}{
		{"system", RoleSystem},
		{"developer", RoleSystem},
		{"assistant", RoleAssistant},
		{"tool", RoleTool},
		{"user", RoleUser},
		{"human", RoleUser}, // Unknown defaults to user
		{"", RoleUser},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := MapStandardRole(tt.input)
			if result != tt.expected {
				t.Errorf("MapStandardRole(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

// ==================== SanitizeText Tests ====================

func TestSanitizeText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"empty", "", ""},
		{"normal text", "Hello, World!", "Hello, World!"},
		{"with tabs and newlines", "Line1\n\tLine2", "Line1\n\tLine2"},
		{"with null byte", "Hello\x00World", "HelloWorld"},
		{"with control chars", "Hello\x01\x02World", "HelloWorld"},
		{"unicode", "Hello 世界 🌍", "Hello 世界 🌍"},
		{"carriage return", "Line1\r\nLine2", "Line1\r\nLine2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SanitizeText(tt.input)
			if result != tt.expected {
				t.Errorf("SanitizeText(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}
