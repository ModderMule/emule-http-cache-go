package http_public

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

const statsSecret = "ops-secret"

func TestStatsAuth(t *testing.T) {
	cs := newStatsServer(t, 64, time.Hour)

	cases := []struct {
		label  string
		method string
		header http.Header
		want   int
	}{
		// The test server is reached over loopback, which is what a shell on
		// the host looks like.
		{"a local caller needs no key", "GET", nil, 200},
		{"HEAD works like GET", "HEAD", nil, 200},

		// Behind nginx every request comes from loopback too, and only the
		// forwarding header tells them apart.
		{"a proxied caller with no key", "GET", http.Header{"X-Forwarded-For": {"198.51.100.7"}}, 401},
		{"X-Real-IP alone is enough to need a key", "GET", http.Header{"X-Real-IP": {"198.51.100.7"}}, 401},
		{"Forwarded alone is enough to need a key", "GET", http.Header{"Forwarded": {"for=198.51.100.7"}}, 401},
		{"a forwarding header claiming loopback", "GET", http.Header{"X-Forwarded-For": {"127.0.0.1"}}, 401},
		{"a proxied caller with a wrong key", "GET", http.Header{
			"X-Forwarded-For": {"198.51.100.7"}, "Authorization": {"Bearer nope"}}, 401},
		{"a proxied caller with the key", "GET", http.Header{
			"X-Forwarded-For": {"198.51.100.7"}, "Authorization": {"Bearer " + statsSecret}}, 200},
		{"a proxied caller with the key in X-Api-Key", "GET", http.Header{
			"X-Real-IP": {"198.51.100.7"}, "X-Api-Key": {statsSecret}}, 200},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			resp, body, err := cs.do(t, tc.method, "/v1/stats", tc.header)
			if err != nil {
				t.Fatalf("reading the body: %v", err)
			}
			t.Logf("input:  %s /v1/stats %v", tc.method, tc.header)
			t.Logf("output: %d %s", resp.StatusCode, body)

			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
			if tc.want == 401 && resp.Header.Get("WWW-Authenticate") == "" {
				t.Errorf("a 401 must carry WWW-Authenticate")
			}
			if tc.method == "HEAD" && len(body) != 0 {
				t.Errorf("a HEAD response carried a body")
			}
		})
	}
}

