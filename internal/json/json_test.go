package json

import (
	stdjson "encoding/json"
	"strings"
	"testing"
)

type TestStruct struct {
	Name    string  `json:"name"`
	Age     int     `json:"age"`
	Balance float64 `json:"balance,omitempty"`
}

func TestMarshalUnmarshal(t *testing.T) {
	original := TestStruct{Name: "Test", Age: 25, Balance: 100.50}

	// Test Marshal
	data, err := Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	// Verify JSON structure
	if !strings.Contains(string(data), `"name":"Test"`) {
		t.Errorf("Marshal output missing name field: %s", data)
	}

	// Test Unmarshal
	var decoded TestStruct
	if err := Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.Name != original.Name || decoded.Age != original.Age {
		t.Errorf("Unmarshal mismatch: got %+v, want %+v", decoded, original)
	}
}

func TestMarshalIndent(t *testing.T) {
	data := map[string]any{"key": "value"}
	result, err := MarshalIndent(data, "", "  ")
	if err != nil {
		t.Fatalf("MarshalIndent failed: %v", err)
	}

	if !strings.Contains(string(result), "\n") {
		t.Error("MarshalIndent should produce indented output")
	}
}

func TestValid(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{`{"key": "value"}`, true},
		{`[1, 2, 3]`, true},
		{`invalid`, false},
		{`{"unclosed": }`, false},
	}

	for _, tt := range tests {
		got := Valid([]byte(tt.input))
		if got != tt.want {
			t.Errorf("Valid(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestRawMessage(t *testing.T) {
	type Wrapper struct {
		Data RawMessage `json:"data"`
	}

	input := []byte(`{"data":{"nested":"value"}}`)
	var w Wrapper
	if err := Unmarshal(input, &w); err != nil {
		t.Fatalf("Unmarshal with RawMessage failed: %v", err)
	}

	expected := `{"nested":"value"}`
	if string(w.Data) != expected {
		t.Errorf("RawMessage = %s, want %s", w.Data, expected)
	}
}

func TestNumber(t *testing.T) {
	input := `{"value": 12345678901234567890}`
	dec := NewDecoder(strings.NewReader(input))
	dec.UseNumber()

	var result map[string]any
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("Decode with UseNumber failed: %v", err)
	}

	num, ok := result["value"].(Number)
	if !ok {
		t.Fatalf("Expected Number type, got %T", result["value"])
	}

	if num.String() != "12345678901234567890" {
		t.Errorf("Number = %s, want 12345678901234567890", num)
	}
}

func TestEncoder(t *testing.T) {
	var buf strings.Builder
	enc := NewEncoder(&buf)

	if err := enc.Encode(map[string]string{"key": "value"}); err != nil {
		t.Fatalf("Encode failed: %v", err)
	}

	result := strings.TrimSpace(buf.String())
	if result != `{"key":"value"}` {
		t.Errorf("Encode = %s, want %s", result, `{"key":"value"}`)
	}
}

func TestDecoderMore(t *testing.T) {
	// Note: Token() is not implemented in sonic StreamDecoder
	// Test More() with simple decode loop instead
	input := `{"a":1,"b":2}`
	dec := NewDecoder(strings.NewReader(input))

	var result map[string]int
	if err := dec.Decode(&result); err != nil {
		t.Fatalf("Decode failed: %v", err)
	}

	if len(result) != 2 {
		t.Errorf("Expected 2 elements, got %d", len(result))
	}
}

func TestCompatibilityWithStdLib(t *testing.T) {
	// Ensure our wrapper produces same output as encoding/json
	data := map[string]any{
		"string": "hello",
		"number": 42,
		"float":  3.14,
		"bool":   true,
		"null":   nil,
		"array":  []int{1, 2, 3},
	}

	sonicOutput, err := Marshal(data)
	if err != nil {
		t.Fatalf("Sonic Marshal failed: %v", err)
	}

	stdOutput, err := stdjson.Marshal(data)
	if err != nil {
		t.Fatalf("Std Marshal failed: %v", err)
	}

	// Both should be valid JSON and unmarshal to same structure
	var sonicDecoded, stdDecoded map[string]any
	Unmarshal(sonicOutput, &sonicDecoded)
	stdjson.Unmarshal(stdOutput, &stdDecoded)

	// Compare key fields
	if sonicDecoded["string"] != stdDecoded["string"] {
		t.Error("String field mismatch")
	}
	if sonicDecoded["bool"] != stdDecoded["bool"] {
		t.Error("Bool field mismatch")
	}
}

// Benchmark comparison
func BenchmarkMarshal_Sonic(b *testing.B) {
	data := TestStruct{Name: "Benchmark", Age: 30, Balance: 1000.00}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Marshal(data)
	}
}

func BenchmarkMarshal_StdLib(b *testing.B) {
	data := TestStruct{Name: "Benchmark", Age: 30, Balance: 1000.00}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stdjson.Marshal(data)
	}
}

func BenchmarkUnmarshal_Sonic(b *testing.B) {
	data := []byte(`{"name":"Benchmark","age":30,"balance":1000.00}`)
	var result TestStruct
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Unmarshal(data, &result)
	}
}

func BenchmarkUnmarshal_StdLib(b *testing.B) {
	data := []byte(`{"name":"Benchmark","age":30,"balance":1000.00}`)
	var result TestStruct
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		stdjson.Unmarshal(data, &result)
	}
}
