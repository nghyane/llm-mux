// Package json provides a drop-in replacement for encoding/json using bytedance/sonic
// for improved performance. All exported functions and types match the standard library API.
package json

import (
	stdjson "encoding/json"
	"io"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/decoder"
	"github.com/bytedance/sonic/encoder"
)

func Marshal(v any) ([]byte, error) {
	return sonic.Marshal(v)
}

// MarshalIndent returns the indented JSON encoding of v.
func MarshalIndent(v any, prefix, indent string) ([]byte, error) {
	return sonic.MarshalIndent(v, prefix, indent)
}

// Unmarshal parses the JSON-encoded data and stores the result in v.
func Unmarshal(data []byte, v any) error {
	return sonic.Unmarshal(data, v)
}

// Valid reports whether data is a valid JSON encoding.
func Valid(data []byte) bool {
	return sonic.Valid(data)
}

// Types from encoding/json - these are used by sonic internally
// and must remain compatible with the standard library.
type (
	// RawMessage is a raw encoded JSON value.
	RawMessage = stdjson.RawMessage

	// Number represents a JSON number literal.
	Number = stdjson.Number

	// Marshaler is the interface for types that can marshal themselves into valid JSON.
	Marshaler = stdjson.Marshaler

	// Unmarshaler is the interface for types that can unmarshal a JSON description of themselves.
	Unmarshaler = stdjson.Unmarshaler

	// Delim is a JSON array or object delimiter, one of [ ] { or }.
	Delim = stdjson.Delim

	// Token is a JSON token - Delim, bool, float64, Number, string, or nil.
	Token = stdjson.Token

	// InvalidUTF8Error is returned by Marshal when attempting to encode a string
	// value that is not a valid UTF-8 sequence.
	InvalidUTF8Error = stdjson.InvalidUTF8Error

	// InvalidUnmarshalError describes an invalid argument passed to Unmarshal.
	InvalidUnmarshalError = stdjson.InvalidUnmarshalError

	// MarshalerError represents an error from calling MarshalJSON.
	MarshalerError = stdjson.MarshalerError

	// SyntaxError is a description of a JSON syntax error.
	SyntaxError = stdjson.SyntaxError

	// UnmarshalFieldError describes a JSON object key that led to an unexported field.
	UnmarshalFieldError = stdjson.UnmarshalFieldError

	// UnmarshalTypeError describes a JSON value that was not appropriate for a value of a specific Go type.
	UnmarshalTypeError = stdjson.UnmarshalTypeError

	// UnsupportedTypeError is returned by Marshal when attempting to encode an unsupported value type.
	UnsupportedTypeError = stdjson.UnsupportedTypeError

	// UnsupportedValueError is returned by Marshal when attempting to encode an unsupported value.
	UnsupportedValueError = stdjson.UnsupportedValueError
)

// Encoder writes JSON values to an output stream.
type Encoder struct {
	enc *encoder.StreamEncoder
}

// NewEncoder returns a new encoder that writes to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{
		enc: encoder.NewStreamEncoder(w),
	}
}

// Encode writes the JSON encoding of v to the stream.
func (e *Encoder) Encode(v any) error {
	return e.enc.Encode(v)
}

// SetIndent instructs the encoder to format each subsequent encoded value.
func (e *Encoder) SetIndent(prefix, indent string) {
	e.enc.SetIndent(prefix, indent)
}

// SetEscapeHTML specifies whether problematic HTML characters should be escaped.
func (e *Encoder) SetEscapeHTML(on bool) {
	e.enc.SetEscapeHTML(on)
}

// Decoder reads and decodes JSON values from an input stream.
type Decoder struct {
	dec *decoder.StreamDecoder
}

// NewDecoder returns a new decoder that reads from r.
func NewDecoder(r io.Reader) *Decoder {
	return &Decoder{
		dec: decoder.NewStreamDecoder(r),
	}
}

// Decode reads the next JSON-encoded value from its input and stores it in v.
func (d *Decoder) Decode(v any) error {
	return d.dec.Decode(v)
}

// UseNumber causes the Decoder to unmarshal a number into an interface{} as a Number instead of float64.
func (d *Decoder) UseNumber() {
	d.dec.UseNumber()
}

// DisallowUnknownFields causes the Decoder to return an error when the destination
// is a struct and the input contains object keys which do not match any non-ignored fields.
func (d *Decoder) DisallowUnknownFields() {
	d.dec.DisallowUnknownFields()
}

// More reports whether there is another element in the current array or object being parsed.
func (d *Decoder) More() bool {
	return d.dec.More()
}

// InputOffset returns the input stream byte offset of the current decoder position.
func (d *Decoder) InputOffset() int64 {
	return d.dec.InputOffset()
}

// Buffered returns a reader of the data remaining in the Decoder's buffer.
func (d *Decoder) Buffered() io.Reader {
	return d.dec.Buffered()
}
