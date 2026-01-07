# Performance

## Overview

llm-mux is designed for high-performance streaming of LLM responses, with specific optimizations for handling large context windows and long-running conversations.

## Streaming Optimizations

### Buffer Management

llm-mux uses memory pooling and pre-allocation strategies to minimize allocations during streaming:

#### Event Serialization Buffers
- **Initial Capacity**: 4KB (up from 1KB)
- **Use Case**: SSE event JSON serialization
- **Benefit**: Most events fit without reallocation (1-3KB typical)
- **Pool Management**: Buffers > 64KB not returned to pool (prevents bloat)

#### String Building
- **Initial Capacity**: 2KB (up from 512 bytes)
- **Use Case**: Text concatenation in streaming responses
- **Pool Management**: Builders > 32KB not returned to pool

#### SSE Chunk Construction
- **Initial Capacity**: 2KB (up from 512 bytes)
- **Use Case**: Building `data:` prefixed SSE chunks
- **Pool Range**: 2KB - 16KB (optimal for pool health)

#### Scanner Buffer
- **Initial Capacity**: 256KB (up from 64KB)
- **Maximum Size**: 20MB
- **Use Case**: Reading streamed responses from providers
- **Benefit**: Reduces buffer reallocations for large context window responses

### Performance Characteristics

Benchmark results on typical workloads:

```
Small payload (~13B):    ~20 ns/op,  0 allocs
Medium payload (~2.5KB): ~42 ns/op,  0 allocs
Large payload (~10KB):   ~124 ns/op, 0 allocs
```

Zero allocations for typical streaming events due to buffer pooling.

## Large Context Window Support

### Automatic Optimization

When handling requests with large context windows (100K+ tokens), llm-mux automatically benefits from:

1. **Reduced Buffer Reallocations**: Larger initial buffers accommodate bigger response chunks
2. **Lower GC Pressure**: Pool management prevents memory bloat
3. **Better Throughput**: Fewer memory operations during streaming

### Memory Profile

Typical memory usage during streaming:
- **Small Response** (< 1K tokens): ~10-20 KB working memory
- **Medium Response** (1K-10K tokens): ~50-100 KB working memory
- **Large Response** (10K-100K tokens): ~500KB-2MB working memory

Buffer pools reuse memory between requests, keeping baseline memory stable even for large responses.

## Best Practices

### For Large Context Windows

1. **No Configuration Needed**: Optimizations are automatic
2. **Streaming Recommended**: Use `stream: true` in requests for better UX
3. **Connection Stability**: Ensure stable network for long-streaming responses

### Monitoring

Enable debug logging to monitor performance:

```yaml
debug: true
logging-to-file: true
```

This logs:
- Request/response timing
- Buffer allocation patterns (in verbose mode)
- Provider-specific performance metrics

## Comparison

### Before Optimization (v1.x)

- Buffer pools started at 1KB/512B
- Frequent reallocations during streaming
- Higher GC overhead with large responses
- No pool size management

### After Optimization (current)

- Buffer pools start at 2KB-4KB
- Minimal reallocations for typical workloads
- Pool management prevents bloat
- 4x larger scanner buffer (64KB → 256KB)

**Result**: ~2-3x reduction in allocations during streaming, especially noticeable with large context windows.

## Technical Details

### Pool Implementation

llm-mux uses Go's `sync.Pool` for buffer management:

```go
// Example: BytesBufferPool
var BytesBufferPool = sync.Pool{
    New: func() any {
        return bytes.NewBuffer(make([]byte, 0, 4096))
    },
}
```

**Benefits**:
- Thread-safe reuse of buffers
- Automatic GC integration
- Zero-allocation for pooled objects

### Size-Based Pool Management

Buffers that grow too large are not returned to the pool:

```go
func PutBuffer(buf *bytes.Buffer) {
    if buf.Cap() > 64*1024 {
        return  // Don't pollute pool with oversized buffers
    }
    buf.Reset()
    BytesBufferPool.Put(buf)
}
```

This prevents a single large request from affecting subsequent requests.

## Future Improvements

Potential areas for further optimization:

1. **Adaptive Buffer Sizing**: Adjust pool sizes based on runtime metrics
2. **Per-Provider Tuning**: Different buffer sizes for different providers
3. **Compression**: Optional response compression for network-bound scenarios
4. **Concurrent Streaming**: Parallel processing of multiple streams

## Benchmarking

To run performance benchmarks:

```bash
cd internal/translator/ir
go test -bench=BenchmarkBufferPool -benchmem
go test -bench=BenchmarkSSEChunkPool -benchmem
```

To profile memory usage:

```bash
go test -memprofile=mem.prof -bench=.
go tool pprof mem.prof
```

## Support

For performance issues or questions:
- [GitHub Issues](https://github.com/nghyane/llm-mux/issues)
- [Discord Community](https://discord.gg/86nFZUh4a9)
