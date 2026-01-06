package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nghyane/llm-mux/internal/config"
	log "github.com/nghyane/llm-mux/internal/logging"
	"github.com/nghyane/llm-mux/internal/provider"
	"github.com/nghyane/llm-mux/internal/translator/from_ir"
	"github.com/nghyane/llm-mux/internal/translator/ir"
	"github.com/nghyane/llm-mux/internal/translator/to_ir"
	"github.com/tidwall/gjson"
)

var scannerBufferPool = sync.Pool{
	New: func() any {
		return make([]byte, DefaultScannerBufferSize)
	},
}

var (
	doneMarker = []byte("[DONE]")
	dataTag    = []byte("data:")
)

type StreamProcessor interface {
	ProcessLine(line []byte) (chunks [][]byte, usage *ir.Usage, err error)
	ProcessDone() (chunks [][]byte, err error)
}

type StreamPreprocessor func(line []byte) (payload []byte, skip bool)

type StreamConfig struct {
	ExecutorName       string
	MaxBufferSize      int
	Preprocessor       StreamPreprocessor
	SkipEmptyLines     bool
	PassthroughOnEmpty bool
	EnsurePublished    bool
	HandleDoneSignal   bool
	SkipDoneInData     bool
	IdleTimeout        time.Duration
}

func GeminiPreprocessor() StreamPreprocessor {
	return func(line []byte) (payload []byte, skip bool) {
		filtered := FilterSSEUsageMetadata(line)

		payload = jsonPayload(filtered)
		if payload == nil {
			return nil, true
		}

		if !gjson.ValidBytes(payload) {
			log.Debugf("gemini preprocessor: skipping malformed SSE payload")
			return nil, true
		}

		return payload, false
	}
}

func DataTagPreprocessor() StreamPreprocessor {
	return func(line []byte) (payload []byte, skip bool) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			return nil, true
		}

		if bytes.Equal(trimmed, doneMarker) {
			return trimmed, false
		}

		if bytes.HasPrefix(trimmed, dataTag) {
			payload = bytes.TrimSpace(trimmed[len(dataTag):])
		} else {
			payload = trimmed
		}

		if len(payload) == 0 {
			return nil, true
		}

		return payload, false
	}
}

func sendChunk(ctx context.Context, out chan<- provider.StreamChunk, chunk provider.StreamChunk) bool {
	select {
	case out <- chunk:
		return true
	case <-ctx.Done():
		return false
	}
}

func isDoneLine(line []byte) bool {
	trimmed := bytes.TrimSpace(line)
	if bytes.Equal(trimmed, doneMarker) {
		return true
	}
	if bytes.HasPrefix(trimmed, dataTag) {
		data := bytes.TrimSpace(trimmed[len(dataTag):])
		return bytes.Equal(data, doneMarker)
	}
	return false
}

func RunSSEStream(
	ctx context.Context,
	body io.ReadCloser,
	reporter *usageReporter,
	processor StreamProcessor,
	cfg StreamConfig,
) <-chan provider.StreamChunk {
	out := make(chan provider.StreamChunk, 64) // Increased buffer for high concurrency

	go func() {
		defer close(out)
		defer func() {
			if r := recover(); r != nil {
				log.Errorf("%s: panic in stream goroutine: %v", cfg.ExecutorName, r)
			}
		}()

		// Use StreamReader for context-aware cancellation and idle detection
		idleTimeout := cfg.IdleTimeout
		if idleTimeout == 0 {
			idleTimeout = DefaultStreamIdleTimeout
		}
		streamReader := NewStreamReader(ctx, body, idleTimeout, cfg.ExecutorName)
		defer streamReader.Close()

		buf := scannerBufferPool.Get().([]byte)
		defer scannerBufferPool.Put(buf)

		scanner := bufio.NewScanner(streamReader)
		maxBufferSize := cfg.MaxBufferSize
		if maxBufferSize == 0 {
			maxBufferSize = DefaultStreamBufferSize
		}
		scanner.Buffer(buf, maxBufferSize)

		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
			}

			line := scanner.Bytes()

			if isDoneLine(line) {
				if cfg.SkipDoneInData {
					continue
				}
				if cfg.HandleDoneSignal && processor != nil {
					doneChunks, doneErr := processor.ProcessDone()
					if doneErr != nil {
						if reporter != nil {
							reporter.publishFailure(ctx)
						}
						sendChunk(ctx, out, provider.StreamChunk{Err: doneErr})
						return
					}
					for _, chunk := range doneChunks {
						if !sendChunk(ctx, out, provider.StreamChunk{Payload: chunk}) {
							return
						}
					}
				}
				continue
			}

			payload := line
			if cfg.Preprocessor != nil {
				var skip bool
				payload, skip = cfg.Preprocessor(line)
				if skip {
					continue
				}
			}

			if cfg.SkipEmptyLines && len(bytes.TrimSpace(payload)) == 0 {
				continue
			}

			chunks, usage, err := processor.ProcessLine(payload)
			if err != nil {
				if reporter != nil {
					reporter.publishFailure(ctx)
				}
				if processor != nil {
					if flushed, _ := processor.ProcessDone(); len(flushed) > 0 {
						for _, chunk := range flushed {
							if !sendChunk(ctx, out, provider.StreamChunk{Payload: chunk}) {
								return
							}
						}
					}
				}
				errorJSON := fmt.Sprintf(`data: {"error": {"message": "%s", "type": "server_error"}}`+"\n\n", err.Error())
				sendChunk(ctx, out, provider.StreamChunk{Payload: []byte(errorJSON)})
				return
			}

			if usage != nil && reporter != nil {
				reporter.publish(ctx, usage)
			}

			if len(chunks) > 0 {
				for _, chunk := range chunks {
					if !sendChunk(ctx, out, provider.StreamChunk{Payload: chunk}) {
						return
					}
				}
			} else if cfg.PassthroughOnEmpty {
				if !sendChunk(ctx, out, provider.StreamChunk{Payload: bytes.Clone(payload)}) {
					return
				}
			}
		}

		if processor != nil {
			doneChunks, doneErr := processor.ProcessDone()
			if doneErr != nil {
				if reporter != nil {
					reporter.publishFailure(ctx)
				}
				sendChunk(ctx, out, provider.StreamChunk{Err: doneErr})
				return
			}
			for _, chunk := range doneChunks {
				if !sendChunk(ctx, out, provider.StreamChunk{Payload: chunk}) {
					return
				}
			}
		}

		if errScan := scanner.Err(); errScan != nil {
			if reporter != nil {
				reporter.publishFailure(ctx)
			}
			errorJSON := fmt.Sprintf(`data: {"error": {"message": "%s", "type": "server_error"}}`+"\n\n", errScan.Error())
			sendChunk(ctx, out, provider.StreamChunk{Payload: []byte(errorJSON)})
			return
		}

		if cfg.EnsurePublished && reporter != nil {
			reporter.ensurePublished(ctx)
		}
	}()

	return out
}

