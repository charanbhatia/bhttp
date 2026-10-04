// Package proto implements bhttp/1 as specified in README.md.
package proto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	Version    = 0x01
	HeaderSize = 8
	MaxPayload = 16384

	TypeData    = 0x00
	TypeHeaders = 0x01

	FlagEnd = 0x01
)

var ErrMalformed = errors.New("malformed")

type Frame struct {
	Type, Flags byte
	Payload     []byte
}

// Encode returns the frame exactly as it goes on the wire.
func (f Frame) Encode() []byte {
	b := make([]byte, HeaderSize, HeaderSize+len(f.Payload))
	b[0], b[1], b[2] = Version, f.Type, f.Flags
	binary.BigEndian.PutUint32(b[4:], uint32(len(f.Payload)))
	return append(b, f.Payload...)
}

// ReadFrame reads one frame of any type. It returns io.EOF only when r ends
// cleanly between frames.
func ReadFrame(r io.Reader) (Frame, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return Frame{}, err
	}
	if h[0] != Version {
		return Frame{}, fmt.Errorf("%w: version %d", ErrMalformed, h[0])
	}
	n := binary.BigEndian.Uint32(h[4:])
	if n > MaxPayload {
		return Frame{}, fmt.Errorf("%w: length %d > %d", ErrMalformed, n, MaxPayload)
	}
	f := Frame{Type: h[1], Flags: h[2], Payload: make([]byte, n)}
	if _, err := io.ReadFull(r, f.Payload); err != nil {
		if err == io.EOF {
			err = io.ErrUnexpectedEOF
		}
		return Frame{}, err
	}
	return f, nil
}
