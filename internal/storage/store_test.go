package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()

	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Storage.DataDir = filepath.Join(dir, "storage")
	cfg.Storage.VarDir = filepath.Join(dir, "var")
	cfg.Storage.MaxChunkSize = 1024 * 1024
	cfg.Storage.DefaultTTL = time.Hour
	cfg.Storage.MaxTTL = 24 * time.Hour
	cfg.GC.MaxDeletes = 200

	return cfg
}

// TestSidecarIsByteIdenticalToPHP is the cheapest possible proof that a Go
// server writing into a store a PHP server also reads stays readable by it.
//
// encoding/json marshals in field declaration order, so the order in ChunkMeta
// is a wire format, not a style choice — and json.Marshal is required over
// Encoder.Encode, which would append a newline PHP does not write.
func TestSidecarIsByteIdenticalToPHP(t *testing.T) {
	original, err := os.ReadFile(filepath.Join("testdata", "php-sidecar.json"))
	if err != nil {
		t.Fatalf("reading the PHP-written sidecar: %v", err)
	}
	t.Logf("input:  %s", original)

	var meta ChunkMeta
	if err := json.Unmarshal(original, &meta); err != nil {
		t.Fatalf("a sidecar written by PHP does not decode: %v", err)
	}
	t.Logf("decoded: %+v", meta)

	round, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("re-encoding: %v", err)
	}
	t.Logf("output: %s", round)

	if !bytes.Equal(original, round) {
		t.Errorf("re-encoding changed the sidecar:\n  php: %s\n  go:  %s", original, round)
	}
}

func TestMetaHidesExpiredAndIncompleteChunks(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)

	live, err := store.Ingest(bytes.NewReader([]byte("ciphertext")), "k", time.Hour, 10)
	if err != nil {
		t.Fatalf("storing a live chunk: %v", err)
	}

	expired, err := store.Ingest(bytes.NewReader([]byte("ciphertext")), "k", -time.Hour, 10)
	if err != nil {
		t.Fatalf("storing an expired chunk: %v", err)
	}

	cases := []struct {
		label string
		id    string
		want  bool
	}{
		{"a live chunk resolves", live.ID, true},
		{"an expired chunk reads as absent", expired.ID, false},
		{"an unknown id reads as absent", "00000000000000000000000000000000", false},
		{"a malformed id reads as absent", "../../etc/passwd", false},
		{"an uppercase id reads as absent", "8390934B39F6AFD3BF9F1EEB982A3E25", false},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Logf("input:  %s", tc.id)

			_, ok := store.Meta(tc.id)
			t.Logf("output: found=%t", ok)

			if ok != tc.want {
				t.Errorf("Meta(%q) found = %t, want %t", tc.id, ok, tc.want)
			}
		})
	}

	// An expired chunk is hidden but not deleted: a GET must not do write work.
	t.Run("an expired chunk is hidden, not deleted", func(t *testing.T) {
		if !store.Exists(expired.ID) {
			t.Errorf("Meta deleted an expired chunk; only a sweep may do that")
		}
	})

	// A blob whose sidecar is gone must not resolve either.
	t.Run("a blob with no sidecar reads as absent", func(t *testing.T) {
		if err := os.Remove(store.MetaPath(live.ID)); err != nil {
			t.Fatalf("removing the sidecar: %v", err)
		}

		_, ok := store.Meta(live.ID)
		t.Logf("output: found=%t", ok)

		if ok {
			t.Errorf("a chunk with no sidecar resolved")
		}
	})
}

