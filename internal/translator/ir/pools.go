// Package ir provides memory pools for the translator layer.
package ir

import (
	"bytes"
	"strings"
	"sync"
)

// -----------------------------------------------------------------------------
// Generic Pool Wrapper
// -----------------------------------------------------------------------------

// Pool is a type-safe wrapper around sync.Pool using Go 1.18+ generics.
type Pool[T any] struct {
	pool sync.Pool
}

// NewPool creates a new type-safe pool with the given constructor function.
func NewPool[T any](newFunc func() T) *Pool[T] {
	return &Pool[T]{
		pool: sync.Pool{
			New: func() any { return newFunc() },
		},
	}
}

// Get retrieves an item from the pool without type assertion at call site.
func (p *Pool[T]) Get() T {
	return p.pool.Get().(T)
}

// Put returns an item to the pool.
func (p *Pool[T]) Put(v T) {
	p.pool.Put(v)
}

// -----------------------------------------------------------------------------
// Bytes Buffer Pool
// -----------------------------------------------------------------------------

// BytesBufferPool provides reusable bytes.Buffer instances.
// Initial capacity increased from 1KB to 4KB for better performance
// with streaming responses that have larger intermediate buffers.
var BytesBufferPool = NewPool(func() *bytes.Buffer {
	return bytes.NewBuffer(make([]byte, 0, 4096))
})

// GetBuffer retrieves a buffer from the pool.
func GetBuffer() *bytes.Buffer {
	return BytesBufferPool.Get()
}

// PutBuffer returns a buffer to the pool after resetting it.
func PutBuffer(buf *bytes.Buffer) {
	buf.Reset()
	BytesBufferPool.Put(buf)
}

// -----------------------------------------------------------------------------
// String Builder Pool
// -----------------------------------------------------------------------------

// StringBuilderPool provides reusable strings.Builder instances.
// Increased from 512B to 2KB for better handling of longer response strings
// in streaming mode with larger context windows.
var StringBuilderPool = NewPool(func() *strings.Builder {
	b := &strings.Builder{}
	b.Grow(2048)
	return b
})

// GetStringBuilder retrieves a string builder from the pool.
func GetStringBuilder() *strings.Builder {
	return StringBuilderPool.Get()
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
var uuidBytePool = NewPool(func() *[]byte {
	b := make([]byte, 16)
	return &b
})

// GetUUIDBuf retrieves a UUID buffer from the pool.
func GetUUIDBuf() *[]byte {
	return uuidBytePool.Get()
}

// PutUUIDBuf returns a UUID buffer to the pool.
func PutUUIDBuf(b *[]byte) {
	uuidBytePool.Put(b)
}

// JSON Schema version constants
// Claude API requires JSON Schema draft 2020-12
// See: https://docs.anthropic.com/en/docs/build-with-claude/tool-use
const (
	JSONSchemaDraft202012 = "https://json-schema.org/draft/2020-12/schema"
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
