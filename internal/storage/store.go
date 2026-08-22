package storage

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

// Read and write slice sizes, matching the PHP server's READ_CHUNK and
// SEND_CHUNK. Nothing ever buffers a whole chunk.
const (
	ingestSlice = 1024 * 1024 // 1 MiB in
	sendSlice   = 512 * 1024  // 512 KiB out
)

// idPattern is the whole defence against a hostile chunk id reaching the
// filesystem. Every path-building method checks it first.
//
// Lowercase only, exactly as PHP's /^[0-9a-f]{32}$/ — an uppercase id is a 404
// in both implementations rather than being folded.
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Store is the filesystem blob store.
type Store struct {
	area

	// newID is injectable so tests can pin the ids they exercise.
	newID func() (string, error)
}

// NewStore builds a store over the configured directories.
func NewStore(cfg *config.Config) *Store {
	return &Store{area: area{cfg: cfg}, newID: randomID}
}

// IsValidID reports whether an id is well formed. A path that is otherwise
// well formed but carries a malformed id is a 404, never a 400: the id space is
// opaque to clients and must not leak validation detail.
func IsValidID(id string) bool {
	return idPattern.MatchString(id)
}

// BlobPath is where a chunk's ciphertext lives.
func (s *Store) BlobPath(id string) string {
	return s.cfg.ChunkPath(id, "bin")
}

// MetaPath is where a chunk's metadata sidecar lives.
func (s *Store) MetaPath(id string) string {
	return s.cfg.ChunkPath(id, "json")
}

// Meta returns the metadata of a stored chunk.
//
// An expired chunk reports as absent but is not deleted here: a GET must not do
// write work. Gc.Sweep reclaims it.
func (s *Store) Meta(id string) (*ChunkMeta, bool) {
	if !IsValidID(id) {
		return nil, false
	}

	var meta ChunkMeta
	if err := readJSONFile(s.MetaPath(id), &meta); err != nil {
		return nil, false
	}
	if meta.ID == "" {
		meta.ID = id
	}

	if meta.IsExpired(time.Now()) {
		return nil, false
	}
	if _, err := os.Stat(s.BlobPath(id)); err != nil {
		return nil, false
	}

	return &meta, true
}

// HasRoomFor reports whether writing n more bytes would still leave
// Storage.MinFreeBytes free on the storage volume.
//
// It fails open when free space cannot be measured: a host where the syscall is
// blocked must keep accepting uploads rather than refuse every single one.
func (s *Store) HasRoomFor(n int64) bool {
	if s.cfg.Storage.MinFreeBytes <= 0 {
		return true
	}

	free, ok := freeSpace(s.cfg.Storage.DataDir)
	if !ok {
		return true
	}

	return free-n >= s.cfg.Storage.MinFreeBytes
}

// Ingest streams a request body into the store and commits it.
//
// declared is the request's Content-Length, which the caller has already
// checked against MaxChunkSize; it is re-checked against the bytes that
// actually arrive, because a body that ends short means the upload was cut off
// and storing it would hand out a URL to a truncated chunk.
func (s *Store) Ingest(body io.Reader, ownerKeyID string, ttl time.Duration, declared int64) (*ChunkMeta, error) {
	id, err := s.newID()
	if err != nil {
		return nil, failure(ErrNoStorage, 500, "cannot generate a chunk id")
	}

	blobPath := s.BlobPath(id)
	shardDir := filepath.Dir(blobPath)
	if err := ensureDir(shardDir); err != nil {
		return nil, failure(ErrNoStorage, 507, "cannot create storage directory")
	}

	// Named, not os.CreateTemp: Gc reaps ".tmp-*" older than an hour, the
	// rename must not cross a filesystem, and CreateTemp's 0600 would diverge
	// from the mode PHP leaves on a committed blob.
	tmpPath := filepath.Join(shardDir, ".tmp-"+id)
	tmp, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o664)
	if err != nil {
		return nil, failure(ErrNoStorage, 507, "cannot open temporary file")
	}

	committed := false
	defer func() {
		if !committed {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	total, digest, err := s.copyBody(tmp, body)
	if err != nil {
		return nil, err
	}

	if total == 0 {
		return nil, failure(ErrEmptyBody, 400, "empty body")
	}
	if declared >= 0 && total != declared {
		return nil, failure(ErrLengthMismatch, 400, "body length does not match Content-Length")
	}

	if s.cfg.Storage.Fsync {
		if err := tmp.Sync(); err != nil {
			return nil, failure(ErrNoStorage, 507, "write failed")
		}
	}
	// Closed explicitly rather than only in the deferred cleanup: a Close error
	// is how a delayed-allocation or network filesystem reports a lost write,
	// and dropping it would commit a chunk that is not really there.
	if err := tmp.Close(); err != nil {
		return nil, failure(ErrNoStorage, 507, "write failed")
	}

	now := time.Now()
	meta := &ChunkMeta{
		ID:         id,
		Size:       total,
		SHA256:     digest,
		OwnerKeyID: ownerKeyID,
		CreatedAt:  now.Unix(),
		ExpiresAt:  now.Add(ttl).Unix(),
	}

	// The sidecar goes down before the blob is renamed into place, and the
	// order matters more than it looks. AllIDs enumerates *.json only, so a
	// blob with no sidecar is invisible to every future sweep — 9.7 MB that can
	// never be reclaimed. A sidecar with no blob is enumerated and reaped at
	// its expiry, and Meta already reports it as absent meanwhile.
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, failure(ErrNoStorage, 507, "cannot write metadata")
	}
	if err := os.WriteFile(s.MetaPath(id), encoded, 0o664); err != nil {
		return nil, failure(ErrNoStorage, 507, "cannot write metadata")
	}

	if err := os.Rename(tmpPath, blobPath); err != nil {
		_ = os.Remove(s.MetaPath(id))
		return nil, failure(ErrNoStorage, 507, "cannot commit chunk")
	}
	committed = true

	if s.cfg.Storage.Fsync {
		syncDir(shardDir)
	}

	return meta, nil
}

