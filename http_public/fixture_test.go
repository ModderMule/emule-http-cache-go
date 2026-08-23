package http_public

import (
	"bytes"
	"crypto/rand"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/log"
	"github.com/ModderMule/emule-http-cache-go/pkg/storage"
)

// cipherSize is the ciphertext of one full eMule part: PARTSIZE plus the whole
// extra block PKCS#7 appends to an exact multiple of the block size.
const cipherSize = 9_728_016

// testConfig builds a Config rooted at a fresh temp directory, with the storage
// limits every test in this package needs. The directory is returned so a
// caller can stage other files beside the data dirs.
func testConfig(t *testing.T) (*config.Config, string) {
	t.Helper()

	dir := t.TempDir()

	cfg := &config.Config{}
	cfg.Server.Mode = "test"
	cfg.Storage.DataDir = filepath.Join(dir, "storage")
	cfg.Storage.VarDir = filepath.Join(dir, "var")
	cfg.Storage.MaxChunkSize = 10 << 20
	cfg.Storage.DefaultTTL = 48 * time.Hour
	cfg.Storage.MaxTTL = 168 * time.Hour

	return cfg, dir
}

// chunkServer is a running server with one chunk already stored, plus
// everything a test needs to make assertions about that chunk.
//
// payload is kept so a test can derive the expected digest from the same bytes
// the server was handed, rather than reading meta.SHA256 back out — otherwise
// an ETag assertion only proves the store is self-consistent, not that the
// header is what the client will use to revalidate.
type chunkServer struct {
	*httptest.Server

	cfg     *config.Config
	store   *storage.Store
	quota   *storage.Quota
	payload []byte
	meta    *storage.ChunkMeta
}

// newChunkServer starts a server holding one random chunk of the given size,
// stored with the given TTL. A negative ttl stores an already-expired chunk.
func newChunkServer(t *testing.T, size int, ttl time.Duration) *chunkServer {
	t.Helper()

	cfg, _ := testConfig(t)

	store := storage.NewStore(cfg)
	quota := storage.NewQuota(cfg)

	payload := make([]byte, size)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("generating a payload: %v", err)
	}

	meta, err := store.Ingest(bytes.NewReader(payload), "test", ttl, int64(size))
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

	return &chunkServer{
		Server:  ts,
		cfg:     cfg,
		store:   store,
		quota:   quota,
		payload: payload,
		meta:    meta,
	}
}

// chunkPath is the route of the stored chunk.
func (s *chunkServer) chunkPath() string {
	return "/v1/chunks/" + s.meta.ID
}

// do sends one request and reads the whole body. A body that fails to read is
// not a fatal error: the abort paths in streamRange are supposed to cut a
// response short, and a test needs to see how far it got.
func (s *chunkServer) do(t *testing.T, method, path string, header http.Header) (*http.Response, []byte, error) {
	t.Helper()

	req, err := http.NewRequest(method, s.URL+path, nil)
	if err != nil {
		t.Fatalf("building a %s %s request: %v", method, path, err)
	}
	for name, values := range header {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	body, readErr := io.ReadAll(resp.Body)

	return resp, body, readErr
}

// get is do() for the common case, where the body must arrive intact.
func (s *chunkServer) get(t *testing.T, path string, header http.Header) (*http.Response, []byte) {
	t.Helper()

	resp, body, err := s.do(t, http.MethodGet, path, header)
	if err != nil {
		t.Fatalf("reading the body of GET %s: %v", path, err)
	}

	return resp, body
}

// rangeHeader is the one-header case, which is most of them.
func rangeHeader(spec string) http.Header {
	return http.Header{"Range": {spec}}
}
