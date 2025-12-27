package wsrelay

import (
	"encoding/binary"
	"github.com/nghyane/llm-mux/internal/json"
)

// Message represents the JSON payload exchanged with websocket clients.
type Message struct {
	ID      string         `json:"id"`
	Type    string         `json:"type"`
	Payload map[string]any `json:"payload,omitempty"`
}

const (
	// MessageTypeHTTPReq identifies an HTTP-style request envelope.
	MessageTypeHTTPReq = "http_request"
	// MessageTypeHTTPResp identifies a non-streaming HTTP response envelope.
	MessageTypeHTTPResp = "http_response"
	// MessageTypeStreamStart marks the beginning of a streaming response.
	MessageTypeStreamStart = "stream_start"
	// MessageTypeStreamChunk carries a streaming response chunk.
	MessageTypeStreamChunk = "stream_chunk"
	// MessageTypeStreamEnd marks the completion of a streaming response.
	MessageTypeStreamEnd = "stream_end"
	// MessageTypeError carries an error response.
	MessageTypeError = "error"
	// MessageTypePing represents ping messages from clients.
	MessageTypePing = "ping"
	// MessageTypePong represents pong responses back to clients.
	MessageTypePong = "pong"
)

// Binary message type identifiers (for optimized streaming)
const (
	BinaryTypeStreamChunk byte = 0x01
	BinaryTypeStreamEnd   byte = 0x02
	BinaryTypeStreamStart byte = 0x03
)

// BinaryChunkMessage is an optimized format for stream chunks.
// Format: [type:1][id_len:2][id:N][data:...]
// This avoids JSON parsing overhead for high-frequency stream chunks.
type BinaryChunkMessage struct {
	Type byte
	ID   string
	Data []byte
}

// EncodeBinaryChunk creates a binary-encoded chunk message.
// Pre-allocates buffer to avoid multiple allocations.
func EncodeBinaryChunk(msgType byte, id string, data []byte) []byte {
	idLen := len(id)
	buf := make([]byte, 1+2+idLen+len(data))
	buf[0] = msgType
	binary.BigEndian.PutUint16(buf[1:3], uint16(idLen))
	copy(buf[3:3+idLen], id)
	copy(buf[3+idLen:], data)
	return buf
}

// DecodeBinaryChunk parses a binary-encoded chunk message.
// Returns zero values if format is invalid.
func DecodeBinaryChunk(data []byte) BinaryChunkMessage {
	if len(data) < 3 {
		return BinaryChunkMessage{}
	}
	msgType := data[0]
	idLen := int(binary.BigEndian.Uint16(data[1:3]))
	if len(data) < 3+idLen {
		return BinaryChunkMessage{}
	}
	return BinaryChunkMessage{
		Type: msgType,
		ID:   string(data[3 : 3+idLen]),
		Data: data[3+idLen:],
	}
}

// IsBinaryMessage checks if data starts with a known binary type marker.
// Used to determine whether to use binary or JSON decoding.
func IsBinaryMessage(data []byte) bool {
	if len(data) == 0 {
		return false
	}
	switch data[0] {
	case BinaryTypeStreamChunk, BinaryTypeStreamEnd, BinaryTypeStreamStart:
		return true
	}
	return false
}

// FastEncodeChunk creates a minimal JSON chunk message.
// Optimized for the common case of stream chunks.
func FastEncodeChunk(id string, data []byte) []byte {
	// Pre-calculate size: {"id":"...","type":"stream_chunk","payload":{"data":"..."}}
	// This avoids json.Marshal overhead for simple messages
	const prefix = `{"id":"`
	const middle = `","type":"stream_chunk","payload":{"data":"`
	const suffix = `"}}`

	// Estimate size (data may need escaping, but usually SSE is clean)
	size := len(prefix) + len(id) + len(middle) + len(data) + len(suffix)
	buf := make([]byte, 0, size)

	buf = append(buf, prefix...)
	buf = append(buf, id...)
	buf = append(buf, middle...)
	buf = appendEscapedJSON(buf, data)
	buf = append(buf, suffix...)

	return buf
}

// appendEscapedJSON appends data with minimal JSON string escaping.
// Only escapes characters that are required for valid JSON.
func appendEscapedJSON(dst, src []byte) []byte {
	for _, c := range src {
		switch c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			if c < 0x20 {
				// Control character - use unicode escape
				dst = append(dst, '\\', 'u', '0', '0')
				dst = append(dst, "0123456789abcdef"[c>>4])
				dst = append(dst, "0123456789abcdef"[c&0xf])
			} else {
				dst = append(dst, c)
			}
		}
	}
	return dst
}

// FastDecodeChunk extracts data from a stream_chunk message without full JSON parsing.
// Falls back to standard JSON unmarshal if fast path fails.
func FastDecodeChunk(msg []byte) (id string, data []byte, ok bool) {
	// Try to extract using simple string search first
	// Format: {"id":"...","type":"stream_chunk","payload":{"data":"..."}}

	// Find "id":"
	const idMarker = `"id":"`
	idStart := bytesIndex(msg, []byte(idMarker))
	if idStart < 0 {
		return "", nil, false
	}
	idStart += len(idMarker)
	idEnd := bytesIndexByte(msg[idStart:], '"')
	if idEnd < 0 {
		return "", nil, false
	}
	id = string(msg[idStart : idStart+idEnd])

	// Find "data":"
	const dataMarker = `"data":"`
	dataStart := bytesIndex(msg, []byte(dataMarker))
	if dataStart < 0 {
		return id, nil, true // No data field
	}
	dataStart += len(dataMarker)

	// Find closing quote (handle escapes)
	dataEnd := findUnescapedQuote(msg[dataStart:])
	if dataEnd < 0 {
		return "", nil, false
	}

	// Unescape the data
	data = unescapeJSON(msg[dataStart : dataStart+dataEnd])
	return id, data, true
}

func bytesIndex(s, sep []byte) int {
	for i := 0; i+len(sep) <= len(s); i++ {
		if bytesEqual(s[i:i+len(sep)], sep) {
			return i
		}
	}
	return -1
}

func bytesIndexByte(s []byte, c byte) int {
	for i, b := range s {
		if b == c {
			return i
		}
	}
	return -1
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func findUnescapedQuote(s []byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) {
			i++ // Skip escaped char
			continue
		}
		if s[i] == '"' {
			return i
		}
	}
	return -1
}

func unescapeJSON(s []byte) []byte {
	// Fast path: no escapes
	hasEscape := false
	for _, c := range s {
		if c == '\\' {
			hasEscape = true
			break
		}
	}
	if !hasEscape {
		return s
	}

	// Slow path: unescape
	var result []byte
	if err := json.Unmarshal(append([]byte{'"'}, append(s, '"')...), &result); err != nil {
		return s // Return original on error
	}
	return result
}