type SimpleStreamProcessor struct {
	ProcessFunc func(line []byte) (chunks [][]byte, usage *ir.Usage, err error)
}

func (p *SimpleStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	if p.ProcessFunc == nil {
		return nil, nil, nil
	}
	return p.ProcessFunc(line)
}

func (p *SimpleStreamProcessor) ProcessDone() ([][]byte, error) {
	return nil, nil
}

func NewSimpleStreamProcessor(fn func(line []byte) (chunks [][]byte, usage *ir.Usage, err error)) *SimpleStreamProcessor {
	return &SimpleStreamProcessor{ProcessFunc: fn}
}

type OpenAIStreamProcessor struct {
	translator *StreamTranslator
	ctx        *StreamContext
	Preprocess func(line []byte, firstChunk bool) []byte
	firstChunk bool
}

func NewOpenAIStreamProcessor(cfg *config.Config, from provider.Format, model, messageID string) *OpenAIStreamProcessor {
	ctx := NewStreamContext()
	return &OpenAIStreamProcessor{
		translator: NewStreamTranslator(cfg, provider.FromString("openai"), from.String(), model, messageID, ctx),
		ctx:        ctx,
		firstChunk: true,
	}
}

func (p *OpenAIStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	payload := line
	isFirst := p.firstChunk
	if p.Preprocess != nil {
		payload = p.Preprocess(line, isFirst)
		if payload == nil {
			return nil, nil, nil
		}
	}
	p.firstChunk = false

	events, err := to_ir.ParseOpenAIChunk(bytes.Clone(payload))
	if err != nil {
		return nil, nil, err
	}

	if len(events) == 0 {
		return nil, nil, nil
	}

	result, err := p.translator.Translate(events)
	if err != nil {
		return nil, nil, err
	}
	return result.Chunks, result.Usage, nil
}

func (p *OpenAIStreamProcessor) ProcessDone() ([][]byte, error) {
	events, _ := to_ir.ParseOpenAIChunk([]byte("[DONE]"))
	if len(events) == 0 {
		return p.translator.Flush()
	}
	result, _ := p.translator.Translate(events)
	flushed, err := p.translator.Flush()
	if err != nil {
		return nil, err
	}
	return append(result.Chunks, flushed...), nil
}

type GeminiCLIStreamProcessor struct {
	Translator *StreamTranslator
}

func NewGeminiCLIStreamProcessor(translator *StreamTranslator) *GeminiCLIStreamProcessor {
	return &GeminiCLIStreamProcessor{Translator: translator}
}

func (p *GeminiCLIStreamProcessor) ProcessLine(payload []byte) ([][]byte, *ir.Usage, error) {
	state := p.Translator.ctx.GeminiState
	var events []ir.UnifiedEvent
	var err error
	if p.Translator.ctx.ToolSchemaCtx != nil {
		events, err = (&from_ir.VertexEnvelopeProvider{}).ParseStreamChunkWithStateContext(payload, state, p.Translator.ctx.ToolSchemaCtx)
	} else {
		events, err = (&from_ir.VertexEnvelopeProvider{}).ParseStreamChunkWithState(payload, state)
	}
	if err != nil {
		return nil, nil, err
	}
	if len(events) == 0 {
		return nil, nil, nil
	}

	result, err := p.Translator.Translate(events)
	if err != nil {
		return nil, nil, err
	}
	return result.Chunks, result.Usage, nil
}

func (p *GeminiCLIStreamProcessor) ProcessDone() ([][]byte, error) {
	return p.Translator.Flush()
}
