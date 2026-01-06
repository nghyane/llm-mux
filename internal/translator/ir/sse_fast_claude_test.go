package ir

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildClaudeToolCallBlockStartSSE(t *testing.T) {
	result := BuildClaudeToolCallBlockStartSSE(0, "toolu_abc123", "get_weather")

	lines := strings.Split(string(result), "\n")
	if len(lines) < 2 {
		t.Fatal("Expected event + data lines")
	}

	if !strings.HasPrefix(lines[0], "event: content_block_start") {
		t.Errorf("Expected 'event: content_block_start', got '%s'", lines[0])
	}

	dataLine := strings.TrimPrefix(lines[1], "data: ")
	var blockStart map[string]any
	if err := json.Unmarshal([]byte(dataLine), &blockStart); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if blockStart["type"] != "content_block_start" {
		t.Errorf("Expected type='content_block_start', got '%v'", blockStart["type"])
	}
	if blockStart["index"] != float64(0) {
		t.Errorf("Expected index=0, got '%v'", blockStart["index"])
	}

	contentBlock := blockStart["content_block"].(map[string]any)
	if contentBlock["type"] != "tool_use" {
		t.Errorf("Expected content_block.type='tool_use', got '%v'", contentBlock["type"])
	}
	if contentBlock["id"] != "toolu_abc123" {
		t.Errorf("Expected content_block.id='toolu_abc123', got '%v'", contentBlock["id"])
	}
	if contentBlock["name"] != "get_weather" {
		t.Errorf("Expected content_block.name='get_weather', got '%v'", contentBlock["name"])
	}
	if contentBlock["input"] == nil {
		t.Error("Expected content_block.input to exist")
	}
}

func TestBuildClaudeToolCallInputDeltaSSE(t *testing.T) {
	result := BuildClaudeToolCallInputDeltaSSE(0, `{"location":"Tokyo"}`)

	lines := strings.Split(string(result), "\n")
	if len(lines) < 2 {
		t.Fatal("Expected event + data lines")
	}

	if !strings.HasPrefix(lines[0], "event: content_block_delta") {
		t.Errorf("Expected 'event: content_block_delta', got '%s'", lines[0])
	}

	dataLine := strings.TrimPrefix(lines[1], "data: ")
	var inputDelta map[string]any
	if err := json.Unmarshal([]byte(dataLine), &inputDelta); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if inputDelta["type"] != "content_block_delta" {
		t.Errorf("Expected type='content_block_delta', got '%v'", inputDelta["type"])
	}
	if inputDelta["index"] != float64(0) {
		t.Errorf("Expected index=0, got '%v'", inputDelta["index"])
	}

	delta := inputDelta["delta"].(map[string]any)
	if delta["type"] != "input_json_delta" {
		t.Errorf("Expected delta.type='input_json_delta', got '%v'", delta["type"])
	}
	if delta["partial_json"] != `{"location":"Tokyo"}` {
		t.Errorf("Expected delta.partial_json, got '%v'", delta["partial_json"])
	}
}

func TestClaudeToolCallSSEPoolReuse(t *testing.T) {
	for i := 0; i < 100; i++ {
		result1 := BuildClaudeToolCallBlockStartSSE(i, "toolu_"+string(rune('a'+i%26)), "func_name")
		if !strings.Contains(string(result1), "content_block_start") {
			t.Error("Invalid block start output")
		}

		result2 := BuildClaudeToolCallInputDeltaSSE(i, `{"key":"value"}`)
		if !strings.Contains(string(result2), "input_json_delta") {
			t.Error("Invalid input delta output")
		}
	}
}
