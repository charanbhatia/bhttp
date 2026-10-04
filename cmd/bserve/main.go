// Command bserve serves the files under ROOT over bhttp/1 (README §7).
package main

import (
	"io"
	"log"
	"mime"
	"net"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"bhttp/proto"
)

const textPlain = "text/plain; charset=utf-8"

func main() {
	if len(os.Args) != 3 {
		log.Fatal("usage: bserve ROOT PORT")
	}
	root, err := os.OpenRoot(os.Args[1])
	if err != nil {
		log.Fatal(err)
	}
	ln, err := net.Listen("tcp", ":"+os.Args[2])
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("serving %s on %s", os.Args[1], ln.Addr())
	log.Fatal(serve(ln, root))
}

// ponytail: any Accept error stops the server; retry with backoff if it must survive fd exhaustion.
func serve(ln net.Listener, root *os.Root) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			return err
		}
		go handle(c, root)
	}
}

// handle answers requests on one connection, in order, until the client closes it.
func handle(c net.Conn, root *os.Root) {
	defer c.Close()
	for {
		req, err := proto.ReadMessage(c, io.Discard)
		if err != nil {
			return
		}
		status, ctype, body := lookup(root, req)
		log.Printf("%s %s %s %d", c.RemoteAddr(), req.Get(":method"), req.Get(":path"), status)
		if err := writeResponse(c, status, ctype, body); err != nil {
			return
		}
	}
}

// lookup maps a request to a file under root. os.Root refuses any path that
// leaves root, whether through ".." or a symlink.
func lookup(root *os.Root, req proto.Headers) (status int, ctype string, body []byte) {
	if req.Get(":method") != "GET" {
		return 405, textPlain, []byte("405 method not allowed\n")
	}
	p := req.Get(":path")
	if strings.HasSuffix(p, "/") {
		p += "index.html"
	}
	// ponytail: whole file in memory; stream it in MaxPayload chunks if files get large.
	b, err := root.ReadFile(strings.TrimPrefix(p, "/"))
	if err != nil {
		return 404, textPlain, []byte("404 not found\n")
	}
	ctype = mime.TypeByExtension(path.Ext(p))
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	return 200, ctype, b
}

// writeResponse sends HEADERS, then the body in DATA frames of at most MaxPayload bytes.
func writeResponse(w io.Writer, status int, ctype string, body []byte) error {
	block, err := proto.EncodeHeaders(proto.Headers{
		{Name: ":status", Value: strconv.Itoa(status)},
		{Name: "content-type", Value: ctype},
		{Name: "content-length", Value: strconv.Itoa(len(body))},
		{Name: "server", Value: "bserve/1"},
		{Name: "date", Value: time.Now().UTC().Format("Mon, 02 Jan 2006 15:04:05 GMT")},
	})
	if err != nil {
		return err
	}
	frames := []proto.Frame{{Type: proto.TypeHeaders, Payload: block}}
	for len(body) > 0 {
		n := min(len(body), proto.MaxPayload)
		frames = append(frames, proto.Frame{Type: proto.TypeData, Payload: body[:n]})
		body = body[n:]
	}
	frames[len(frames)-1].Flags = proto.FlagEnd
	for _, f := range frames {
		if _, err := w.Write(f.Encode()); err != nil {
			return err
		}
	}
	return nil
}
