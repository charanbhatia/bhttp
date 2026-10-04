package proto

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const (
	resHeaders = "01 01 00 00 00 00 00 06  03 00 03 32 30 30" // HEADERS, :status "200"
	dataHel    = "01 00 00 00 00 00 00 03  68 65 6c"          // DATA "hel"
	dataLoEnd  = "01 00 01 00 00 00 00 02  6c 6f"             // DATA END "lo"
	emptyEnd   = "01 00 01 00 00 00 00 00"                    // DATA END, no payload
	unknown    = "01 7f 01 00 00 00 00 02  ff ff"             // type 0x7f with END set: must not end anything
)

func stream(t *testing.T, parts ...string) *bytes.Reader {
	return bytes.NewReader(unhex(t, strings.Join(parts, " ")))
}

func TestReadMessageHeadersOnly(t *testing.T) {
	var body bytes.Buffer
	h, err := ReadMessage(stream(t, specRequest), &body)
	if err != nil || h.Get(":path") != "/index.html" || body.Len() != 0 {
		t.Fatalf("got %v, body %q, %v", h, body.String(), err)
	}
}

func TestReadMessageBody(t *testing.T) {
	for _, c := range []struct {
		name, want string
		parts      []string
	}{
		{"two DATA frames", "hello", []string{resHeaders, dataHel, dataLoEnd}},
		{"empty DATA carries END", "hello", []string{resHeaders, dataHel, "01 00 00 00 00 00 00 02 6c 6f", emptyEnd}},
		{"unknown frames anywhere", "hello", []string{unknown, resHeaders, unknown, dataHel, unknown, unknown, dataLoEnd}},
		{"empty body", "", []string{resHeaders, unknown, emptyEnd}},
	} {
		var body bytes.Buffer
		h, err := ReadMessage(stream(t, c.parts...), &body)
		if err != nil || h.Get(":status") != "200" || body.String() != c.want {
			t.Errorf("%s: got %v, body %q, %v", c.name, h, body.String(), err)
		}
	}
}

// Two messages on one stream, with unknown frames around the boundary: the
// reader must stop exactly at END so the next message is read intact.
func TestReadMessageKeepsAlignment(t *testing.T) {
	r := stream(t, resHeaders, dataLoEnd, unknown, specRequest, unknown)
	for _, want := range []string{":status", ":path"} {
		if h, err := ReadMessage(r, io.Discard); err != nil || h.Get(want) == "" {
			t.Fatalf("want %s: got %v, %v", want, h, err)
		}
	}
	if _, err := ReadMessage(r, io.Discard); err != io.EOF {
		t.Fatalf("after last message: got %v, want io.EOF", err)
	}
}

func TestReadMessageErrors(t *testing.T) {
	for _, c := range []struct {
		name  string
		parts []string
		want  error
	}{
		{"nothing", nil, io.EOF},
		{"only unknown frames", []string{unknown, unknown}, io.EOF},
		{"DATA first", []string{dataLoEnd}, ErrMalformed},
		{"HEADERS twice", []string{resHeaders, specRequest}, ErrMalformed},
		{"bad header block", []string{"01 01 01 00 00 00 00 03 0b 00 00"}, ErrMalformed},
		{"bad frame mid-body", []string{resHeaders, "02 00 01 00 00 00 00 00"}, ErrMalformed},
		{"EOF after HEADERS", []string{resHeaders}, io.ErrUnexpectedEOF},
		{"EOF mid-body", []string{resHeaders, dataHel}, io.ErrUnexpectedEOF},
	} {
		if _, err := ReadMessage(stream(t, c.parts...), io.Discard); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}
