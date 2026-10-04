package proto

import (
	"fmt"
	"io"
)

// ReadMessage reads one message (README §4): a HEADERS frame, then DATA frames
// up to the frame carrying END. Body bytes are copied to body. Frames of unknown
// type are skipped wherever they appear. It returns io.EOF only when r ends
// cleanly before the message starts.
func ReadMessage(r io.Reader, body io.Writer) (Headers, error) {
	var h Headers
	open := false
	for {
		f, err := ReadFrame(r)
		if err == io.EOF && open {
			err = io.ErrUnexpectedEOF
		}
		if err != nil {
			return nil, err
		}
		switch f.Type {
		case TypeHeaders:
			if open {
				return nil, fmt.Errorf("%w: HEADERS inside a message", ErrMalformed)
			}
			if h, err = DecodeHeaders(f.Payload); err != nil {
				return nil, err
			}
			open = true
		case TypeData:
			if !open {
				return nil, fmt.Errorf("%w: DATA before HEADERS", ErrMalformed)
			}
			if _, err := body.Write(f.Payload); err != nil {
				return nil, err
			}
		default:
			continue // unknown type: skipped, flags and all (README §3)
		}
		if f.Flags&FlagEnd != 0 {
			return h, nil
		}
	}
}