func TestIngestRefusals(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)

	cases := []struct {
		label    string
		body     []byte
		declared int64
		wantErr  error
		wantCode int
	}{
		{"an empty body", nil, 0, ErrEmptyBody, 400},
		{"a body shorter than its Content-Length", []byte("short"), 999, ErrLengthMismatch, 400},
		{"a body longer than maxChunkSize", make([]byte, cfg.Storage.MaxChunkSize+1), cfg.Storage.MaxChunkSize + 1, ErrTooLarge, 413},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Logf("input:  %d bytes, declared %d", len(tc.body), tc.declared)

			meta, err := store.Ingest(bytes.NewReader(tc.body), "k", time.Hour, tc.declared)
			t.Logf("output: meta=%v err=%v status=%d", meta, err, HTTPStatus(err))

			if !errors.Is(err, tc.wantErr) {
				t.Errorf("error = %v, want %v", err, tc.wantErr)
			}
			if HTTPStatus(err) != tc.wantCode {
				t.Errorf("status = %d, want %d", HTTPStatus(err), tc.wantCode)
			}
		})
	}

	// Nothing above may leave a temp file behind, or the store slowly fills
	// with the debris of refused uploads between sweeps.
	t.Run("a refused upload leaves no temp file", func(t *testing.T) {
		var leftovers []string
		_ = filepath.Walk(cfg.Storage.DataDir, func(path string, info os.FileInfo, err error) error {
			if err == nil && info != nil && !info.IsDir() && len(info.Name()) > 5 && info.Name()[:5] == ".tmp-" {
				leftovers = append(leftovers, path)
			}
			return nil
		})
		t.Logf("output: %d leftover temp files", len(leftovers))

		if len(leftovers) > 0 {
			t.Errorf("refused uploads left temp files behind: %v", leftovers)
		}
	})
}

// A short body must not be committed: storing it would hand out a URL to a
// truncated chunk that every downloader would then fail on.
func TestIngestCommitsNothingOnFailure(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)

	t.Logf("input:  5 bytes declared as 999")

	_, err := store.Ingest(io.LimitReader(bytes.NewReader([]byte("short")), 5), "k", time.Hour, 999)
	t.Logf("output: err=%v ids=%v", err, store.AllIDs())

	if err == nil {
		t.Fatalf("a short body must be refused")
	}
	if ids := store.AllIDs(); len(ids) != 0 {
		t.Errorf("a refused upload was committed: %v", ids)
	}
}

func TestDeleteReportsWhetherFilesWent(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)

	meta, err := store.Ingest(bytes.NewReader([]byte("ciphertext")), "k", time.Hour, 10)
	if err != nil {
		t.Fatalf("storing: %v", err)
	}

	t.Logf("input:  %s", meta.ID)

	first := store.Delete(meta.ID)
	second := store.Delete(meta.ID)
	t.Logf("output: first=%t second=%t exists=%t", first, second, store.Exists(meta.ID))

	if !first {
		t.Errorf("deleting a stored chunk must report true")
	}
	// False, not true: Delete says whether the files went, and a second call
	// found nothing to remove. The handler pairs it with Exists so a
	// concurrent delete still answers 204.
	if second {
		t.Errorf("deleting an absent chunk must report false")
	}
	if store.Exists(meta.ID) {
		t.Errorf("the chunk is still on disk after Delete")
	}
}

func TestHasRoomFor(t *testing.T) {
	cfg := testConfig(t)
	if err := os.MkdirAll(cfg.Storage.DataDir, 0o775); err != nil {
		t.Fatalf("creating the storage dir: %v", err)
	}

	t.Run("a zero floor has room for anything", func(t *testing.T) {
		cfg.Storage.MinFreeBytes = 0
		got := NewStore(cfg).HasRoomFor(1 << 40)
		t.Logf("input:  floor=0, want 1 TiB -> output: %t", got)

		if !got {
			t.Errorf("a zero floor must never refuse")
		}
	})

	t.Run("a floor above free space leaves none", func(t *testing.T) {
		free, ok := freeSpace(cfg.Storage.DataDir)
		if !ok {
			t.Skip("free space cannot be measured on this platform")
		}

		cfg.Storage.MinFreeBytes = free + (1 << 30)
		got := NewStore(cfg).HasRoomFor(0)
		t.Logf("input:  floor=%d, free=%d -> output: %t", cfg.Storage.MinFreeBytes, free, got)

		if got {
			t.Errorf("a floor above free space must refuse")
		}
	})

	t.Run("an unreadable storage path fails open", func(t *testing.T) {
		bad := *cfg
		bad.Storage.DataDir = filepath.Join(cfg.Storage.DataDir, "no-such-directory")
		bad.Storage.MinFreeBytes = 1 << 62

		got := NewStore(&bad).HasRoomFor(0)
		t.Logf("input:  an unreadable path with an enormous floor -> output: %t", got)

		// A host that cannot measure its own volume must keep accepting
		// uploads rather than refuse every single one.
		if !got {
			t.Errorf("an unmeasurable volume must fail open")
		}
	})
}
