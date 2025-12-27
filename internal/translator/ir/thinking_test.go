package ir

import "testing"

func TestIsGemini3(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"gemini-3-pro-preview", true},
		{"gemini-3-flash-preview", true},
		{"gemini-3-pro-image-preview", true},
		{"Gemini-3-Pro-Preview", true}, // case insensitive
		{"gemini-2.5-flash", false},
		{"gemini-2.5-pro", false},
		{"claude-sonnet-4", false},
		{"gpt-4o", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := IsGemini3(tt.model)
			if result != tt.expected {
				t.Errorf("IsGemini3(%q) = %v, want %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestIsGemini3Flash(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"gemini-3-flash-preview", true},
		{"Gemini-3-Flash-Preview", true}, // case insensitive
		{"gemini-3-pro-preview", false},
		{"gemini-3-pro-image-preview", false},
		{"gemini-2.5-flash", false}, // not Gemini 3
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := IsGemini3Flash(tt.model)
			if result != tt.expected {
				t.Errorf("IsGemini3Flash(%q) = %v, want %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestEffortToThinkingLevel(t *testing.T) {
	tests := []struct {
		model    string
		effort   string
		expected ThinkingLevel
	}{
		// Gemini 3 Flash - supports all levels
		{"gemini-3-flash-preview", "none", ThinkingLevelMinimal},
		{"gemini-3-flash-preview", "minimal", ThinkingLevelMinimal},
		{"gemini-3-flash-preview", "low", ThinkingLevelLow},
		{"gemini-3-flash-preview", "medium", ThinkingLevelHigh}, // Per Gemini 3 docs: medium -> HIGH
		{"gemini-3-flash-preview", "high", ThinkingLevelHigh},
		{"gemini-3-flash-preview", "xhigh", ThinkingLevelHigh},
		{"gemini-3-flash-preview", "", ThinkingLevelMedium}, // default for Flash

		// Gemini 3 Pro - only LOW and HIGH
		{"gemini-3-pro-preview", "none", ThinkingLevelLow},    // no MINIMAL for Pro
		{"gemini-3-pro-preview", "minimal", ThinkingLevelLow}, // no MINIMAL for Pro
		{"gemini-3-pro-preview", "low", ThinkingLevelLow},
		{"gemini-3-pro-preview", "medium", ThinkingLevelHigh}, // Per Gemini 3 docs: medium -> HIGH
		{"gemini-3-pro-preview", "high", ThinkingLevelHigh},
		{"gemini-3-pro-preview", "xhigh", ThinkingLevelHigh},
		{"gemini-3-pro-preview", "", ThinkingLevelHigh}, // default for Pro
	}

	for _, tt := range tests {
		name := tt.model + "/" + tt.effort
		if tt.effort == "" {
			name = tt.model + "/default"
		}
		t.Run(name, func(t *testing.T) {
			result := EffortToThinkingLevel(tt.model, tt.effort)
			if result != tt.expected {
				t.Errorf("EffortToThinkingLevel(%q, %q) = %q, want %q",
					tt.model, tt.effort, result, tt.expected)
			}
		})
	}
}

func TestBudgetToThinkingLevel(t *testing.T) {
	tests := []struct {
		model    string
		budget   int
		expected ThinkingLevel
	}{
		// Flash model
		{"gemini-3-flash-preview", 0, ThinkingLevelMinimal},
		{"gemini-3-flash-preview", 128, ThinkingLevelMinimal},
		{"gemini-3-flash-preview", 129, ThinkingLevelLow},
		{"gemini-3-flash-preview", 1024, ThinkingLevelLow},
		{"gemini-3-flash-preview", 1025, ThinkingLevelMedium},
		{"gemini-3-flash-preview", 8192, ThinkingLevelMedium},
		{"gemini-3-flash-preview", 8193, ThinkingLevelHigh},
		{"gemini-3-flash-preview", 32768, ThinkingLevelHigh},

		// Pro model (no MINIMAL or MEDIUM)
		{"gemini-3-pro-preview", 0, ThinkingLevelLow},
		{"gemini-3-pro-preview", 128, ThinkingLevelLow},
		{"gemini-3-pro-preview", 1024, ThinkingLevelLow},
		{"gemini-3-pro-preview", 1025, ThinkingLevelHigh},
		{"gemini-3-pro-preview", 8192, ThinkingLevelHigh},
		{"gemini-3-pro-preview", 32768, ThinkingLevelHigh},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := BudgetToThinkingLevel(tt.model, tt.budget)
			if result != tt.expected {
				t.Errorf("BudgetToThinkingLevel(%q, %d) = %q, want %q",
					tt.model, tt.budget, result, tt.expected)
			}
		})
	}
}

func TestDefaultThinkingLevel(t *testing.T) {
	tests := []struct {
		model    string
		expected ThinkingLevel
	}{
		{"gemini-3-flash-preview", ThinkingLevelMedium},
		{"gemini-3-pro-preview", ThinkingLevelHigh},
		{"gemini-3-pro-image-preview", ThinkingLevelHigh},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := DefaultThinkingLevel(tt.model)
			if result != tt.expected {
				t.Errorf("DefaultThinkingLevel(%q) = %q, want %q", tt.model, result, tt.expected)
			}
		})
	}
}

