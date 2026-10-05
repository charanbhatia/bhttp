package main

import (
	"bytes"
	"encoding/hex"
	"io"
	"net"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"bhttp/proto"
)

// Responses written out byte by byte from the spec.
const (
	ok200 = "01 01 00 00 00 00 00 06 03 00 03 32 30 30" + // HEADERS, :status "200"
		" 01 7f 01 00 00 00 00 02 ff ff" + // unknown type with END set: skipped
		" 01 00 00 00 00 00 00 03 68 65 6c" + // DATA "hel"
		" 01 7f 00 00 00 00 00 00" + // unknown, empty
		" 01 00 01 00 00 00 00 02 6c 6f" // DATA END "lo"
	notFound404 = "01 01 00 00 00 00 00 06 03 00 03 34 30 34" + // HEADERS, :status "404"
		" 01 00 01 00 00 00 00 05 6e 6f 70 65 0a" // DATA END "nope\n"
	error500 = "01 01 01 00 00 00 00 06 03 00 03 35 30 30" // HEADERS END, :status "500"
)

func unhex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(strings.Join(strings.Fields(s), ""))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// specRequest assembles, field by field, the request the spec says
// `bcurl HOST/index.html` sends.
func specRequest(t *testing.T, host string) []byte {
	payload := slices.Concat(
		unhex(t, "01 00 03"), []byte("GET"),
		unhex(t, "02 00 0b"), []byte("/index.html"),
		[]byte{4, 0, byte(len(host))}, []byte(host),
		unhex(t, "05 00 07"), []byte("bcurl/1"),
		unhex(t, "06 00 03"), []byte("*/*"))
	return slices.Concat(unhex(t, "01 01 01 00 00 00 00"), []byte{byte(len(payload))}, payload)
}

// fake is a server that answers the i-th request on a connection with
// replies[i], then closes. It records the raw request frames and counts connections.
func fake(t *testing.T, replies ...string) (addr string, reqs chan []byte, conns *atomic.Int32) {
	var wire [][]byte
	for _, r := range replies {
		wire = append(wire, unhex(t, r))
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	reqs, conns = make(chan []byte, 10), new(atomic.Int32)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns.Add(1)
			go func() {
				defer c.Close()
				for _, w := range wire {
					var raw bytes.Buffer
					if _, err := proto.ReadFrame(io.TeeReader(c, &raw)); err != nil {
						return
					}
					reqs <- raw.Bytes()
					c.Write(w)
				}
			}()
		}
	}()
	return ln.Addr().String(), reqs, conns
}

func bcurl(args ...string) (code int, stdout, stderr string) {
	var out, errs bytes.Buffer
	code = run(args, &out, &errs)
	return code, out.String(), errs.String()
}

func TestRequestBytesMatchSpec(t *testing.T) {
	readme := unhex(t, "01 01 01 00 00 00 00 35 01 00 03 47 45 54 02 00 0b 2f 69 6e 64 65 78 2e 68 74 6d 6c"+
		"04 00 0e 6c 6f 63 61 6c 68 6f 73 74 3a 39 30 30 30 05 00 07 62 63 75 72 6c 2f 31 06 00 03 2a 2f 2a")
	if !bytes.Equal(specRequest(t, "localhost:9000"), readme) {
		t.Fatal("specRequest disagrees with README §10")
	}
	addr, reqs, _ := fake(t, ok200)
	code, out, errs := bcurl(addr + "/index.html")
	if code != 0 || out != "hello" || errs != "" {
		t.Fatalf("got %d %q %q", code, out, errs)
	}
	if got, want := <-reqs, specRequest(t, addr); !bytes.Equal(got, want) {
		t.Fatalf("request\ngot  % x\nwant % x", got, want)
	}
}

func TestOneConnectionForAllURLs(t *testing.T) {
	addr, reqs, conns := fake(t, ok200, notFound404, ok200)
	code, out, _ := bcurl(addr+"/a", addr+"/b/", addr)
	if code != 1 || out != "hellonope\nhello" || conns.Load() != 1 {
		t.Fatalf("got exit %d, %q, %d connections", code, out, conns.Load())
	}
	for _, want := range []string{"/a", "/b/", "/"} {
		f, _ := proto.ReadFrame(bytes.NewReader(<-reqs))
		if h, _ := proto.DecodeHeaders(f.Payload); h.Get(":path") != want {
			t.Errorf("got :path %q, want %q", h.Get(":path"), want)
		}
	}
}

func TestExitCodes(t *testing.T) {
	refused := func() string { // a port with nothing listening on it
		ln, _ := net.Listen("tcp", "127.0.0.1:0")
		defer ln.Close()
		return ln.Addr().String()
	}()
	for _, c := range []struct {
		name  string
		reply string // "" = close without answering
		args  []string
		want  int
	}{
		{"200", ok200, nil, 0},
		{"404", notFound404, nil, 1},
		{"500", error500, nil, 1},
		{"bad version in reply", "02 01 01 00 00 00 00 00", nil, 2},
		{"no :status", "01 01 01 00 00 00 00 00", nil, 2},
		{":status abc", "01 01 01 00 00 00 00 06 03 00 03 61 62 63", nil, 2},
		{"server hangs up", "", nil, 2},
		{"no URL", ok200, []string{}, 2},
		{"two hosts", ok200, []string{"a:1/x", "b:2/y"}, 2},
		{"connection refused", ok200, []string{refused + "/"}, 2},
	} {
		addr, _, _ := fake(t, c.reply)
		args := c.args
		if args == nil {
			args = []string{addr + "/"}
		}
		if code, _, errs := bcurl(args...); code != c.want {
			t.Errorf("%s: exit %d, want %d (%s)", c.name, code, c.want, errs)
		}
	}
}
