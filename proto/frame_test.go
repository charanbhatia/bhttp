package proto

import (
	"bytes"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
)

// specRequest is the example from README §10, copied byte for byte.
const specRequest = `
01 01 01 00 00 00 00 35
01 00 03 47 45 54
02 00 0b 2f 69 6e 64 65 78 2e 68 74 6d 6c
04 00 0e 6c 6f 63 61 6c 68 6f 73 74 3a 39 30 30 30
05 00 07 62 63 75 72 6c 2f 31
06 00 03 2a 2f 2a`

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEncodeMatchesSpec(t *testing.T) {
	wire := unhex(t, specRequest)
	got := Frame{Type: TypeHeaders, Flags: FlagEnd, Payload: wire[HeaderSize:]}.Encode()
	if !bytes.Equal(got, wire) {
		t.Fatalf("got  % x\nwant % x", got, wire)
	}
}

func TestReadFrameSpecExample(t *testing.T) {
	wire := unhex(t, specRequest)
	f, err := ReadFrame(bytes.NewReader(wire))
	if err != nil {
		t.Fatal(err)
	}
	if f.Type != TypeHeaders || f.Flags != FlagEnd || !bytes.Equal(f.Payload, wire[HeaderSize:]) {
		t.Fatalf("got %+v", f)
	}
}

func TestReadFrameBackToBack(t *testing.T) {
	r := bytes.NewReader(unhex(t, `
		01 00 00 00 00 00 00 02 68 69
		01 00 01 00 00 00 00 00
		01 7f ff 5a 00 00 00 01 21`)) // unknown type, unknown flags, non-zero reserved
	want := []Frame{
		{TypeData, 0, []byte("hi")},
		{TypeData, FlagEnd, []byte{}},
		{0x7f, 0xff, []byte("!")},
	}
	for i, w := range want {
		f, err := ReadFrame(r)
		if err != nil || f.Type != w.Type || f.Flags != w.Flags || !bytes.Equal(f.Payload, w.Payload) {
			t.Fatalf("frame %d: got %+v, %v; want %+v", i, f, err, w)
		}
	}
	if _, err := ReadFrame(r); err != io.EOF {
		t.Fatalf("after last frame: got %v, want io.EOF", err)
	}
}

func TestReadFrameMaxPayload(t *testing.T) {
	wire := append(unhex(t, "01 00 01 00 00 00 40 00"), make([]byte, MaxPayload)...)
	if f, err := ReadFrame(bytes.NewReader(wire)); err != nil || len(f.Payload) != MaxPayload {
		t.Fatalf("16384-byte payload: got len %d, %v", len(f.Payload), err)
	}
}

func TestReadFrameErrors(t *testing.T) {
	for _, c := range []struct {
		name, wire string
		want       error
	}{
		{"version 2", "02 00 01 00 00 00 00 00", ErrMalformed},
		{"version 0", "00 00 01 00 00 00 00 00", ErrMalformed},
		{"length 16385", "01 00 01 00 00 00 40 01", ErrMalformed},
		{"length 4G", "01 00 01 00 ff ff ff ff", ErrMalformed},
		{"truncated header", "01 00 01", io.ErrUnexpectedEOF},
		{"no payload", "01 00 01 00 00 00 00 02", io.ErrUnexpectedEOF},
		{"short payload", "01 00 01 00 00 00 00 02 68", io.ErrUnexpectedEOF},
	} {
		if _, err := ReadFrame(bytes.NewReader(unhex(t, c.wire))); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}
