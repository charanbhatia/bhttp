// Command bcurl fetches URLs over a single bhttp/1 connection (README §8).
package main

import (
	"encoding/binary"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"

	"bhttp/proto"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run returns the exit code: 0 if every status is below 400, 1 if any is 4xx
// or 5xx, 2 on a usage, connection or protocol error.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("bcurl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	verbose := fs.Bool("v", false, "hexdump every frame sent (>) and received (<) to stderr")
	fs.Usage = func() { fmt.Fprintln(stderr, "usage: bcurl [-v] HOST:PORT/PATH ...") }
	if err := fs.Parse(args); err != nil || fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	host, _, _ := strings.Cut(fs.Arg(0), "/")
	var paths []string
	for _, u := range fs.Args() {
		h, p, _ := strings.Cut(u, "/")
		if h != host {
			fmt.Fprintf(stderr, "bcurl: %s: every URL must use %s, there is only one connection\n", u, host)
			return 2
		}
		paths = append(paths, "/"+p)
	}

	c, err := net.Dial("tcp", host)
	if err != nil {
		fmt.Fprintln(stderr, "bcurl:", err)
		return 2
	}
	defer c.Close()
	var rw io.ReadWriter = c
	if *verbose {
		rw = struct {
			io.Reader
			io.Writer
		}{io.TeeReader(c, &dumper{w: stderr, mark: "<"}), io.MultiWriter(c, &dumper{w: stderr, mark: ">"})}
	}
	code := 0
	for _, p := range paths {
		status, err := fetch(rw, host, p, stdout)
		if err != nil {
			fmt.Fprintf(stderr, "bcurl: %s: %v\n", p, err)
			return 2
		}
		if status >= 400 {
			code = 1
		}
	}
	return code
}

// fetch sends one GET and copies the response body to body.
func fetch(c io.ReadWriter, host, path string, body io.Writer) (int, error) {
	block, err := proto.EncodeHeaders(proto.Headers{
		{Name: ":method", Value: "GET"},
		{Name: ":path", Value: path},
		{Name: "host", Value: host},
		{Name: "user-agent", Value: "bcurl/1"},
		{Name: "accept", Value: "*/*"},
	})
	if err != nil {
		return 0, err
	}
	if _, err := c.Write(proto.Frame{Type: proto.TypeHeaders, Flags: proto.FlagEnd, Payload: block}.Encode()); err != nil {
		return 0, err
	}
	h, err := proto.ReadMessage(c, body)
	if err != nil {
		return 0, err
	}
	status, err := strconv.Atoi(h.Get(":status"))
	if err != nil || status < 100 || status > 599 {
		return 0, fmt.Errorf("%w: :status %q", proto.ErrMalformed, h.Get(":status"))
	}
	return status, nil
}

// dumper hexdumps every complete frame written to it. Bytes may arrive in any
// chunking; a frame is printed as soon as its last byte is in.
type dumper struct {
	w    io.Writer
	mark string
	buf  []byte
}

func (d *dumper) Write(p []byte) (int, error) {
	d.buf = append(d.buf, p...)
	for len(d.buf) >= proto.HeaderSize {
		n := proto.HeaderSize + int(binary.BigEndian.Uint32(d.buf[4:]))
		if len(d.buf) < n {
			break
		}
		d.dump(d.buf[:n])
		d.buf = d.buf[n:]
	}
	return len(p), nil
}

func (d *dumper) dump(frame []byte) {
	typ, flags, payload := frame[1], frame[2], frame[proto.HeaderSize:]
	name := map[byte]string{proto.TypeData: "DATA", proto.TypeHeaders: "HEADERS"}[typ]
	if name == "" {
		name = fmt.Sprintf("type=0x%02x (unknown, skipped)", typ)
	}
	fmt.Fprintf(d.w, "%s %s flags=0x%02x length=%d\n", d.mark, name, flags, len(payload))
	for line := range strings.Lines(hex.Dump(frame)) {
		fmt.Fprintf(d.w, "%s %s", d.mark, line)
	}
	if typ == proto.TypeHeaders {
		h, _ := proto.DecodeHeaders(payload)
		for _, f := range h {
			fmt.Fprintf(d.w, "%s   %s: %s\n", d.mark, f.Name, f.Value)
		}
	}
}