func TestIsValidThoughtSignature(t *testing.T) {
	tests := []struct {
		name     string
		sig      []byte
		expected bool
	}{
		{"valid signature", []byte("abc123signature"), true},
		{"empty", []byte{}, false},
		{"nil", nil, false},
		{"undefined", []byte("undefined"), false},
		{"[undefined]", []byte("[undefined]"), false},
		{"null", []byte("null"), false},
		{"[null]", []byte("[null]"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := IsValidThoughtSignature(tt.sig)
			if result != tt.expected {
				t.Errorf("IsValidThoughtSignature(%q) = %v, want %v", tt.sig, result, tt.expected)
			}
		})
	}
}

func TestIsClaude(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"claude-3-sonnet", true},
		{"claude-sonnet-4", true},
		{"Claude-Opus-3", true}, // case insensitive
		{"anthropic.claude-sonnet", true},
		{"gemini-2.5-pro", false},
		{"gpt-4o", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := IsClaude(tt.model)
			if result != tt.expected {
				t.Errorf("IsClaude(%q) = %v, want %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestIsThinkingModel(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"claude-3-5-sonnet-20241022-thinking", true},
		{"claude-thinking-v1", true},
		{"Thinking-Model", true}, // case insensitive
		{"claude-3-sonnet", false},
		{"gemini-2.5-pro", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := IsThinkingModel(tt.model)
			if result != tt.expected {
				t.Errorf("IsThinkingModel(%q) = %v, want %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestModelMayHaveThinking(t *testing.T) {
	tests := []struct {
		model    string
		expected bool
	}{
		{"gemini-2.5-flash", true},
		{"gemini-2.5-pro", true},
		{"gemini-3-flash-preview", true},
		{"claude-3-sonnet", true},
		{"claude-thinking-v1", true},
		{"gpt-4o", false},
		{"gpt-5", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			result := ModelMayHaveThinking(tt.model)
			if result != tt.expected {
				t.Errorf("ModelMayHaveThinking(%q) = %v, want %v", tt.model, result, tt.expected)
			}
		})
	}
}

func TestEffortToBudget(t *testing.T) {
	tests := []struct {
		effort         string
		expectedBudget int
		expectedIncl   bool
	}{
		{"none", 0, false},
		{"minimal", 128, true},
		{"low", 1024, true},
		{"medium", 8192, true},
		{"high", 32768, true},
		{"xhigh", 65536, true},
		{"", -1, true}, // default
		{"unknown", -1, true},
	}

	for _, tt := range tests {
		t.Run(tt.effort, func(t *testing.T) {
			budget, include := EffortToBudget(tt.effort)
			if budget != tt.expectedBudget || include != tt.expectedIncl {
				t.Errorf("EffortToBudget(%q) = (%d, %v), want (%d, %v)",
					tt.effort, budget, include, tt.expectedBudget, tt.expectedIncl)
			}
		})
	}
}

func TestBudgetToEffort(t *testing.T) {
	tests := []struct {
		budget         int
		defaultForZero string
		expected       string
	}{
		{0, "none", "none"},
		{-1, "none", "none"},
		{100, "none", "low"},
		{1024, "none", "low"},
		{2000, "none", "medium"},
		{8192, "none", "medium"},
		{10000, "none", "high"},
		{32768, "none", "high"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			result := BudgetToEffort(tt.budget, tt.defaultForZero)
			if result != tt.expected {
				t.Errorf("BudgetToEffort(%d, %q) = %q, want %q",
					tt.budget, tt.defaultForZero, result, tt.expected)
			}
		})
	}
}

// ==================== ContentType Tests ====================

func TestContentTypeRedactedThinking(t *testing.T) {
	// Verify ContentTypeRedactedThinking constant is defined correctly
	if ContentTypeRedactedThinking != "redacted_thinking" {
		t.Errorf("ContentTypeRedactedThinking = %q, want %q", ContentTypeRedactedThinking, "redacted_thinking")
	}
}

func TestContentPartRedactedData(t *testing.T) {
	// Test that ContentPart can store redacted thinking data
	encryptedData := "base64-encoded-encrypted-thinking-data"
	part := ContentPart{
		Type:         ContentTypeRedactedThinking,
		RedactedData: encryptedData,
	}

	if part.Type != ContentTypeRedactedThinking {
		t.Errorf("ContentPart.Type = %q, want %q", part.Type, ContentTypeRedactedThinking)
	}
	if part.RedactedData != encryptedData {
		t.Errorf("ContentPart.RedactedData = %q, want %q", part.RedactedData, encryptedData)
	}
}

// ==================== UnifiedEvent RedactedData Tests ====================

func TestUnifiedEventRedactedData(t *testing.T) {
	// Test that UnifiedEvent can carry redacted_thinking data in streaming
	encryptedData := "encrypted-thinking-content"
	event := UnifiedEvent{
		Type:         EventTypeReasoning,
		RedactedData: encryptedData,
	}

	if event.RedactedData != encryptedData {
		t.Errorf("UnifiedEvent.RedactedData = %q, want %q", event.RedactedData, encryptedData)
	}
}

// ==================== ThinkingConfig Tests ====================

func TestThinkingConfig_DefaultValues(t *testing.T) {
	// Test default ThinkingConfig creation
	config := &ThinkingConfig{}

	if config.IncludeThoughts != false {
		t.Error("ThinkingConfig.IncludeThoughts should default to false")
	}
	if config.ThinkingBudget != nil {
		t.Error("ThinkingConfig.ThinkingBudget should default to nil")
	}
	if config.ThinkingLevel != "" {
		t.Errorf("ThinkingConfig.ThinkingLevel should default to empty, got %q", config.ThinkingLevel)
	}
}

func TestThinkingConfig_WithBudget(t *testing.T) {
	budget := int32(8192)
	config := &ThinkingConfig{
		IncludeThoughts: true,
		ThinkingBudget:  &budget,
		ThinkingLevel:   ThinkingLevelMedium,
	}

	if config.ThinkingBudget == nil {
		t.Fatal("ThinkingConfig.ThinkingBudget should not be nil")
	}
	if *config.ThinkingBudget != budget {
		t.Errorf("ThinkingConfig.ThinkingBudget = %d, want %d", *config.ThinkingBudget, budget)
	}
}

func TestThinkingConfig_Effort(t *testing.T) {
	tests := []struct {
		effort       ReasoningEffort
		expectedName string
	}{
		{ReasoningEffortNone, "none"},
		{ReasoningEffortMinimal, "minimal"},
		{ReasoningEffortLow, "low"},
		{ReasoningEffortMedium, "medium"},
		{ReasoningEffortHigh, "high"},
		{ReasoningEffortXHigh, "xhigh"},
	}

	for _, tt := range tests {
		t.Run(tt.expectedName, func(t *testing.T) {
			config := &ThinkingConfig{
				Effort: tt.effort,
			}
			if string(config.Effort) != tt.expectedName {
				t.Errorf("ThinkingConfig.Effort = %q, want %q", config.Effort, tt.expectedName)
			}
		})
	}
}

// ==================== ThinkingLevel Tests ====================

func TestThinkingLevelConstants(t *testing.T) {
	tests := []struct {
		level    ThinkingLevel
		expected string
	}{
		{ThinkingLevelUnspecified, "THINKING_LEVEL_UNSPECIFIED"},
		{ThinkingLevelMinimal, "MINIMAL"},
		{ThinkingLevelLow, "LOW"},
		{ThinkingLevelMedium, "MEDIUM"},
		{ThinkingLevelHigh, "HIGH"},
		{ThinkingLevelOff, "OFF"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			if string(tt.level) != tt.expected {
				t.Errorf("ThinkingLevel constant = %q, want %q", tt.level, tt.expected)
			}
		})
	}
}

func TestThinkingLevelToBudget(t *testing.T) {
	tests := []struct {
		level    ThinkingLevel
		expected int
	}{
		{ThinkingLevelMinimal, 128},
		{ThinkingLevelLow, 1024},
		{ThinkingLevelMedium, 8192},
		{ThinkingLevelHigh, 32768},
		{ThinkingLevelUnspecified, 8192}, // default
		{"UNKNOWN", 8192},                // unknown defaults to medium
	}

	for _, tt := range tests {
		t.Run(string(tt.level), func(t *testing.T) {
			result := ThinkingLevelToBudget(tt.level)
			if result != tt.expected {
				t.Errorf("ThinkingLevelToBudget(%q) = %d, want %d", tt.level, result, tt.expected)
			}
		})
	}
}
