// Package traffic counts what the server does: uploads, downloads, refusals and
// how many distinct callers it has seen.
//
// Everything lives in memory and starts from zero with the process. That is
// deliberate — the store is the filesystem and nothing else, and a counter file
// would be one more thing a co-resident PHP install does not know about.
//
// No client address is kept. Distinct callers are counted with HyperLogLog
// sketches (see Sketch), keyed by a hash whose seed is random per process.
package traffic

import (
	"hash/maphash"
	"net/netip"
	"sync"
	"time"
)

// Cause is why a request was refused.
type Cause int

const (
	Unauthorized  Cause = iota // 401
	NotFound                   // 404
	TooLarge                   // 413
	QuotaExceeded              // 429
	StorageFull                // 507

	causeCount
)

// Figure is one counter: everything since the process started, and the rolling
// last day.
type Figure struct {
	Total   int64
	Last24h int64
}

// Rejected is the refusals, by cause.
type Rejected struct {
	Unauthorized  Figure
	NotFound      Figure
	TooLarge      Figure
	QuotaExceeded Figure
	StorageFull   Figure
}

// Snapshot is every figure at one moment.
type Snapshot struct {
	Uploads         Figure
	UploadedBytes   Figure
	Downloads       Figure
	DownloadedBytes Figure
	Deletes         Figure
	Expired         Figure
	Rejected        Rejected

	// Clients is an estimate, not a tally: see Sketch.
	Clients Figure
}

// Recorder is the server's set of counters.
//
// A nil Recorder accepts every call and records nothing, so a caller that was
// built without one needs no checks of its own.
type Recorder struct {
	// One lock for everything. A request here moves megabytes, so the few
	// nanoseconds spent serialising a counter update are not worth a design.
	mu sync.Mutex

	seed maphash.Seed

	uploads         Window
	uploadedBytes   Window
	downloads       Window
	downloadedBytes Window
	deletes         Window
	expired         Window
	rejected        [causeCount]Window

	clients sketchWindow
}

// NewRecorder builds an empty set of counters.
func NewRecorder() *Recorder {
	return &Recorder{seed: maphash.MakeSeed()}
}

// Upload counts one stored chunk of the given size.
func (r *Recorder) Upload(bytes int64, now time.Time) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.uploads.Add(1, now)
	r.uploadedBytes.Add(bytes, now)
}

// Download counts one chunk response and the bytes that actually left.
func (r *Recorder) Download(bytes int64, now time.Time) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.downloads.Add(1, now)
	r.downloadedBytes.Add(bytes, now)
}

// Delete counts one chunk removed by its uploader.
func (r *Recorder) Delete(now time.Time) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.deletes.Add(1, now)
}

// Expired counts chunks reclaimed by the expiry sweep.
func (r *Recorder) Expired(chunks int, now time.Time) {
	if r == nil {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.expired.Add(int64(chunks), now)
}

// Reject counts one refused request.
func (r *Recorder) Reject(cause Cause, now time.Time) {
	if r == nil || cause < 0 || cause >= causeCount {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	r.rejected[cause].Add(1, now)
}

// Seen notes a caller's address. An invalid address is ignored.
//
// An IPv6 address is counted by its /64: one host cycles through many
// addresses inside its prefix under privacy extensions, and counting each
// would turn a single client into dozens.
func (r *Recorder) Seen(addr netip.Addr, now time.Time) {
	if r == nil || !addr.IsValid() {
		return
	}

	addr = addr.Unmap().WithZone("")
	if addr.Is6() {
		if prefix, err := addr.Prefix(64); err == nil {
			addr = prefix.Addr()
		}
	}
	raw := addr.As16()

	r.mu.Lock()
	defer r.mu.Unlock()

	r.clients.add(maphash.Bytes(r.seed, raw[:]), now)
}

// Snapshot reads every figure.
func (r *Recorder) Snapshot(now time.Time) Snapshot {
	if r == nil {
		return Snapshot{}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	clients, recent := r.clients.counts(now)

	return Snapshot{
		Uploads:         figure(&r.uploads, now),
		UploadedBytes:   figure(&r.uploadedBytes, now),
		Downloads:       figure(&r.downloads, now),
		DownloadedBytes: figure(&r.downloadedBytes, now),
		Deletes:         figure(&r.deletes, now),
		Expired:         figure(&r.expired, now),
		Rejected: Rejected{
			Unauthorized:  figure(&r.rejected[Unauthorized], now),
			NotFound:      figure(&r.rejected[NotFound], now),
			TooLarge:      figure(&r.rejected[TooLarge], now),
			QuotaExceeded: figure(&r.rejected[QuotaExceeded], now),
			StorageFull:   figure(&r.rejected[StorageFull], now),
		},
		Clients: Figure{Total: clients, Last24h: recent},
	}
}

// -- internals ---------------------------------------------------------------

func figure(w *Window, now time.Time) Figure {
	return Figure{Total: w.Total(), Last24h: w.Last24h(now)}
}
