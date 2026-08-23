package storage

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

// unsafeKeyChars is everything a key id may not contribute to a filename. The
// id comes from config rather than from a request, but it still ends up in a
// path, so it is sanitised regardless.
var unsafeKeyChars = regexp.MustCompile(`[^A-Za-z0-9._-]`)

// Quota is the per-key, per-UTC-day upload allowance.
//
// One small counter file per key per day under the var directory. A quota of 0
// means unlimited.
type Quota struct {
	area

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewQuota builds a quota accountant over the configured var directory.
func NewQuota(cfg *config.Config) *Quota {
	return &Quota{area: area{cfg: cfg}, locks: map[string]*sync.Mutex{}}
}

// Consume reserves bytes against today's allowance for a key.
//
// It returns false when the reservation would exceed the quota, in which case
// nothing is charged. Callers reserve before reading a request body, so a flood
// of concurrent uploads cannot collectively overshoot.
func (q *Quota) Consume(keyID string, bytes int64) bool {
	limit := q.cfg.QuotaFor(keyID)
	if limit <= 0 {
		return true
	}

	path := q.counterPath(keyID, time.Now())
	if err := ensureDir(q.cfg.Storage.VarDir); err != nil {
		// A broken var directory must not silently disable the quota.
		return false
	}

	return q.withLockedCounter(path, func(used int64) (int64, bool) {
		if used+bytes > limit {
			return 0, false
		}
		return used + bytes, true
	})
}

// Refund gives back a reservation that turned out not to be used, which is what
// a failed ingest leaves behind.
func (q *Quota) Refund(keyID string, bytes int64) {
	if q.cfg.QuotaFor(keyID) <= 0 || bytes <= 0 {
		return
	}

	q.withLockedCounter(q.counterPath(keyID, time.Now()), func(used int64) (int64, bool) {
		next := used - bytes
		if next < 0 {
			next = 0
		}
		return next, true
	})
}

// Used reports the bytes already charged to a key today.
func (q *Quota) Used(keyID string) int64 {
	raw, err := os.ReadFile(q.counterPath(keyID, time.Now()))
	if err != nil {
		return 0
	}

	n, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)

	return n
}

// SecondsUntilReset is how long until this key's daily allowance rolls over.
//
// It is the honest value for a Retry-After on a 429: the quota is per UTC day,
// so the client is told when to come back rather than left to guess. Always
// within the client's accepted 1..86400 delta-seconds window.
func SecondsUntilReset(now time.Time) int {
	utc := now.UTC()
	midnight := time.Date(utc.Year(), utc.Month(), utc.Day(), 0, 0, 0, 0, time.UTC).Add(24 * time.Hour)

	seconds := int(midnight.Sub(utc).Seconds())
	if seconds < 1 {
		return 1
	}

	return seconds
}

// -- internals ---------------------------------------------------------------

// withLockedCounter reads the counter, hands it to mutate, and writes back
// whatever comes out. A false from mutate means "leave it alone", which is what
// a refused reservation returns.
//
// Two layers of lock. The in-process mutex comes first so that at most one
// goroutine per counter ever parks an OS thread in the syscall; flock inside it
// is what keeps the `gc` subcommand, a second daemon, or a co-resident PHP
// install from interleaving a read-modify-write.
func (q *Quota) withLockedCounter(path string, mutate func(used int64) (int64, bool)) bool {
	gate := q.gateFor(path)
	gate.Lock()
	defer gate.Unlock()

	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o664)
	if err != nil {
		return false
	}
	defer f.Close()

	if err := lockFile(f); err != nil {
		return false
	}
	defer unlockFile(f)

	raw, err := io.ReadAll(f)
	if err != nil {
		return false
	}
	used, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)

	next, ok := mutate(used)
	if !ok {
		return false
	}

	if err := f.Truncate(0); err != nil {
		return false
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return false
	}
	if _, err := f.WriteString(strconv.FormatInt(next, 10)); err != nil {
		return false
	}

	return true
}

// gateFor returns the in-process mutex guarding one counter file. Keyed by path
// rather than by key id, so the UTC day rollover naturally starts a fresh
// entry.
func (q *Quota) gateFor(path string) *sync.Mutex {
	q.mu.Lock()
	defer q.mu.Unlock()

	gate, ok := q.locks[path]
	if !ok {
		gate = &sync.Mutex{}
		q.locks[path] = gate
	}

	return gate
}

// forgetStaleGates drops mutexes for counter files that are no longer current,
// so the map cannot grow without bound on a long-lived server. Called from the
// sweep, which reaps the counter files themselves on the same schedule.
func (q *Quota) forgetStaleGates(now time.Time) {
	today := now.UTC().Format("20060102")

	q.mu.Lock()
	defer q.mu.Unlock()

	for path := range q.locks {
		if !strings.HasSuffix(path, "-"+today+".txt") {
			delete(q.locks, path)
		}
	}
}

func (q *Quota) counterPath(keyID string, now time.Time) string {
	safe := unsafeKeyChars.ReplaceAllString(keyID, "_")
	if safe == "" {
		safe = "key"
	}

	return q.varPath("quota-" + safe + "-" + now.UTC().Format("20060102") + ".txt")
}
