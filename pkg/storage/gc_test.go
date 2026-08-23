package storage

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ModderMule/emule-http-cache-go/log"
)

func TestSweepReclaimsOnlyExpiredChunks(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)
	gc := NewGc(cfg, store, NewQuota(cfg))

	live, _ := store.Ingest(bytes.NewReader([]byte("live")), "k", time.Hour, 4)
	expired, _ := store.Ingest(bytes.NewReader([]byte("dead")), "k", -time.Hour, 4)
	t.Logf("input:  live=%s expired=%s", live.ID, expired.ID)

	reclaimed := gc.Sweep(cfg.GC.MaxDeletes)
	t.Logf("output: reclaimed=%d liveStillThere=%t expiredGone=%t",
		reclaimed, store.Exists(live.ID), !store.Exists(expired.ID))

	if reclaimed != 1 {
		t.Errorf("reclaimed %d, want 1", reclaimed)
	}
	if !store.Exists(live.ID) {
		t.Errorf("the sweep took a chunk that had not expired")
	}
	if store.Exists(expired.ID) {
		t.Errorf("the sweep left an expired chunk in place")
	}
}

// The stamp is decimal unix seconds with no trailing newline, which is what the
// PHP server writes and reads. A Go server sharing a var directory with one
// must not confuse it.
func TestSweepStampFormat(t *testing.T) {
	cfg := testConfig(t)
	gc := NewGc(cfg, NewStore(cfg), NewQuota(cfg))

	if _, ok := gc.LastSweepAt(); ok {
		t.Errorf("there must be no stamp before the first sweep")
	}

	gc.Sweep(10)

	raw, err := os.ReadFile(filepath.Join(cfg.Storage.VarDir, stampFile))
	if err != nil {
		t.Fatalf("the stamp was not written: %v", err)
	}
	t.Logf("output: %q", raw)

	if bytes.ContainsAny(raw, "\n\r ") {
		t.Errorf("the stamp carries whitespace: %q", raw)
	}

	last, ok := gc.LastSweepAt()
	t.Logf("parsed: %s ok=%t", last.UTC().Format(time.RFC3339), ok)

	if !ok || time.Since(last) > time.Minute {
		t.Errorf("the stamp does not read back as a recent time")
	}
}

// An interrupted ingest leaves a .tmp-* file. It is the backstop for a process
// killed between opening the temp file and renaming it into place, so the sweep
// has to actually reap them.
func TestSweepReapsStaleTempFiles(t *testing.T) {
	cfg := testConfig(t)
	store := NewStore(cfg)
	gc := NewGc(cfg, store, NewQuota(cfg))

	shard := filepath.Join(cfg.Storage.DataDir, "ab")
	if err := os.MkdirAll(shard, 0o775); err != nil {
		t.Fatalf("creating a shard: %v", err)
	}

	stale := filepath.Join(shard, ".tmp-abcdef0123456789abcdef0123456789")
	fresh := filepath.Join(shard, ".tmp-0123456789abcdef0123456789abcdef")
	for _, path := range []string{stale, fresh} {
		if err := os.WriteFile(path, []byte("partial"), 0o664); err != nil {
			t.Fatalf("staging %s: %v", path, err)
		}
	}

	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("ageing the stale temp file: %v", err)
	}
	t.Logf("input:  one temp file 2h old, one just written")

	gc.Sweep(10)

	_, staleErr := os.Stat(stale)
	_, freshErr := os.Stat(fresh)
	t.Logf("output: staleGone=%t freshKept=%t", staleErr != nil, freshErr == nil)

	if staleErr == nil {
		t.Errorf("an hour-old temp file survived the sweep")
	}
	if freshErr != nil {
		t.Errorf("the sweep reaped an upload that may still be in progress")
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	cfg := testConfig(t)
	cfg.GC.Interval = 10 * time.Millisecond
	gc := NewGc(cfg, NewStore(cfg), NewQuota(cfg))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		gc.Run(ctx, log.NewNop())
	}()

	time.Sleep(30 * time.Millisecond)
	cancel()

	select {
	case <-done:
		t.Logf("output: the sweeper stopped when its context was cancelled")
	case <-time.After(2 * time.Second):
		t.Errorf("the sweeper did not stop; shutdown would hang")
	}
}

// A zero interval disables the sweeper entirely, which is what an install that
// runs the gc subcommand from cron sets.
func TestRunDisabledByZeroInterval(t *testing.T) {
	cfg := testConfig(t)
	cfg.GC.Interval = 0

	done := make(chan struct{})
	go func() {
		defer close(done)
		NewGc(cfg, NewStore(cfg), NewQuota(cfg)).Run(context.Background(), log.NewNop())
	}()

	select {
	case <-done:
		t.Logf("output: Run returned immediately")
	case <-time.After(time.Second):
		t.Errorf("a zero interval must disable the sweeper, not start one")
	}
}
