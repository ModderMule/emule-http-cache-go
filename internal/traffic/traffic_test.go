package traffic

import (
	"math"
	"net/netip"
	"testing"
	"time"
)

// mix is a fixed, well-distributed hash (splitmix64), so the sketch tests are
// reproducible. The recorder seeds its own hash at random.
func mix(i uint64) uint64 {
	i += 0x9e3779b97f4a7c15
	i = (i ^ (i >> 30)) * 0xbf58476d1ce4e5b9
	i = (i ^ (i >> 27)) * 0x94d049bb133111eb

	return i ^ (i >> 31)
}

func TestSketchEstimates(t *testing.T) {
	cases := []struct {
		label     string
		distinct  uint64
		tolerance float64 // fraction of the true count
	}{
		{"nothing", 0, 0},
		{"one value", 1, 0},
		{"a handful", 10, 0},
		{"a small server's day", 1_000, 0.02},
		{"past the linear-counting range", 100_000, 0.03},
		{"a million", 1_000_000, 0.03},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			var s Sketch
			for i := uint64(0); i < tc.distinct; i++ {
				s.Add(mix(i))
			}

			got := s.Count()
			off := math.Abs(float64(got) - float64(tc.distinct))
			t.Logf("input:  %d distinct values", tc.distinct)
			t.Logf("output: estimate %d, off by %.0f", got, off)

			if off > tc.tolerance*float64(tc.distinct) {
				t.Errorf("estimate %d is more than %.0f%% from %d", got, tc.tolerance*100, tc.distinct)
			}
		})
	}
}

func TestSketchIgnoresRepeats(t *testing.T) {
	var once, often Sketch
	for i := range uint64(500) {
		once.Add(mix(i))
		for range 20 {
			often.Add(mix(i))
		}
	}

	t.Logf("input:  500 values once, and the same 500 twenty times each")
	t.Logf("output: once=%d often=%d", once.Count(), often.Count())

	if once != often {
		t.Errorf("a repeated value changed the sketch")
	}
}

func TestSketchMergeIsTheUnion(t *testing.T) {
	var left, right, both Sketch
	for i := range uint64(3_000) {
		both.Add(mix(i))
		if i < 2_000 {
			left.Add(mix(i))
		}
		if i >= 1_000 {
			right.Add(mix(i))
		}
	}

	merged := left
	merged.Merge(&right)

	t.Logf("input:  values 0..1999 merged with values 1000..2999")
	t.Logf("output: merged=%d, counted directly=%d", merged.Count(), both.Count())

	if merged != both {
		t.Errorf("merging two sketches differs from one sketch shown both sets")
	}
}

func TestWindowRollsOffAfterADay(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)

	var w Window
	w.Add(5, start)
	w.Add(2, start.Add(10*time.Hour))

	cases := []struct {
		label string
		after time.Duration
		want  int64
	}{
		{"at once", 0, 5},
		{"ten hours on", 10 * time.Hour, 7},
		{"the first hour is still inside", 24 * time.Hour, 7},
		{"the first hour has rolled off", 25 * time.Hour, 2},
		{"everything has rolled off", 40 * time.Hour, 0},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := w.Last24h(start.Add(tc.after))
			t.Logf("input:  5 at t+0h and 2 at t+10h, read at t+%s", tc.after)
			t.Logf("output: last24h=%d total=%d", got, w.Total())

			if got != tc.want {
				t.Errorf("last24h = %d, want %d", got, tc.want)
			}
			if w.Total() != 7 {
				t.Errorf("total = %d, want 7: it must never roll off", w.Total())
			}
		})
	}
}

// A slot is reused 25 hours later. What it held before must not leak into the
// new hour.
func TestWindowClearsAReusedSlot(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	later := start.Add(windowHours * time.Hour)

	var w Window
	w.Add(9, start)
	w.Add(1, later)

	got := w.Last24h(later)
	t.Logf("input:  9 at t+0h, 1 at t+%dh (the same slot)", windowHours)
	t.Logf("output: last24h=%d total=%d", got, w.Total())

	if got != 1 {
		t.Errorf("last24h = %d, want 1", got)
	}
	if w.Total() != 10 {
		t.Errorf("total = %d, want 10", w.Total())
	}
}

func TestRecorderSnapshot(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	nextDay := start.Add(30 * time.Hour)

	r := NewRecorder()
	r.Upload(1_000, start)
	r.Download(400, start)
	r.Reject(NotFound, start)
	r.Seen(netip.MustParseAddr("203.0.113.1"), start)
	r.Seen(netip.MustParseAddr("203.0.113.2"), start)

	r.Upload(50, nextDay)
	r.Download(1_000, nextDay)
	r.Download(600, nextDay)
	r.Delete(nextDay)
	r.Expired(3, nextDay)
	r.Reject(Unauthorized, nextDay)
	r.Reject(Cause(99), nextDay) // out of range, ignored
	r.Seen(netip.MustParseAddr("203.0.113.2"), nextDay)
	r.Seen(netip.Addr{}, nextDay) // invalid, ignored

	got := r.Snapshot(nextDay)
	t.Logf("input:  two uploads, three downloads, a delete, a sweep of 3, two refusals, two callers, across 30 hours")
	t.Logf("output: %+v", got)

	want := Snapshot{
		Uploads:         Figure{Total: 2, Last24h: 1},
		UploadedBytes:   Figure{Total: 1_050, Last24h: 50},
		Downloads:       Figure{Total: 3, Last24h: 2},
		DownloadedBytes: Figure{Total: 2_000, Last24h: 1_600},
		Deletes:         Figure{Total: 1, Last24h: 1},
		Expired:         Figure{Total: 3, Last24h: 3},
		Rejected: Rejected{
			Unauthorized: Figure{Total: 1, Last24h: 1},
			NotFound:     Figure{Total: 1, Last24h: 0},
		},
		Clients: Figure{Total: 2, Last24h: 1},
	}
	if got != want {
		t.Errorf("snapshot differs\n got: %+v\nwant: %+v", got, want)
	}
}

func TestRecorderCountsAnIPv6HostOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	cases := []struct {
		label string
		addrs []string
		want  int64
	}{
		{"privacy addresses inside one /64", []string{"2001:db8:1:2::a", "2001:db8:1:2:ffff::b"}, 1},
		{"two different /64s", []string{"2001:db8:1:2::a", "2001:db8:1:3::a"}, 2},
		{"an IPv4 address and its mapped form", []string{"198.51.100.7", "::ffff:198.51.100.7"}, 1},
		{"a zone does not make a new host", []string{"fe80::1%eth0", "fe80::1%eth1"}, 1},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			r := NewRecorder()
			for _, addr := range tc.addrs {
				r.Seen(netip.MustParseAddr(addr), now)
			}

			got := r.Snapshot(now).Clients.Total
			t.Logf("input:  %v", tc.addrs)
			t.Logf("output: %d client(s)", got)

			if got != tc.want {
				t.Errorf("counted %d client(s), want %d", got, tc.want)
			}
		})
	}
}

func TestNilRecorderIsANoOp(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)

	var r *Recorder
	r.Upload(1, now)
	r.Download(1, now)
	r.Delete(now)
	r.Expired(1, now)
	r.Reject(NotFound, now)
	r.Seen(netip.MustParseAddr("203.0.113.1"), now)

	got := r.Snapshot(now)
	t.Logf("input:  every method on a nil recorder")
	t.Logf("output: %+v", got)

	if got != (Snapshot{}) {
		t.Errorf("a nil recorder reported something: %+v", got)
	}
}