// Exists reports whether the blob or its sidecar is still on disk, expired or
// not.
func (s *Store) Exists(id string) bool {
	if !IsValidID(id) {
		return false
	}

	if _, err := os.Stat(s.BlobPath(id)); err == nil {
		return true
	}
	_, err := os.Stat(s.MetaPath(id))

	return err == nil
}

// Delete removes a chunk and its sidecar.
//
// It reports whether the files actually went, not merely that they were there.
// A shard directory the current user cannot write to leaves them in place, and
// counting those would have Gc report the same reclaim forever.
func (s *Store) Delete(id string) bool {
	if !IsValidID(id) {
		return false
	}

	blob, meta := s.BlobPath(id), s.MetaPath(id)
	_, blobErr := os.Stat(blob)
	_, metaErr := os.Stat(meta)
	if blobErr != nil && metaErr != nil {
		return false
	}

	blobGone := blobErr != nil || os.Remove(blob) == nil
	metaGone := metaErr != nil || os.Remove(meta) == nil

	return blobGone && metaGone
}

// OpenBlob opens a chunk's ciphertext for reading.
func (s *Store) OpenBlob(id string) (*os.File, error) {
	if !IsValidID(id) {
		return nil, os.ErrNotExist
	}

	return os.Open(s.BlobPath(id))
}

// AllIDs lists every chunk id currently on disk, expired or not.
func (s *Store) AllIDs() []string {
	var ids []string

	for _, shard := range s.shards() {
		entries, err := os.ReadDir(s.shardPath(shard))
		if err != nil {
			continue
		}

		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasSuffix(name, ".json") {
				continue
			}

			id := strings.TrimSuffix(name, ".json")
			if IsValidID(id) {
				ids = append(ids, id)
			}
		}
	}

	return ids
}

// RawExpiresAt reads a chunk's expiry straight off disk, ignoring the rule that
// an expired chunk reads as absent. Gc needs the real value.
func (s *Store) RawExpiresAt(id string) (int64, bool) {
	var meta ChunkMeta
	if err := readJSONFile(s.MetaPath(id), &meta); err != nil {
		return 0, false
	}

	return meta.ExpiresAt, true
}

// -- internals ---------------------------------------------------------------

// copyBody streams body into dst in 1 MiB slices, hashing as it goes, so peak
// memory stays at one slice regardless of chunk size.
//
// Written as an explicit loop rather than io.Copy over an io.MultiWriter so a
// failed disk write and a truncated upload stay distinguishable — they are a
// 507 and a 400 respectively, and io.Copy folds them into one error.
func (s *Store) copyBody(dst *os.File, body io.Reader) (int64, string, error) {
	hash := sha256.New()
	buf := make([]byte, ingestSlice)
	max := s.cfg.Storage.MaxChunkSize

	var total int64
	for {
		n, readErr := body.Read(buf)

		if n > 0 {
			total += int64(n)
			if total > max {
				return 0, "", failure(ErrTooLarge, 413, "chunk exceeds maxChunkSize")
			}

			_, _ = hash.Write(buf[:n]) // hash.Hash never returns an error
			if _, err := dst.Write(buf[:n]); err != nil {
				return 0, "", failure(ErrNoStorage, 507, "write failed")
			}
		}

		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			// The client hung up or the body was framed short. Either way the
			// bytes promised by Content-Length did not arrive.
			if errors.Is(readErr, io.ErrUnexpectedEOF) {
				return 0, "", failure(ErrLengthMismatch, 400, "body length does not match Content-Length")
			}

			return 0, "", failure(ErrLengthMismatch, 400, "body length does not match Content-Length")
		}
	}

	return total, hex.EncodeToString(hash.Sum(nil)), nil
}

// randomID is 128 bits from a CSPRNG as 32 lowercase hex characters. The id is
// the capability that guards a chunk, so nothing weaker will do.
func randomID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}

	return hex.EncodeToString(raw), nil
}

// syncDir flushes a directory entry so a rename survives a power loss. Best
// effort: a filesystem that refuses to open a directory is not a reason to fail
// an upload that is already on disk.
func syncDir(path string) {
	d, err := os.Open(path)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}
