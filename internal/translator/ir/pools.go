// Package ir provides memory pools for the translator layer.
package ir

import (
	"bytes"
	"strings"
	"sync"
)

var BytesBufferPool = sync.Pool{
	New: func() any {
		// Increased from 1KB to 4KB to reduce reallocations for streaming events
		// Most SSE events with JSON payloads are 1-3KB
		return bytes.NewBuffer(make([]byte, 0, 4096))
	},
}

func GetBuffer() *bytes.Buffer {
	return BytesBufferPool.Get().(*bytes.Buffer)
}

// PutBuffer returns a buffer to the pool after resetting it.
// Buffers that have grown excessively large are not returned to the pool
// to prevent memory bloat in the pool.
func PutBuffer(buf *bytes.Buffer) {
	// Don't return buffers larger than 64KB to the pool
	// This prevents the pool from being polluted with oversized buffers
	if buf.Cap() > 64*1024 {
		return
	}
	buf.Reset()
	BytesBufferPool.Put(buf)
}

// StringBuilderPool provides reusable strings.Builder instances.
var StringBuilderPool = sync.Pool{
	New: func() any {
		b := &strings.Builder{}
		// Increased from 512 bytes to 2KB for better performance with larger text chunks
		b.Grow(2048)
		return b
	},
}

func GetStringBuilder() *strings.Builder {
	return StringBuilderPool.Get().(*strings.Builder)
}

// PutStringBuilder returns a string builder to the pool after resetting it.
// Large builders are not returned to prevent pool bloat.
func PutStringBuilder(sb *strings.Builder) {
	// Don't return builders larger than 32KB to the pool
	if sb.Cap() > 32*1024 {
		return
	}
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

// -----------------------------------------------------------------------------
// SSE Chunk Pools - Optimized for streaming responses
// -----------------------------------------------------------------------------

// sseChunkPool provides reusable byte slices for SSE chunk building.
var sseChunkPool = sync.Pool{
	New: func() any {
		// Increased from 512 bytes to 2KB to better accommodate typical SSE chunks
		// with JSON payloads, reducing reallocations
		b := make([]byte, 0, 2048)
		return &b
	},
}

func GetSSEChunkBuf() []byte {
	bp := sseChunkPool.Get().(*[]byte)
	return (*bp)[:0]
}

// PutSSEChunkBuf returns an SSE chunk buffer to the pool.
// Only returns buffers within a reasonable size range to maintain pool efficiency.
func PutSSEChunkBuf(b []byte) {
	// Accept buffers between 2KB and 16KB to keep the pool healthy
	if cap(b) >= 2048 && cap(b) <= 16*1024 {
		bp := b[:0]
		sseChunkPool.Put(&bp)
	}
}

func BuildSSEChunk(jsonData []byte) []byte {
	size := 6 + len(jsonData) + 2 // "data: " + json + "\n\n"
	buf := GetSSEChunkBuf()
	if cap(buf) < size {
		buf = make([]byte, 0, size)
	}
	buf = append(buf, "data: "...)
	buf = append(buf, jsonData...)
	buf = append(buf, "\n\n"...)
	return buf
}

func BuildSSEEvent(eventType string, jsonData []byte) []byte {
	size := 7 + len(eventType) + 7 + len(jsonData) + 2
	buf := GetSSEChunkBuf()
	if cap(buf) < size {
		buf = make([]byte, 0, size)
	}
	buf = append(buf, "event: "...)
	buf = append(buf, eventType...)
	buf = append(buf, "\ndata: "...)
	buf = append(buf, jsonData...)
	buf = append(buf, "\n\n"...)
	return buf
}
