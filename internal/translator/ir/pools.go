// Package ir provides memory pools for the translator layer.
package ir

import (
	"bytes"
	"strings"
	"sync"
)

var BytesBufferPool = sync.Pool{
	New: func() any {
		return bytes.NewBuffer(make([]byte, 0, 1024))
	},
}

func GetBuffer() *bytes.Buffer {
	return BytesBufferPool.Get().(*bytes.Buffer)
}

// PutBuffer returns a buffer to the pool after resetting it.
func PutBuffer(buf *bytes.Buffer) {
	buf.Reset()
	BytesBufferPool.Put(buf)
}

// StringBuilderPool provides reusable strings.Builder instances.
var StringBuilderPool = sync.Pool{
	New: func() any {
		b := &strings.Builder{}
		b.Grow(512)
		return b
	},
}

func GetStringBuilder() *strings.Builder {
	return StringBuilderPool.Get().(*strings.Builder)
}

// PutStringBuilder returns a string builder to the pool after resetting it.
func PutStringBuilder(sb *strings.Builder) {
	sb.Reset()
	StringBuilderPool.Put(sb)
}

// -----------------------------------------------------------------------------
// UUID Pool - Optimized UUID generation
// -----------------------------------------------------------------------------

// uuidBytePool provides reusable byte slices for UUID generation.
var uuidBytePool = sync.Pool{
	New: func() any {
		b := make([]byte, 16)
		return &b
	},
}

func GetUUIDBuf() *[]byte {
	return uuidBytePool.Get().(*[]byte)
}

// PutUUIDBuf returns a UUID buffer to the pool.
func PutUUIDBuf(b *[]byte) {
	uuidBytePool.Put(b)
}

// -----------------------------------------------------------------------------
// Pre-allocated common values
// -----------------------------------------------------------------------------

// Common empty values to avoid allocations
var (
	EmptyMap       = map[string]any{}
	EmptySlice     = []any{}
	EmptyStringMap = map[string]string{}
)

// JSON Schema version constants
// Claude API requires JSON Schema draft 2020-12
// See: https://docs.anthropic.com/en/docs/build-with-claude/tool-use
const (
	JSONSchemaDraft202012 = "https://json-schema.org/draft/2020-12/schema"
)

// Common JSON schema fragments (immutable, safe to share)
var (
	EmptyObjectSchema = map[string]any{
		"type":       "object",
		"properties": map[string]any{},
	}

	ClaudeEmptyInputSchema = map[string]any{
		"type":                 "object",
		"properties":           map[string]any{},
		"additionalProperties": false,
		"$schema":              JSONSchemaDraft202012,
	}
)

func BuildSSEChunk(jsonData []byte) []byte {
	size := 6 + len(jsonData) + 2 // "data: " + json + "\n\n"
	buf := make([]byte, 0, size)
	buf = append(buf, "data: "...)
	buf = append(buf, jsonData...)
	buf = append(buf, "\n\n"...)
	return buf
}

func BuildSSEEvent(eventType string, jsonData []byte) []byte {
	size := 7 + len(eventType) + 7 + len(jsonData) + 2
	buf := make([]byte, 0, size)
	buf = append(buf, "event: "...)
	buf = append(buf, eventType...)
	buf = append(buf, "\ndata: "...)
	buf = append(buf, jsonData...)
	buf = append(buf, "\n\n"...)
	return buf
}
