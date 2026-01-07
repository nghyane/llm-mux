package ir

import (
	"strings"
	"testing"
)

func TestBytesBufferPool_InitialCapacity(t *testing.T) {
	buf := GetBuffer()
	defer PutBuffer(buf)

	// Verify initial capacity is 4KB
	if buf.Cap() < 4096 {
		t.Errorf("Expected buffer capacity >= 4096, got %d", buf.Cap())
	}
}

func TestBytesBufferPool_LargeBuffersNotReturned(t *testing.T) {
	buf := GetBuffer()
	
	// Grow buffer beyond 64KB threshold
	largeData := make([]byte, 70*1024)
	buf.Write(largeData)
	
	if buf.Cap() <= 64*1024 {
		t.Fatal("Buffer didn't grow as expected")
	}
	
	// Put the large buffer back
	PutBuffer(buf)
	
	// Get a new buffer - should be a fresh one, not the large one
	newBuf := GetBuffer()
	defer PutBuffer(newBuf)
	
	// The new buffer should have the default capacity, not the large one
	if newBuf.Cap() > 64*1024 {
		t.Errorf("Pool returned oversized buffer with capacity %d", newBuf.Cap())
	}
}

func TestStringBuilderPool_InitialCapacity(t *testing.T) {
	sb := GetStringBuilder()
	defer PutStringBuilder(sb)

	// Verify initial capacity is at least 2KB
	if sb.Cap() < 2048 {
		t.Errorf("Expected string builder capacity >= 2048, got %d", sb.Cap())
	}
}

func TestStringBuilderPool_LargeBuildersNotReturned(t *testing.T) {
	sb := GetStringBuilder()
	
	// Grow builder beyond 32KB threshold
	largeString := strings.Repeat("x", 35*1024)
	sb.WriteString(largeString)
	
	if sb.Cap() <= 32*1024 {
		t.Fatal("Builder didn't grow as expected")
	}
	
	// Put the large builder back
	PutStringBuilder(sb)
	
	// Get a new builder - should be a fresh one, not the large one
	newSb := GetStringBuilder()
	defer PutStringBuilder(newSb)
	
	// The new builder should have the default capacity, not the large one
	if newSb.Cap() > 32*1024 {
		t.Errorf("Pool returned oversized builder with capacity %d", newSb.Cap())
	}
}

func TestSSEChunkPool_InitialCapacity(t *testing.T) {
	chunk := GetSSEChunkBuf()
	defer PutSSEChunkBuf(chunk)

	// Verify initial capacity is 2KB
	if cap(chunk) < 2048 {
		t.Errorf("Expected SSE chunk capacity >= 2048, got %d", cap(chunk))
	}
}

func TestSSEChunkPool_SizeRange(t *testing.T) {
	// Test that small buffers are not returned
	smallBuf := make([]byte, 0, 1024)
	PutSSEChunkBuf(smallBuf)
	
	// Test that large buffers are not returned
	largeBuf := make([]byte, 0, 20*1024)
	PutSSEChunkBuf(largeBuf)
	
	// Get a buffer from pool - should have default capacity
	buf := GetSSEChunkBuf()
	if cap(buf) < 2048 || cap(buf) > 16*1024 {
		t.Errorf("Pool returned buffer with unexpected capacity %d", cap(buf))
	}
	PutSSEChunkBuf(buf)
}

func TestBuildSSEChunk(t *testing.T) {
	jsonData := []byte(`{"test":"data"}`)
	chunk := BuildSSEChunk(jsonData)
	defer PutSSEChunkBuf(chunk)
	
	expected := "data: {\"test\":\"data\"}\n\n"
	if string(chunk) != expected {
		t.Errorf("Expected %q, got %q", expected, string(chunk))
	}
	
	// Verify chunk has reasonable capacity
	if cap(chunk) < 2048 {
		t.Errorf("Expected chunk capacity >= 2048, got %d", cap(chunk))
	}
}

func TestBuildSSEEvent(t *testing.T) {
	eventType := "message_start"
	jsonData := []byte(`{"type":"message_start"}`)
	chunk := BuildSSEEvent(eventType, jsonData)
	defer PutSSEChunkBuf(chunk)
	
	expected := "event: message_start\ndata: {\"type\":\"message_start\"}\n\n"
	if string(chunk) != expected {
		t.Errorf("Expected %q, got %q", expected, string(chunk))
	}
}

// Benchmark to demonstrate performance improvement
func BenchmarkBufferPoolSmallPayload(b *testing.B) {
	data := []byte("Hello, World!")
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := GetBuffer()
		buf.Write(data)
		PutBuffer(buf)
	}
}

func BenchmarkBufferPoolMediumPayload(b *testing.B) {
	// Simulate a typical SSE event payload (2-3KB)
	data := make([]byte, 2500)
	for i := range data {
		data[i] = byte('a' + (i % 26))
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := GetBuffer()
		buf.Write(data)
		PutBuffer(buf)
	}
}

func BenchmarkBufferPoolLargePayload(b *testing.B) {
	// Simulate a large chunk in a streaming response
	data := make([]byte, 10*1024)
	for i := range data {
		data[i] = byte('a' + (i % 26))
	}
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf := GetBuffer()
		buf.Write(data)
		PutBuffer(buf)
	}
}

func BenchmarkSSEChunkPoolTypicalChunk(b *testing.B) {
	// Simulate a typical SSE chunk
	jsonData := []byte(`{"id":"msg_123","type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}`)
	
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		chunk := BuildSSEChunk(jsonData)
		PutSSEChunkBuf(chunk)
	}
}
