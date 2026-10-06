package http_public

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ModderMule/emule-http-cache-go/internal/security"
	"github.com/ModderMule/emule-http-cache-go/internal/traffic"
	"github.com/ModderMule/emule-http-cache-go/pkg/storage"
)

// statsTTL is how long one walk of the store answers for. The walk costs a stat
// and a sidecar read per chunk, which is nothing once and too much per request
// from a dashboard polling every second.
const statsTTL = 5 * time.Second

// StatsResponse is the body of GET /v1/stats.
type StatsResponse struct {
	// StartedAt is when this process started, in unix seconds. Every "total"
	// below counts from here: the counters live in memory and a restart zeroes
	// them.
	StartedAt int64 `json:"startedAt" example:"1791283200"`

	Storage StorageStats `json:"storage"`
	Traffic TrafficStats `json:"traffic"`
	Clients ClientStats  `json:"clients"`
}

// StorageStats is what the store holds on disk.
type StorageStats struct {
	// Chunks and Bytes cover every chunk present, expired or not.
	Chunks int64 `json:"chunks" example:"1204"`
	Bytes  int64 `json:"bytes" example:"11712531264"`

	// ExpiredChunks and ExpiredBytes are the part of the above that is past its
	// TTL and waiting for the next sweep.
	ExpiredChunks int64 `json:"expiredChunks" example:"37"`
	ExpiredBytes  int64 `json:"expiredBytes" example:"359936592"`

	// AsOf is when the store was last walked, in unix seconds. The figures are
	// reused for a few seconds.
	AsOf int64 `json:"asOf" example:"1791369600"`
}

// CounterStats is one counter.
type CounterStats struct {
	// Total counts since startedAt.
	Total int64 `json:"total" example:"5210"`

	// Last24h is kept in hourly buckets, so it covers the hour in progress and
	// the 24 before it: between 24 and 25 hours.
	Last24h int64 `json:"last24h" example:"310"`
}

// TrafficStats is what the server has been asked to do.
type TrafficStats struct {
	Uploads       CounterStats `json:"uploads"`
	UploadedBytes CounterStats `json:"uploadedBytes"`

	// Downloads counts GET responses that carried chunk bytes, whole or a
	// Range. DownloadedBytes is what was actually sent, so an interrupted
	// transfer counts for as far as it got.
	Downloads       CounterStats `json:"downloads"`
	DownloadedBytes CounterStats `json:"downloadedBytes"`

	Deletes CounterStats `json:"deletes"`

	// GCExpired counts chunks reclaimed by this process's own sweeper. A `gc`
	// run from cron is another process and is not seen here.
	GCExpired CounterStats `json:"gcExpired"`

	Rejected RejectedStats `json:"rejected"`
}

// RejectedStats is the refused /v1 requests, by cause.
type RejectedStats struct {
	Unauthorized  CounterStats `json:"unauthorized"`
	NotFound      CounterStats `json:"notFound"`
	TooLarge      CounterStats `json:"tooLarge"`
	QuotaExceeded CounterStats `json:"quotaExceeded"`
	StorageFull   CounterStats `json:"storageFull"`
}

// ClientStats is how many distinct addresses have called a /v1 route.
type ClientStats struct {
	Total   int64 `json:"total" example:"842"`
	Last24h int64 `json:"last24h" example:"97"`

	// Approximate is always true. The server keeps no addresses; it counts them
	// with HyperLogLog sketches, which are exact for a handful of callers and
	// within about 1 % beyond that. An IPv6 caller is counted by its /64.
	Approximate bool `json:"approximate" example:"true"`
}

// handleStats reports how full and how busy the server is.
//
// @Summary     Storage and traffic figures
// @Description For the operator. Needs an enabled API key, except from a caller on the server itself: a loopback connection carrying no X-Forwarded-For, X-Real-IP or Forwarded header. A request relayed by a reverse proxy always needs the key. An extension of this implementation, not part of the contract — another backend may answer 404. Counters are held in memory and count from startedAt.
// @Tags        ops
// @Produce     json
// @Param       Authorization header string false "Bearer <apiKey>. Optional only for a caller on the server itself."
// @Success     200 {object} StatsResponse
// @Failure     401 {object} ErrorResponse "bad or missing API key"
// @Failure     503 {object} ErrorResponse "server not installed"
// @Router      /v1/stats [get]
func (s *Server) handleStats(c *gin.Context) {
	if !s.requireInstalled(c) {
		return
	}

	st := s.now()

	if !security.IsLocalCaller(c.Request) {
		if _, ok := security.Identify(st.cfg, c.Request); !ok {
			writeUnauthorized(c)
			return
		}
	}

	now := time.Now()
	usage, asOf := s.usage.get(st.store, now)
	seen := s.traffic.Snapshot(now)

	writeJSON(c, http.StatusOK, StatsResponse{
		StartedAt: s.startedAt.Unix(),
		Storage: StorageStats{
			Chunks:        usage.Chunks,
			Bytes:         usage.Bytes,
			ExpiredChunks: usage.ExpiredChunks,
			ExpiredBytes:  usage.ExpiredBytes,
			AsOf:          asOf.Unix(),
		},
		Traffic: TrafficStats{
			Uploads:         counterStats(seen.Uploads),
			UploadedBytes:   counterStats(seen.UploadedBytes),
			Downloads:       counterStats(seen.Downloads),
			DownloadedBytes: counterStats(seen.DownloadedBytes),
			Deletes:         counterStats(seen.Deletes),
			GCExpired:       counterStats(seen.Expired),
			Rejected: RejectedStats{
				Unauthorized:  counterStats(seen.Rejected.Unauthorized),
				NotFound:      counterStats(seen.Rejected.NotFound),
				TooLarge:      counterStats(seen.Rejected.TooLarge),
				QuotaExceeded: counterStats(seen.Rejected.QuotaExceeded),
				StorageFull:   counterStats(seen.Rejected.StorageFull),
			},
		},
		Clients: ClientStats{
			Total:       seen.Clients.Total,
			Last24h:     seen.Clients.Last24h,
			Approximate: true,
		},
	})
}

// -- internals ---------------------------------------------------------------

// usageCache holds the last walk of the store.
//
// The zero value is an empty cache.
type usageCache struct {
	mu    sync.Mutex
	store *storage.Store
	usage storage.Usage
	at    time.Time
}

// get returns the store's usage and when it was measured, walking the store
// only if the last answer is older than statsTTL.
//
// The lock is held across the walk on purpose: callers that arrive together
// wait for one walk rather than each starting their own. A different store
// means the install page has swapped the config, and the old answer describes
// a directory this server no longer uses.
func (u *usageCache) get(store *storage.Store, now time.Time) (storage.Usage, time.Time) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.store != store || now.Sub(u.at) >= statsTTL {
		u.store, u.usage, u.at = store, store.Usage(now), now
	}

	return u.usage, u.at
}

func counterStats(f traffic.Figure) CounterStats {
	return CounterStats{Total: f.Total, Last24h: f.Last24h}
}
