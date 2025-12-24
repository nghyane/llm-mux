package ir

import "testing"

func TestFromKiroToolID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"tooluse_abc123", "call_abc123"},
		{"tooluse_", "call_"},
		{"call_abc123", "call_abc123"},   // already standard
		{"toolu_abc123", "toolu_abc123"}, // Claude format unchanged
		{"abc123", "abc123"},             // no prefix
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := FromKiroToolID(tt.input)
			if result != tt.expected {
				t.Errorf("FromKiroToolID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFromClaudeToolID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"toolu_abc123", "call_abc123"},
		{"toolu_", "call_"},
		{"call_abc123", "call_abc123"},       // already standard
		{"tooluse_abc123", "tooluse_abc123"}, // Kiro format unchanged
		{"abc123", "abc123"},                 // no prefix
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := FromClaudeToolID(tt.input)
			if result != tt.expected {
				t.Errorf("FromClaudeToolID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestToKiroToolID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"call_abc123", "tooluse_abc123"},
		{"call_", "tooluse_"},
		{"tooluse_abc123", "tooluse_abc123"}, // already Kiro format
		{"toolu_abc123", "toolu_abc123"},     // Claude format unchanged
		{"abc123", "abc123"},                 // no prefix
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ToKiroToolID(tt.input)
			if result != tt.expected {
				t.Errorf("ToKiroToolID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestToClaudeToolID(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"call_abc123", "toolu_abc123"},
		{"call_", "toolu_"},
		{"toolu_abc123", "toolu_abc123"},           // already Claude format (fast path)
		{"tooluse_abc123", "toolu_tooluse_abc123"}, // Kiro format gets prefix added
		{"abc123", "toolu_abc123"},                 // no prefix gets toolu_ added
		{"", "toolu_"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ToClaudeToolID(tt.input)
			if result != tt.expected {
				t.Errorf("ToClaudeToolID(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestGenToolCallID(t *testing.T) {
	id := GenToolCallID()
	if len(id) < 5 || id[:5] != "call_" {
		t.Errorf("GenToolCallID() = %q, should start with 'call_'", id)
	}
	// Should be unique
	id2 := GenToolCallID()
	if id == id2 {
		t.Errorf("GenToolCallID() returned same ID twice: %q", id)
	}
}

func TestGenClaudeToolCallID(t *testing.T) {
	id := GenClaudeToolCallID()
	if len(id) < 6 || id[:6] != "toolu_" {
		t.Errorf("GenClaudeToolCallID() = %q, should start with 'toolu_'", id)
	}
	// Should be unique
	id2 := GenClaudeToolCallID()
	if id == id2 {
		t.Errorf("GenClaudeToolCallID() returned same ID twice: %q", id)
	}
}
