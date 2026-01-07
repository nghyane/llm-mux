package stream

import (
	"github.com/nghyane/llm-mux/internal/translator/ir"
)

type ChunkParser func(line []byte) ([]ir.UnifiedEvent, error)

type BaseStreamProcessor struct {
	Translator *StreamTranslator
	ParseChunk ChunkParser
}

func (p *BaseStreamProcessor) ProcessLine(line []byte) ([][]byte, *ir.Usage, error) {
	events, err := p.ParseChunk(line)
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

func (p *BaseStreamProcessor) ProcessDone() ([][]byte, error) {
	return p.Translator.Flush()
}

func NewBaseStreamProcessor(translator *StreamTranslator, parser ChunkParser) *BaseStreamProcessor {
	return &BaseStreamProcessor{
		Translator: translator,
		ParseChunk: parser,
	}
}
