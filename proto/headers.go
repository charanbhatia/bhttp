package proto

import (
	"encoding/binary"
	"fmt"
	"slices"
)

// StaticTable maps indexes 1–10 to names (README §5). Index 0 means a literal name follows.
var StaticTable = [...]string{1: ":method", ":path", ":status", "host", "user-agent", "accept", "content-type", "content-length", "server", "date"}

var errTruncated = fmt.Errorf("%w: header block truncated", ErrMalformed)

type Field struct{ Name, Value string }

type Headers []Field

// Get returns the value of the first field called name, or "".
func (h Headers) Get(name string) string {
	for _, f := range h {
		if f.Name == name {
			return f.Value
		}
	}
	return ""
}

// EncodeHeaders returns h as a HEADERS payload.
func EncodeHeaders(h Headers) ([]byte, error) {
	var b []byte
	for _, f := range h {
		if i := slices.Index(StaticTable[:], f.Name); i > 0 {
			b = append(b, byte(i))
		} else {
			if len(f.Name) == 0 || len(f.Name) > 255 {
				return nil, fmt.Errorf("header name %q: length must be 1-255", f.Name)
			}
			b = append(b, 0, byte(len(f.Name)))
			b = append(b, f.Name...)
		}
		b = binary.BigEndian.AppendUint16(b, uint16(len(f.Value)))
		b = append(b, f.Value...)
	}
	// Also rejects any value over 65535 bytes, whose length would not fit in 16 bits.
	if len(b) > MaxPayload {
		return nil, fmt.Errorf("header block is %d bytes, max %d", len(b), MaxPayload)
	}
	return b, nil
}

// DecodeHeaders parses a HEADERS payload.
func DecodeHeaders(b []byte) (Headers, error) {
	var h Headers
	for len(b) > 0 {
		idx := b[0]
		b = b[1:]
		var name string
		switch {
		case idx == 0:
			if len(b) == 0 || len(b) < 1+int(b[0]) {
				return nil, errTruncated
			}
			if b[0] == 0 {
				return nil, fmt.Errorf("%w: empty literal header name", ErrMalformed)
			}
			name, b = string(b[1:1+b[0]]), b[1+b[0]:]
		case int(idx) < len(StaticTable):
			name = StaticTable[idx]
		default:
			return nil, fmt.Errorf("%w: header index %d", ErrMalformed, idx)
		}
		if len(b) < 2 {
			return nil, errTruncated
		}
		n := 2 + int(binary.BigEndian.Uint16(b))
		if len(b) < n {
			return nil, errTruncated
		}
		h = append(h, Field{name, string(b[2:n])})
		b = b[n:]
	}
	return h, nil
}
