// Package storage is the filesystem blob store behind the cache.
//
// Layout, byte-identical to the PHP reference server so the two can be pointed
// at the same directory:
//
//	<dataDir>/<first two hex chars of id>/<id>.bin    the ciphertext
//	<dataDir>/<first two hex chars of id>/<id>.json   its metadata sidecar
//	<dataDir>/<first two hex chars of id>/.tmp-<id>   an ingest in progress
//	<varDir>/quota-<keyId>-<YYYYMMDD>.txt             bytes charged today
//	<varDir>/gc-last.txt                              unix time of the last sweep
//
// The 256-way fan-out keeps directory sizes sane; the sidecar keeps the store
// self-describing, so a sweep or an operator with nothing but a shell never
// needs a database.
package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

// area is the filesystem plumbing Store, Quota and Gc all share.
//
// PHP expressed this as an abstract StorageArea base class. Go has no
// inheritance, so the three types embed this struct instead: same single copy
// of the shard walk, no interface, and a compile error the day one of them is
// built without a config.
type area struct {
	cfg *config.Config
}

// shards lists the two-hex-character directories under the storage root.
func (a area) shards() []string {
	entries, err := os.ReadDir(a.cfg.Storage.DataDir)
	if err != nil {
		return nil
	}

	shards := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if len(name) == 2 && isHex(name) {
			shards = append(shards, name)
		}
	}

	return shards
}

func (a area) shardPath(shard string) string {
	return filepath.Join(a.cfg.Storage.DataDir, shard)
}

func (a area) varPath(name string) string {
	return filepath.Join(a.cfg.Storage.VarDir, name)
}

// -- stateless helpers -------------------------------------------------------
//
// These take everything they need as arguments and are deliberately not
// methods: Gc reaps under both the storage root and the var directory, so they
// must stay path-agnostic.

// readJSONFile decodes a file into v. A missing or malformed file is an error.
func readJSONFile(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	return json.Unmarshal(raw, v)
}

// reapByPrefix deletes files in dir whose name starts with prefix and whose
// mtime is older than maxAge. It returns how many went.
func reapByPrefix(dir, prefix string, maxAge time.Duration, now time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}

	removed := 0
	for _, entry := range entries {
		name := entry.Name()
		if len(name) < len(prefix) || name[:len(prefix)] != prefix {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= maxAge {
			continue
		}
		if os.Remove(filepath.Join(dir, name)) == nil {
			removed++
		}
	}

	return removed
}

// ensureDir creates a directory and every parent, tolerating a concurrent
// creator.
func ensureDir(path string) error {
	if err := os.MkdirAll(path, 0o775); err != nil {
		if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
			return nil
		}
		return err
	}

	return nil
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}

	return len(s) > 0
}
