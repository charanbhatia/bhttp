// Package e2e runs the real bcurl binary against the real bserve binary.
package e2e

import (
	"bytes"
	"encoding/hex"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"bhttp/proto"
)

func TestBcurlAgainstBserve(t *testing.T) {
	bin, dir := t.TempDir(), t.TempDir()
	if out, err := exec.Command("go", "build", "-o", bin+"/", "../cmd/...").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	index := "<h1>e2e</h1>\n"
	big := strings.Repeat("0123456789abcdef", 3000) // 48000 bytes: 3 DATA frames
	os.Mkdir(filepath.Join(dir, "www"), 0o755)
	os.WriteFile(filepath.Join(dir, "www", "index.html"), []byte(index), 0o644)
	os.WriteFile(filepath.Join(dir, "www", "big.txt"), []byte(big), 0o644)
	os.WriteFile(filepath.Join(dir, "secret.txt"), []byte("TOP SECRET"), 0o644)

	ln, err := net.Listen("tcp", "127.0.0.1:0") // borrow a free port
	if err != nil {
		t.Fatal(err)
	}
	port := strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	ln.Close()
	srvLog, _ := os.Create(filepath.Join(dir, "bserve.log"))
	srv := exec.Command(filepath.Join(bin, "bserve"), filepath.Join(dir, "www"), port)
	srv.Stderr = srvLog
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Process.Kill(); srv.Wait() })
	host := "localhost:" + port
	for i := 0; ; i++ {
		if c, err := net.Dial("tcp", host); err == nil {
			c.Close()
			break
		}
		if i == 50 {
			t.Fatal("bserve did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}

	bcurl := func(args ...string) (int, string, string) {
		var out, errs bytes.Buffer
		cmd := exec.Command(filepath.Join(bin, "bcurl"), args...)
		cmd.Stdout, cmd.Stderr = &out, &errs
		cmd.Run()
		return cmd.ProcessState.ExitCode(), out.String(), errs.String()
	}

	t.Run("-v on the slide's command", func(t *testing.T) {
		code, out, errs := bcurl("-v", host+"/index.html")
		if code != 0 || out != index {
			t.Fatalf("got exit %d, %q", code, out)
		}
		for _, want := range []string{"> HEADERS flags=0x01", ">   :path: /index.html", "<   :status: 200", "< DATA flags=0x01 length=13"} {
			if !strings.Contains(errs, want) {
				t.Errorf("stderr is missing %q", want)
			}
		}
	})

	t.Run("big file", func(t *testing.T) {
		if code, out, _ := bcurl(host + "/big.txt"); code != 0 || out != big {
			t.Fatalf("got exit %d, %d bytes", code, len(out))
		}
	})

	t.Run("nothing outside the root", func(t *testing.T) {
		if code, out, _ := bcurl(host + "/../secret.txt"); code != 1 || strings.Contains(out, "SECRET") {
			t.Fatalf("got exit %d, %q", code, out)
		}
	})

	t.Run("one connection for many URLs", func(t *testing.T) {
		code, out, _ := bcurl(host+"/", host+"/nope", host+"/index.html")
		if code != 1 || out != index+"404 not found\n"+index {
			t.Fatalf("got exit %d, %q", code, out)
		}
		// bserve logs "CLIENT_ADDR METHOD PATH STATUS"; all three must share CLIENT_ADDR.
		logged, _ := os.ReadFile(srvLog.Name())
		m := regexp.MustCompile(`(\S+) GET (?:/|/nope|/index\.html) (?:200|404)\n`).FindAllStringSubmatch(string(logged), -1)
		if len(m) < 3 || m[len(m)-1][1] != m[len(m)-2][1] || m[len(m)-2][1] != m[len(m)-3][1] {
			t.Fatalf("expected the last 3 requests on one connection:\n%s", logged)
		}
	})
}

// The README's annotated hexdump must be real bhttp/1: rebuild its bytes from
// the table rows, check every offset, and decode a request then a response
// whose body is the shipped www/index.html.
func TestReadmeHexdump(t *testing.T) {
	readme, err := os.ReadFile("../README.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(readme), "\n## Annotated hexdump\n")
	if !ok {
		t.Fatal("README has no annotated hexdump")
	}
	var wire []byte
	start := 0
	for _, m := range regexp.MustCompile(`(?m)^([0-9a-f]{2})  ((?:[0-9a-f]{2} )*[0-9a-f]{2})(?:  |$)`).FindAllStringSubmatch(section, -1) {
		off, _ := strconv.ParseUint(m[1], 16, 8)
		if off == 0 {
			start = len(wire)
		}
		if int(off) != len(wire)-start {
			t.Fatalf("row %q: offset should be %02x", m[0], len(wire)-start)
		}
		b, _ := hex.DecodeString(strings.ReplaceAll(m[2], " ", ""))
		wire = append(wire, b...)
	}

	r := bytes.NewReader(wire)
	req, err := proto.ReadMessage(r, io.Discard)
	if err != nil || req.Get(":method") != "GET" || req.Get(":path") != "/index.html" {
		t.Fatalf("request: %v %v", req, err)
	}
	var body bytes.Buffer
	res, err := proto.ReadMessage(r, &body)
	index, _ := os.ReadFile("../www/index.html")
	if err != nil || res.Get(":status") != "200" || body.String() != string(index) || res.Get("content-length") != strconv.Itoa(len(index)) {
		t.Fatalf("response: %v %q %v", res, body.String(), err)
	}
	if r.Len() != 0 {
		t.Fatalf("%d bytes left over after the response", r.Len())
	}
}
