package main

import (
	"bytes"
	"encoding/hex"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"bhttp/proto"
)

const indexHTML = "<h1>hi</h1>\n"

// start serves a fresh root on a loopback port. Next to the root sits
// secret.txt, which no request may reach.
func start(t *testing.T, idle time.Duration) (addr, secret string) {
	dir := t.TempDir()
	www := filepath.Join(dir, "www")
	secret = filepath.Join(dir, "secret.txt")
	for name, data := range map[string]string{
		secret:                               "TOP SECRET",
		filepath.Join(www, "index.html"):     indexHTML,
		filepath.Join(www, "sub/index.html"): "sub\n",
		filepath.Join(www, "big.bin"):        strings.Repeat("x", 2*proto.MaxPayload+7232),
	} {
		os.MkdirAll(filepath.Dir(name), 0o755)
		if err := os.WriteFile(name, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	os.Symlink("../secret.txt", filepath.Join(www, "escape"))
	root, err := os.OpenRoot(www)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go serve(ln, root, idle)
	return ln.Addr().String(), secret
}

func dial(t *testing.T, addr string) net.Conn {
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() { c.Close() })
	return c
}

// do sends raw bytes and reads one response message.
func do(t *testing.T, c net.Conn, wire []byte) (proto.Headers, string) {
	t.Helper()
	if _, err := c.Write(wire); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	h, err := proto.ReadMessage(c, &body)
	if err != nil {
		t.Fatal(err)
	}
	return h, body.String()
}

func request(t *testing.T, method, path string) []byte {
	block, err := proto.EncodeHeaders(proto.Headers{{Name: ":method", Value: method}, {Name: ":path", Value: path}})
	if err != nil {
		t.Fatal(err)
	}
	return proto.Frame{Type: proto.TypeHeaders, Flags: proto.FlagEnd, Payload: block}.Encode()
}

// The README §10 request, byte for byte.
func TestSpecRequest(t *testing.T) {
	addr, _ := start(t, time.Minute)
	wire, _ := hex.DecodeString("0101010000000035" + "010003474554" + "02000b2f696e6465782e68746d6c" +
		"04000e6c6f63616c686f73743a39303030" + "050007626375726c2f31" + "0600032a2f2a")
	h, body := do(t, dial(t, addr), wire)
	if h.Get(":status") != "200" || body != indexHTML {
		t.Fatalf("got %v %q", h, body)
	}
	if h.Get("content-type") != "text/html; charset=utf-8" || h.Get("content-length") != strconv.Itoa(len(indexHTML)) || h.Get("server") != "bserve/1" {
		t.Fatalf("headers: %v", h)
	}
	if _, err := time.Parse(time.RFC1123, h.Get("date")); err != nil {
		t.Fatalf("date: %v", err)
	}
}

// Every request below goes over the same connection: the server must keep it open.
func TestOneConnection(t *testing.T) {
	addr, _ := start(t, time.Minute)
	c := dial(t, addr)
	for _, r := range []struct {
		method, path, status, body string
	}{
		{"GET", "/index.html", "200", indexHTML},
		{"GET", "/", "200", indexHTML},
		{"GET", "/sub/", "200", "sub\n"},
		{"GET", "/sub/../index.html", "200", indexHTML},
		{"GET", "/missing.html", "404", "404 not found\n"},
		{"GET", "/sub", "404", "404 not found\n"}, // a directory, not a file
		{"POST", "/index.html", "405", "405 method not allowed\n"},
		{"GET", "/big.bin", "200", strings.Repeat("x", 2*proto.MaxPayload+7232)},
	} {
		h, body := do(t, c, request(t, r.method, r.path))
		if h.Get(":status") != r.status || body != r.body || h.Get("content-length") != strconv.Itoa(len(body)) {
			t.Errorf("%s %s: got %v, %d-byte body", r.method, r.path, h, len(body))
		}
	}
}

func TestNothingOutsideRoot(t *testing.T) {
	addr, secret := start(t, time.Minute)
	c := dial(t, addr)
	for _, p := range []string{"/../secret.txt", "/sub/../../secret.txt", "/" + secret, "//" + secret, "/escape"} {
		if h, body := do(t, c, request(t, "GET", p)); h.Get(":status") != "404" || strings.Contains(body, "SECRET") {
			t.Errorf("%s: got %v %q", p, h, body)
		}
	}
}

// A request body is read and thrown away, so the next request still lines up.
func TestRequestBodyDiscarded(t *testing.T) {
	addr, _ := start(t, time.Minute)
	c := dial(t, addr)
	block, _ := proto.EncodeHeaders(proto.Headers{{Name: ":method", Value: "POST"}, {Name: ":path", Value: "/"}})
	post := append(proto.Frame{Type: proto.TypeHeaders, Payload: block}.Encode(),
		proto.Frame{Type: proto.TypeData, Flags: proto.FlagEnd, Payload: []byte("form=1")}.Encode()...)
	if h, _ := do(t, c, post); h.Get(":status") != "405" {
		t.Fatalf("POST: got %v", h)
	}
	if h, body := do(t, c, request(t, "GET", "/")); h.Get(":status") != "200" || body != indexHTML {
		t.Fatalf("GET after POST: got %v %q", h, body)
	}
}

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// closed reports whether the server has closed c.
func closed(c net.Conn) bool {
	_, err := proto.ReadFrame(c)
	return err == io.EOF
}

func TestMalformedGets400ThenClose(t *testing.T) {
	addr, _ := start(t, time.Minute)
	for _, m := range []struct{ name, wire string }{
		{"version 2", "02 01 01 00 00 00 00 00"},
		{"length 16385", "01 01 01 00 00 00 40 01" + strings.Repeat(" ff", 100)},
		{"DATA first", "01 00 01 00 00 00 00 02 68 69"},
		{"header index 11", "01 01 01 00 00 00 00 03 0b 00 00"},
		{"truncated field", "01 01 01 00 00 00 00 02 01 00"},
		{"HEADERS twice", "01 01 00 00 00 00 00 06 01 00 03 47 45 54 01 01 01 00 00 00 00 00"},
		{"no :method", "01 01 01 00 00 00 00 04 02 00 01 2f"},
		{":path without /", "01 01 01 00 00 00 00 0a 01 00 03 47 45 54 02 00 01 78"},
	} {
		c := dial(t, addr)
		h, body := do(t, c, unhex(t, m.wire))
		if h.Get(":status") != "400" || !strings.HasPrefix(body, "400 bad request: malformed") {
			t.Errorf("%s: got %v %q", m.name, h, body)
		}
		if !closed(c) {
			t.Errorf("%s: connection still open after 400", m.name)
		}
	}
}

// Unknown frame types are skipped before a request, inside a request body and
// between requests, all on one connection.
func TestSkipsUnknownFrames(t *testing.T) {
	addr, _ := start(t, time.Minute)
	c := dial(t, addr)
	unknown := unhex(t, "01 7f 01 00 00 00 00 02 ff ff") // END set, and still ignored
	block, _ := proto.EncodeHeaders(proto.Headers{{Name: ":method", Value: "GET"}, {Name: ":path", Value: "/"}})
	open := proto.Frame{Type: proto.TypeHeaders, Payload: block}.Encode()
	end := proto.Frame{Type: proto.TypeData, Flags: proto.FlagEnd}.Encode()
	for i, wire := range [][]byte{
		slices.Concat(unknown, unknown, request(t, "GET", "/")),
		slices.Concat(request(t, "GET", "/"), unknown),
		slices.Concat(open, unknown, end),
	} {
		if h, body := do(t, c, wire); h.Get(":status") != "200" || body != indexHTML {
			t.Errorf("wire %d: got %v %q", i, h, body)
		}
	}
}

func TestIdleTimeout(t *testing.T) {
	addr, _ := start(t, 200*time.Millisecond)
	for name, wire := range map[string]string{
		"silent client":   "",
		"frame cut short": "01 01 01 00 00 00 00 0f 01 00 03 47 45 54", // promises 15 bytes, sends 6
	} {
		c := dial(t, addr)
		c.Write(unhex(t, wire))
		if !closed(c) {
			t.Errorf("%s: not closed after idle timeout", name)
		}
	}
}
