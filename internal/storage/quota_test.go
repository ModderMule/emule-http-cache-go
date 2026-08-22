package storage

import (
	"testing"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

func quotaConfig(t *testing.T, limit int64) *config.Config {
	t.Helper()

	cfg := testConfig(t)
	cfg.RawKeys = map[string]config.RawAPIKey{
		"k": {Secret: "s", QuotaBytesPerDay: limit},
	}
	cfg.APIKeys = []config.APIKey{{ID: "k", Secret: "s", QuotaBytesPerDay: limit, Enabled: true}}

	return cfg
}

func TestQuotaConsumeAndRefund(t *testing.T) {
	cfg := quotaConfig(t, 1000)
	q := NewQuota(cfg)

	t.Logf("input:  limit=1000")

	if !q.Consume("k", 600) {
		t.Fatalf("the first 600 bytes must fit")
	}
	t.Logf("after 600: used=%d", q.Used("k"))

	if q.Consume("k", 600) {
		t.Errorf("600 more bytes must not fit under a 1000-byte limit")
	}
	if used := q.Used("k"); used != 600 {
		t.Errorf("a refused reservation charged the key: used=%d, want 600", used)
	}

	q.Refund("k", 600)
	t.Logf("after refund: used=%d", q.Used("k"))

	if used := q.Used("k"); used != 0 {
		t.Errorf("used = %d after a full refund, want 0", used)
	}

	if !q.Consume("k", 1000) {
		t.Errorf("the whole allowance must fit once it has been given back")
	}
}

func TestQuotaZeroMeansUnlimited(t *testing.T) {
	q := NewQuota(quotaConfig(t, 0))

	t.Logf("input:  limit=0 (unlimited)")

	ok := q.Consume("k", 1<<40)
	t.Logf("output: consume 1 TiB = %t, used=%d", ok, q.Used("k"))

	if !ok {
		t.Errorf("a zero limit must mean unlimited")
	}
	// Nothing is written at all when there is no limit to track.
	if used := q.Used("k"); used != 0 {
		t.Errorf("an unlimited key wrote a counter: used=%d", used)
	}
}

func TestSecondsUntilReset(t *testing.T) {
	cases := map[string]time.Time{
		"just after midnight UTC": time.Date(2026, 8, 22, 0, 0, 1, 0, time.UTC),
		"midday UTC":              time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC),
		"one second to midnight":  time.Date(2026, 8, 22, 23, 59, 59, 0, time.UTC),
	}

	for label, now := range cases {
		t.Run(label, func(t *testing.T) {
			t.Logf("input:  %s", now.Format(time.RFC3339))

			got := SecondsUntilReset(now)
			t.Logf("output: %d", got)

			// The client only honours Retry-After as delta-seconds in 1..86400
			// and silently discards anything else, so a value outside that
			// window is the same as sending none.
			if got < 1 || got > 86400 {
				t.Errorf("Retry-After of %d is outside the 1..86400 the client accepts", got)
			}
		})
	}
}