func TestStatsNeedsAnInstalledServer(t *testing.T) {
	ts := newInstallServer(t)

	resp, err := ts.Client().Get(ts.URL + "/v1/stats")
	if err != nil {
		t.Fatalf("GET /v1/stats: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	t.Logf("input:  GET /v1/stats on a server with no config")
	t.Logf("output: %d %s", resp.StatusCode, body)

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}

func TestStatsStorage(t *testing.T) {
	// One chunk, already past its TTL: on disk until a sweep, so counted, and
	// counted as expired.
	cs := newStatsServer(t, 1_000, -time.Hour)
	before := time.Now().Unix()

	first := cs.stats(t)

	want := StorageStats{Chunks: 1, Bytes: 1_000, ExpiredChunks: 1, ExpiredBytes: 1_000, AsOf: first.Storage.AsOf}
	if first.Storage != want {
		t.Errorf("storage = %+v, want %+v", first.Storage, want)
	}
	if first.Storage.AsOf < before || first.Storage.AsOf > time.Now().Unix() {
		t.Errorf("asOf = %d, want the time of this request (about %d)", first.Storage.AsOf, before)
	}
	if first.StartedAt <= 0 || first.StartedAt > first.Storage.AsOf {
		t.Errorf("startedAt = %d, want a time at or before asOf %d", first.StartedAt, first.Storage.AsOf)
	}

	// A chunk stored inside the cache window does not show until it lapses.
	// That is the trade: the figures may be a few seconds old, and asOf says so.
	if _, err := cs.store.Ingest(bytes.NewReader(make([]byte, 500)), "test", time.Hour, 500); err != nil {
		t.Fatalf("storing a second chunk: %v", err)
	}
	t.Logf("input:  a second chunk of 500 bytes, stored inside the %s cache window", statsTTL)

	second := cs.stats(t)
	if second.Storage != first.Storage {
		t.Errorf("the store was walked again inside the cache window:\n first: %+v\nsecond: %+v",
			first.Storage, second.Storage)
	}
}

func TestStatsTraffic(t *testing.T) {
	cs := newStatsServer(t, 1_000, time.Hour)
	key := http.Header{"Authorization": {"Bearer " + statsSecret}}

	// Each step is one request and the status it must get, so a figure that is
	// off can be traced to the request that should have moved it.
	steps := []struct {
		label  string
		method string
		path   string
		header http.Header
		body   []byte
		want   int
	}{
		{"a whole chunk", "GET", cs.chunkPath(), nil, nil, 200},
		{"the first 100 bytes of it", "GET", cs.chunkPath(), rangeHeader("bytes=0-99"), nil, 206},
		{"its headers only", "HEAD", cs.chunkPath(), nil, nil, 200},
		{"a chunk that does not exist", "GET", "/v1/chunks/00000000000000000000000000000000", nil, nil, 404},
		{"a route that does not exist", "GET", "/v1/nope", nil, nil, 404},
		{"an upload with a wrong key", "POST", "/v1/chunks",
			http.Header{"Authorization": {"Bearer nope"}}, make([]byte, 50), 401},
		{"an upload of 50 bytes", "POST", "/v1/chunks", key, make([]byte, 50), 201},
		{"an upload over maxChunkSize", "POST", "/v1/chunks", key, make([]byte, cs.cfg.Storage.MaxChunkSize+1), 413},
		{"deleting the fixture chunk", "DELETE", cs.chunkPath(), key, nil, 204},
		{"the status page, which is not a /v1 route", "GET", "/", nil, nil, 200},
	}

	for _, step := range steps {
		status := cs.send(t, step.method, step.path, step.header, step.body)
		t.Logf("input:  %s %s (%s, %d byte body)", step.method, scrubPath(step.path), step.label, len(step.body))
		t.Logf("output: %d", status)

		if status != step.want {
			t.Fatalf("%s: status = %d, want %d", step.label, status, step.want)
		}
	}

	got := cs.stats(t)

	one := func(n int64) CounterStats { return CounterStats{Total: n, Last24h: n} }
	want := TrafficStats{
		Uploads:         one(1),
		UploadedBytes:   one(50),
		Downloads:       one(2),
		DownloadedBytes: one(1_100),
		Deletes:         one(1),
		Rejected: RejectedStats{
			Unauthorized: one(1),
			NotFound:     one(2),
			TooLarge:     one(1),
		},
	}
	if got.Traffic != want {
		t.Errorf("traffic differs\n got: %+v\nwant: %+v", got.Traffic, want)
	}

	// Every request above came from the test's own loopback address.
	if got.Clients != (ClientStats{Total: 1, Last24h: 1, Approximate: true}) {
		t.Errorf("clients = %+v, want one caller", got.Clients)
	}
}

func TestStatsCountsCallersBehindAProxy(t *testing.T) {
	cs := newStatsServer(t, 64, time.Hour)

	// What nginx on this host sends: a loopback peer naming the real caller.
	// One address calls twice and must be counted once.
	callers := []string{"198.51.100.7", "198.51.100.8", "198.51.100.7", "2001:db8::1"}
	for _, caller := range callers {
		status := cs.send(t, "GET", "/v1/info", http.Header{"X-Real-IP": {caller}}, nil)
		t.Logf("input:  GET /v1/info, X-Real-IP: %s", caller)
		t.Logf("output: %d", status)
	}

	// Reading the figures is not counted, with or without a refusal.
	cs.send(t, "GET", "/v1/stats", http.Header{"X-Real-IP": {"192.0.2.99"}}, nil)

	got := cs.stats(t)
	if got.Clients != (ClientStats{Total: 3, Last24h: 3, Approximate: true}) {
		t.Errorf("clients = %+v, want three callers", got.Clients)
	}
	if got.Traffic.Rejected.Unauthorized.Total != 1 {
		t.Errorf("unauthorized = %d, want 1: a refused /v1/stats is still a refusal",
			got.Traffic.Rejected.Unauthorized.Total)
	}
}

// -- the harness -------------------------------------------------------------

// newStatsServer is newChunkServer with one enabled API key, which the stats
// route needs for any caller that is not local.
func newStatsServer(t *testing.T, size int, ttl time.Duration) *chunkServer {
	t.Helper()

	cs := newChunkServer(t, size, ttl)

	// The fixture's chunk belongs to "test", so the key has to as well for the
	// delete step to be allowed.
	cs.cfg.APIKeys = []config.APIKey{{ID: "test", Secret: statsSecret, Enabled: true}}

	return cs
}

// stats fetches and decodes /v1/stats as a local caller.
func (s *chunkServer) stats(t *testing.T) StatsResponse {
	t.Helper()

	resp, body := s.get(t, "/v1/stats", nil)
	t.Logf("input:  GET /v1/stats")
	t.Logf("output: %d %s", resp.StatusCode, body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/stats: status %d", resp.StatusCode)
	}

	var stats StatsResponse
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&stats); err != nil {
		t.Fatalf("decoding /v1/stats: %v", err)
	}

	return stats
}

// send is do() with a request body, returning only the status. The response
// body is read to the end so a download is counted in full.
func (s *chunkServer) send(t *testing.T, method, path string, header http.Header, body []byte) int {
	t.Helper()

	req, err := http.NewRequest(method, s.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("building a %s %s request: %v", method, path, err)
	}
	for name, values := range header {
		req.Header[http.CanonicalHeaderKey(name)] = values
	}

	resp, err := s.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Fatalf("reading the body of %s %s: %v", method, path, err)
	}

	return resp.StatusCode
}
