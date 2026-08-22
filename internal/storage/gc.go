package storage

import (
	"context"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/log"
)

const (
	// tempMaxAge is how long an interrupted ingest's .tmp-* file is kept before
	// it is reaped. It is the backstop for a process that was killed between
	// opening the temp file and renaming it into place.
	tempMaxAge = time.Hour

	// quotaMaxAge is how long a daily counter file is kept. They are per UTC
	// day, so anything older than a week is dead weight.
	quotaMaxAge = 7 * 24 * time.Hour

	// stampFile records when the last sweep started.
	stampFile = "gc-last.txt"
)

// Gc reclaims expired chunks.
//
// Chunks are never deleted on read — a GET must not do write work — so
// something has to reclaim them. In the PHP server that was a daily stamp plus
// a dice roll on every upload, because PHP has no long-lived process. Here it
// is a goroutine on a ticker, with the `gc` subcommand for a manual or cron
// run. The stamp file is still written, so both can report when the last sweep
// happened and a PHP install sharing the directory still sees it.
type Gc struct {
	area

	store *Store
	quota *Quota
}

// NewGc builds a collector over a store and its quota accountant.
func NewGc(cfg *config.Config, store *Store, quota *Quota) *Gc {
	return &Gc{area: area{cfg: cfg}, store: store, quota: quota}
}

// Sweep reclaims expired chunks, interrupted uploads and stale quota counters,
// deleting at most maxDeletes chunks. It returns how many items went.
func (g *Gc) Sweep(maxDeletes int) int {
	now := time.Now()

	// Stamped before the work, not after: a slow sweep must not leave the next
	// caller thinking one is still overdue and starting a second.
	g.recordSweep(now)

	deleted := 0
	for _, id := range g.store.AllIDs() {
		if deleted >= maxDeletes {
			break
		}

		expiresAt, ok := g.store.RawExpiresAt(id)
		if ok && expiresAt > now.Unix() {
			continue
		}

		if g.store.Delete(id) {
			deleted++
		}
	}

	for _, shard := range g.shards() {
		deleted += reapByPrefix(g.shardPath(shard), ".tmp-", tempMaxAge, now)
	}
	deleted += reapByPrefix(g.cfg.Storage.VarDir, "quota-", quotaMaxAge, now)

	if g.quota != nil {
		g.quota.forgetStaleGates(now)
	}

	return deleted
}

// LastSweepAt reports when a sweep last started.
func (g *Gc) LastSweepAt() (time.Time, bool) {
	raw, err := os.ReadFile(g.varPath(stampFile))
	if err != nil {
		return time.Time{}, false
	}

	seconds, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil || seconds <= 0 {
		return time.Time{}, false
	}

	return time.Unix(seconds, 0), true
}

// Run sweeps on a ticker until ctx is cancelled. An interval of zero disables
// the sweeper entirely, leaving the `gc` subcommand as the only reclaim path.
//
// The first sweep fires immediately only when one is already overdue. Without
// that check a crash-restart loop would sweep on every boot, and with it the
// stamp keeps meaning what it says.
func (g *Gc) Run(ctx context.Context, logger log.Logger) {
	interval := g.cfg.GC.Interval
	if interval <= 0 {
		logger.Infof("expiry sweeper disabled (gc.interval is 0); run the gc subcommand from cron instead")
		return
	}

	if last, ok := g.LastSweepAt(); !ok || time.Since(last) >= interval {
		logger.Infof("expiry sweep reclaimed %d item(s)", g.Sweep(g.cfg.GC.MaxDeletes))
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n := g.Sweep(g.cfg.GC.MaxDeletes); n > 0 {
				logger.Infof("expiry sweep reclaimed %d item(s)", n)
			}
		}
	}
}

// -- internals ---------------------------------------------------------------

// recordSweep stamps the start of a sweep. Best effort: a var directory that
// cannot be written is not a reason to skip the reclaim itself.
//
// The file holds decimal unix seconds with no trailing newline, which is what
// the PHP server writes, and is chmod 0666 afterwards for the same reason it is
// there: the daemon and a hand-run gc may be different users, and whoever
// creates the stamp first must not lock the other out of rewriting it.
func (g *Gc) recordSweep(now time.Time) {
	if err := ensureDir(g.cfg.Storage.VarDir); err != nil {
		return
	}

	path := g.varPath(stampFile)
	if err := os.WriteFile(path, []byte(strconv.FormatInt(now.Unix(), 10)), 0o666); err != nil {
		return
	}
	_ = os.Chmod(path, 0o666)
}
