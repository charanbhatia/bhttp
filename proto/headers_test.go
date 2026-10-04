package proto

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

var specRequestHeaders = Headers{
	{":method", "GET"},
	{":path", "/index.html"},
	{"host", "localhost:9000"},
	{"user-agent", "bcurl/1"},
	{"accept", "*/*"},
}

func TestEncodeHeadersMatchesSpec(t *testing.T) {
	got, err := EncodeHeaders(specRequestHeaders)
	if want := unhex(t, specRequest)[HeaderSize:]; err != nil || !bytes.Equal(got, want) {
		t.Fatalf("got  % x, %v\nwant % x", got, err, want)
	}
}

func TestDecodeHeadersSpecExample(t *testing.T) {
	h, err := DecodeHeaders(unhex(t, specRequest)[HeaderSize:])
	if err != nil || !slices.Equal(h, specRequestHeaders) {
		t.Fatalf("got %v, %v", h, err)
	}
	if h.Get(":path") != "/index.html" || h.Get("date") != "" {
		t.Fatalf("Get: %q %q", h.Get(":path"), h.Get("date"))
	}
}

func TestStaticTableMatchesSpec(t *testing.T) {
	spec := []string{":method", ":path", ":status", "host", "user-agent", "accept", "content-type", "content-length", "server", "date"}
	for i, name := range spec {
		b, err := EncodeHeaders(Headers{{name, ""}})
		if want := []byte{byte(i + 1), 0, 0}; err != nil || !bytes.Equal(b, want) {
			t.Errorf("%s: got % x, %v; want % x", name, b, err, want)
		}
	}
}

func TestLiteralName(t *testing.T) {
	wire := unhex(t, "00 05 78 2d 66 6f 6f 00 03 62 61 72") // literal "x-foo": "bar"
	h := Headers{{"x-foo", "bar"}}
	if b, err := EncodeHeaders(h); err != nil || !bytes.Equal(b, wire) {
		t.Fatalf("encode: got % x, %v", b, err)
	}
	if got, err := DecodeHeaders(wire); err != nil || !slices.Equal(got, h) {
		t.Fatalf("decode: got %v, %v", got, err)
	}
}

func TestDecodeEdgeCases(t *testing.T) {
	h, err := DecodeHeaders(unhex(t, "09 00 00 03 00 03 32 30 30 03 00 03 34 30 34"))
	if err != nil || h.Get("server") != "" || len(h) != 3 || h.Get(":status") != "200" {
		t.Fatalf("empty value / first of repeated name wins: got %v, %v", h, err)
	}
	if h, err := DecodeHeaders(nil); err != nil || len(h) != 0 {
		t.Fatalf("empty block: got %v, %v", h, err)
	}
}

func TestDecodeMalformed(t *testing.T) {
	for _, wire := range []string{
		"0b 00 00",             // index 11
		"ff 00 00",             // index 255
		"00 00 00 00",          // literal name, NameLen 0
		"00",                   // literal, no NameLen
		"00 05 78 2d",          // name runs past the end
		"01",                   // no ValueLen
		"01 00",                // half a ValueLen
		"01 00 05 47 45",       // value runs past the end
		"01 00 03 47 45 54 0b", // good field, then index 11
	} {
		if _, err := DecodeHeaders(unhex(t, wire)); !errors.Is(err, ErrMalformed) {
			t.Errorf("% s: got %v, want ErrMalformed", wire, err)
		}
	}
}

func TestEncodeRejects(t *testing.T) {
	for name, h := range map[string]Headers{
		"empty name":   {{"", "x"}},
		"long name":    {{strings.Repeat("x", 256), ""}},
		"65536 value":  {{":path", strings.Repeat("x", 65536)}},
		"over the cap": {{":path", strings.Repeat("x", MaxPayload)}},
	} {
		if _, err := EncodeHeaders(h); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}
