package http_public

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/storage"
	"github.com/ModderMule/emule-http-cache-go/log"
)

// The client's own limits, from src/core/net/HttpClientReqSocket.h. Exceeding
// the total drops the connection; exceeding the line limit silently truncates
// the line, which corrupts a Content-Range rather than rejecting it.
const (
	clientMaxHeaderLine  = 1024
	clientMaxHeaderTotal = 2048
)

// cipherSize is the ciphertext of one full eMule part.
const cipherSize = 9_728_016

// newTestServer starts a real listener with one chunk stored, and returns the
// server plus that chunk's id.
func newTestServer(t *testing.T, size int) (*httptest.Server, string) {
	t.Helper()

	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	cfg.Storage.DataDir = filepath.Join(dir, "storage")
	cfg.Storage.VarDir = filepath.Join(dir, "var")
	cfg.Storage.MaxChunkSize = 10 * 1024 * 1024
	cfg.Storage.DefaultTTL = 48 * time.Hour
	cfg.Storage.MaxTTL = 168 * time.Hour

	store := storage.NewStore(cfg)
	quota := storage.NewQuota(cfg)

	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("generating a payload: %v", err)
	}

	meta, err := store.Ingest(bytes.NewReader(payload), "test", time.Hour, int64(size))
	if err != nil {
		t.Fatalf("storing a chunk: %v", err)
	}

	srv, err := New(Deps{
		Config: cfg, Store: store, Quota: quota,
		GC: storage.NewGc(cfg, store, quota), Logger: log.NewNop(), Installed: true,
	})
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts, meta.ID
}

// TestRawWireResponse is the assertion an httptest round trip structurally
// cannot make.
//
// net/http's client decodes Transfer-Encoding: chunked transparently and hands
// back a correct body, so a server that had silently switched to chunked would
// look perfect through it. eMuleQt does not: it reads chunk responses over a
// hand-built socket with no chunked decoder, and would feed the hex length
// lines straight into SHA-256 and AES-CBC. This dials the socket itself and
// reads the bytes on the wire.
func TestRawWireResponse(t *testing.T) {
	ts, id := newTestServer(t, cipherSize)

	target, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parsing the test server URL: %v", err)
	}

	// Byte for byte what URLClient::buildGetHeader emits, Range included: the
	// real client sends one on every request, so the normal success is a 206.
	request := fmt.Sprintf(
		"GET /v1/chunks/%s HTTP/1.1\r\nHost: %s\r\nUser-Agent: eMuleQt/1.0\r\nAccept: */*\r\n"+
			"Connection: keep-alive\r\nRange: bytes=0-%d\r\n\r\n",
		id, target.Host, cipherSize-1)
	t.Logf("input:\n%s", strings.ReplaceAll(request, "\r\n", "\\r\\n\n"))

	conn, err := net.Dial("tcp", target.Host)
	if err != nil {
		t.Fatalf("dialling: %v", err)
	}
	defer conn.Close()

	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("writing the request: %v", err)
	}

	reader := bufio.NewReader(conn)

	// Accounted exactly as HttpClientReqSocket does: the sum of trimmed line
	// lengths, status line included, CRLFs excluded.
	var headerBytes int
	var lines []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("reading headers: %v", err)
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			break
		}

		if len(trimmed) > clientMaxHeaderLine {
			t.Errorf("header line is %d bytes, over the client's %d-byte cap, where it would be silently truncated: %s",
				len(trimmed), clientMaxHeaderLine, trimmed)
		}

		headerBytes += len(trimmed)
		lines = append(lines, trimmed)
	}

	t.Logf("output:\n%s", strings.Join(lines, "\n"))
	t.Logf("header accounting: %d bytes of the client's %d-byte budget", headerBytes, clientMaxHeaderTotal)

	if headerBytes > clientMaxHeaderTotal {
		t.Errorf("response headers total %d bytes, over the client's %d-byte cap — it drops the connection at that point",
			headerBytes, clientMaxHeaderTotal)
	}

	joined := strings.ToLower(strings.Join(lines, "\n"))
	if strings.Contains(joined, "transfer-encoding") {
		t.Errorf("the response is chunked; the client has no chunked decoder and would treat the framing as ciphertext")
	}

	want := map[string]string{
		"HTTP/1.1 206 Partial Content": "",
		"Content-Length: ":             fmt.Sprint(cipherSize),
		"Content-Range: ":              fmt.Sprintf("bytes 0-%d/%d", cipherSize-1, cipherSize),
	}
	for prefix, value := range want {
		found := false
		for _, line := range lines {
			if strings.HasPrefix(line, prefix) && strings.HasSuffix(line, value) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no response line starting %q and ending %q", prefix, value)
		}
	}

	// Drain the body and confirm the promised length actually arrives.
	body := make([]byte, cipherSize)
	read := 0
	for read < cipherSize {
		n, err := reader.Read(body[read:])
		read += n
		if err != nil {
			break
		}
	}
	t.Logf("body: %d bytes of the promised %d", read, cipherSize)

	if read != cipherSize {
		t.Errorf("body is %d bytes, want %d", read, cipherSize)
	}
}

// TestNoRedirectOnChunkPath pins gin's two redirect defaults off. Either one
// would answer a near-miss path with a 301, and the client treats any 3xx on
// this path as a failed fetch with no retry.
func TestNoRedirectOnChunkPath(t *testing.T) {
	ts, id := newTestServer(t, 4096)

	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	for _, path := range []string{
		"/v1/chunks/" + id + "/",
		"/v1/chunks/" + strings.ToUpper(id),
		"/v1/chunks/not-a-valid-id",
	} {
		t.Logf("input:  GET %s", path)

		resp, err := client.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = resp.Body.Close()

		t.Logf("output: %d", resp.StatusCode)

		if resp.StatusCode != 404 {
			t.Errorf("GET %s = %d, want 404 (a 3xx here is a fatal fetch error for the client)", path, resp.StatusCode)
		}
	}
}
