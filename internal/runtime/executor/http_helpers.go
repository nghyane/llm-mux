package executor

import (
	"compress/flate"
	"compress/gzip"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

type HeaderConfig struct {
	Token         string
	UserAgent     string
	ExtraHeaders  map[string]string
	StreamHeaders map[string]string
}

func ApplyAPIHeaders(r *http.Request, cfg HeaderConfig, stream bool) {
	SetCommonHeaders(r, "application/json")
	r.Header.Set("Authorization", "Bearer "+cfg.Token)

	if stream {
		r.Header.Set("Accept", "text/event-stream")
		for k, v := range cfg.StreamHeaders {
			r.Header.Set(k, v)
		}
	} else {
		r.Header.Set("Accept", "application/json")
	}

	if cfg.UserAgent != "" {
		r.Header.Set("User-Agent", cfg.UserAgent)
	}

	for k, v := range cfg.ExtraHeaders {
		r.Header.Set(k, v)
	}
}

var GzipReaderPool = sync.Pool{
	New: func() any {
		return new(gzip.Reader)
	},
}

var zstdDecoderPool = sync.Pool{
	New: func() any {
		decoder, _ := zstd.NewReader(nil)
		return decoder
	},
}

var brotliReaderPool = sync.Pool{
	New: func() any {
		return new(brotli.Reader)
	},
}

type compositeReadCloser struct {
	io.Reader
	closers []func() error
}

func (c *compositeReadCloser) Close() error {
	var firstErr error
	for i := range c.closers {
		if c.closers[i] == nil {
			continue
		}
		if err := c.closers[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

type pooledGzipReadCloser struct {
	gr   *gzip.Reader
	body io.ReadCloser
}

func (p *pooledGzipReadCloser) Read(b []byte) (int, error) {
	return p.gr.Read(b)
}

func (p *pooledGzipReadCloser) Close() error {
	err := p.gr.Close()
	GzipReaderPool.Put(p.gr)
	if bodyErr := p.body.Close(); bodyErr != nil && err == nil {
		err = bodyErr
	}
	return err
}

type pooledZstdReadCloser struct {
	decoder *zstd.Decoder
	body    io.ReadCloser
}

func (p *pooledZstdReadCloser) Read(b []byte) (int, error) {
	return p.decoder.Read(b)
}

func (p *pooledZstdReadCloser) Close() error {
	p.decoder.Reset(nil)
	zstdDecoderPool.Put(p.decoder)
	return p.body.Close()
}

type pooledBrotliReadCloser struct {
	br   *brotli.Reader
	body io.ReadCloser
}

func (p *pooledBrotliReadCloser) Read(b []byte) (int, error) {
	return p.br.Read(b)
}

func (p *pooledBrotliReadCloser) Close() error {
	// Close body first to terminate any ongoing stream - don't drain as it may block forever
	err := p.body.Close()
	// Reset reader for pool reuse without draining
	p.br.Reset(nil)
	brotliReaderPool.Put(p.br)
	return err
}

func decodeResponseBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	if body == nil {
		return nil, fmt.Errorf("response body is nil")
	}
	if contentEncoding == "" {
		return body, nil
	}
	encodings := strings.Split(contentEncoding, ",")
	for _, raw := range encodings {
		encoding := strings.TrimSpace(strings.ToLower(raw))
		switch encoding {
		case "", "identity":
			continue
		case "gzip":
			gr := GzipReaderPool.Get().(*gzip.Reader)
			if err := gr.Reset(body); err != nil {
				GzipReaderPool.Put(gr)
				_ = body.Close()
				return nil, fmt.Errorf("failed to reset gzip reader: %w", err)
			}
			return &pooledGzipReadCloser{gr: gr, body: body}, nil
		case "deflate":
			deflateReader := flate.NewReader(body)
			return &compositeReadCloser{
				Reader: deflateReader,
				closers: []func() error{
					deflateReader.Close,
					func() error { return body.Close() },
				},
			}, nil
		case "br":
			br := brotliReaderPool.Get().(*brotli.Reader)
			if err := br.Reset(body); err != nil {
				brotliReaderPool.Put(br)
				_ = body.Close()
				return nil, fmt.Errorf("failed to reset brotli reader: %w", err)
			}
			return &pooledBrotliReadCloser{br: br, body: body}, nil
		case "zstd":
			decoder := zstdDecoderPool.Get().(*zstd.Decoder)
			if err := decoder.Reset(body); err != nil {
				zstdDecoderPool.Put(decoder)
				_ = body.Close()
				return nil, fmt.Errorf("failed to reset zstd decoder: %w", err)
			}
			return &pooledZstdReadCloser{decoder: decoder, body: body}, nil
		default:
			continue
		}
	}
	return body, nil
}

// DecodeResponseBody is an exported alias for decodeResponseBody.
func DecodeResponseBody(body io.ReadCloser, contentEncoding string) (io.ReadCloser, error) {
	return decodeResponseBody(body, contentEncoding)
}
