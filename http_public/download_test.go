package http_public

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"
)

// chunkTTL is the lifetime every fixture chunk here is stored with, so the
// Cache-Control assertion has something exact to compare against.
const chunkTTL = time.Hour

// maxAgePattern pulls the delta-seconds out of the Cache-Control header while
// also pinning the two directives around it.
var maxAgePattern = regexp.MustCompile(`^public, max-age=(\d+), immutable$`)

// TestChunkResponseCarriesTheCacheHeaders covers the six headers serveChunk
// sets before it looks at the Range header at all.
//
// The three rows are the three exits from that switch. They exist together
// because the headers being set *before* the branch is the property under test:
// a 416 is still a response about a real chunk, and a client that revalidates
// on one must get the same validator it would have got from a 200. Four of
// these six were asserted nowhere, in this repo or the PHP one.
func TestChunkResponseCarriesTheCacheHeaders(t *testing.T) {
	srv := newChunkServer(t, 20_000, chunkTTL)

	digest := sha256.Sum256(srv.payload)
	wantETag := `"` + hex.EncodeToString(digest[:])[:32] + `"`

	cases := []struct {
		label      string
		rangeSpec  string
		wantStatus int
		wantLength string
	}{
		{"the whole entity", "", http.StatusOK, "20000"},
		{"a satisfiable range", "bytes=1000-1999", http.StatusPartialContent, "1000"},
		{"an unsatisfiable range", "bytes=999999999-", http.StatusRequestedRangeNotSatisfiable, "0"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Logf("input:  GET %s, Range: %q", srv.chunkPath(), tc.rangeSpec)

			header := http.Header{}
			if tc.rangeSpec != "" {
				header = rangeHeader(tc.rangeSpec)
			}

			resp, body := srv.get(t, srv.chunkPath(), header)
			t.Logf("output: %d, %d body bytes", resp.StatusCode, len(body))
			for _, name := range []string{
				"Content-Type", "Accept-Ranges", "ETag", "Cache-Control",
				"X-Chunk-Expires", "X-Content-Type-Options", "Content-Length",
			} {
				t.Logf("        %s: %s", name, resp.Header.Get(name))
			}

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}

			fixed := map[string]string{
				"Content-Type":           "application/octet-stream",
				"Accept-Ranges":          "bytes",
				"X-Content-Type-Options": "nosniff",
				"ETag":                   wantETag,
				"X-Chunk-Expires":        strconv.FormatInt(srv.meta.ExpiresAt, 10),
				"Content-Length":         tc.wantLength,
			}
			for name, want := range fixed {
				if got := resp.Header.Get(name); got != want {
					t.Errorf("%s = %q, want %q", name, got, want)
				}
			}

			// max-age counts down in real time, so the assertion is a window:
			// it must be the remaining TTL, not a constant and not zero.
			cacheControl := resp.Header.Get("Cache-Control")
			m := maxAgePattern.FindStringSubmatch(cacheControl)
			if m == nil {
				t.Fatalf("Cache-Control = %q, want the form %q", cacheControl, "public, max-age=<n>, immutable")
			}

			maxAge, err := strconv.Atoi(m[1])
			if err != nil {
				t.Fatalf("max-age %q does not parse: %v", m[1], err)
			}

			ttl := int(chunkTTL / time.Second)
			if maxAge > ttl || maxAge < ttl-60 {
				t.Errorf("max-age = %d, want the remaining TTL, within [%d, %d]", maxAge, ttl-60, ttl)
			}
		})
	}

	// The whole-entity body is the bytes that went in, which is what makes the
	// ETag assertion above meaningful rather than self-referential.
	t.Run("the body is the stored ciphertext", func(t *testing.T) {
		resp, body := srv.get(t, srv.chunkPath(), http.Header{})
		t.Logf("output: %d, %d body bytes, equal to the payload = %t",
			resp.StatusCode, len(body), bytes.Equal(body, srv.payload))

		if !bytes.Equal(body, srv.payload) {
			t.Errorf("the served body is not the stored payload")
		}
	})
}

// TestUnsatisfiableRangeIsA416 promotes byterange_test.go's rejection table to
// the HTTP surface: the parser saying "unsatisfiable" has to become a 416 that
// tells the client the real size.
//
// bytes=-0 is the row that matters most. Go's own net/http range parser calls
// it a valid zero-length range and would answer 206 with
// "Content-Range: bytes 20000-19999/20000", which the client reads as a size
// mismatch and abandons the fetch over.
func TestUnsatisfiableRangeIsA416(t *testing.T) {
	srv := newChunkServer(t, 20_000, chunkTTL)

	wantContentRange := "bytes */" + strconv.FormatInt(srv.meta.Size, 10)

	for _, spec := range []string{
		"bytes=999999999-",
		"bytes=500-100",
		"bytes=-",
		"bytes=-0",
		"bytes=abc",
	} {
		t.Run(spec, func(t *testing.T) {
			t.Logf("input:  Range: %s", spec)

			resp, body := srv.get(t, srv.chunkPath(), rangeHeader(spec))
			t.Logf("output: %d, Content-Range: %q, Content-Length: %q, %d body bytes",
				resp.StatusCode, resp.Header.Get("Content-Range"), resp.Header.Get("Content-Length"), len(body))

			if resp.StatusCode != http.StatusRequestedRangeNotSatisfiable {
				t.Fatalf("status = %d, want 416", resp.StatusCode)
			}
			if got := resp.Header.Get("Content-Range"); got != wantContentRange {
				t.Errorf("Content-Range = %q, want %q — without it the client cannot learn the real size", got, wantContentRange)
			}
			if got := resp.Header.Get("Content-Length"); got != "0" {
				t.Errorf("Content-Length = %q, want %q", got, "0")
			}
			if len(body) != 0 {
				t.Errorf("a 416 carried %d bytes of body", len(body))
			}
		})
	}
}

// TestHeadSendsHeadersWithoutABody checks the half of HEAD that matters.
//
// The status was already covered; the Content-Length was not, and that is the
// part a client uses — eMuleQt sizes its receive buffer from it before it ever
// asks for the body.
func TestHeadSendsHeadersWithoutABody(t *testing.T) {
	srv := newChunkServer(t, 20_000, chunkTTL)

	cases := []struct {
		label           string
		rangeSpec       string
		wantStatus      int
		wantLength      string
		wantContentRnge string
	}{
		{"HEAD of the whole entity", "", http.StatusOK, "20000", ""},
		{"HEAD of a range", "bytes=1000-1999", http.StatusPartialContent, "1000", "bytes 1000-1999/20000"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Logf("input:  HEAD %s, Range: %q", srv.chunkPath(), tc.rangeSpec)

			header := http.Header{}
			if tc.rangeSpec != "" {
				header = rangeHeader(tc.rangeSpec)
			}

			resp, body, err := srv.do(t, http.MethodHead, srv.chunkPath(), header)
			if err != nil {
				t.Fatalf("reading the body: %v", err)
			}
			t.Logf("output: %d, Content-Length: %q, Content-Range: %q, %d body bytes",
				resp.StatusCode, resp.Header.Get("Content-Length"), resp.Header.Get("Content-Range"), len(body))

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}

			// The literal header, not resp.ContentLength: the client parses the
			// bytes off the wire, so that is the thing under test.
			if got := resp.Header.Get("Content-Length"); got != tc.wantLength {
				t.Errorf("Content-Length = %q, want %q", got, tc.wantLength)
			}
			if got := resp.Header.Get("Content-Range"); got != tc.wantContentRnge {
				t.Errorf("Content-Range = %q, want %q", got, tc.wantContentRnge)
			}
			if len(body) != 0 {
				t.Errorf("HEAD returned %d bytes of body", len(body))
			}
		})
	}
}

// TestConditionalGet covers the If-None-Match handling, which had no test at
// all — including the deliberate decision not to honour it on a Range request.
func TestConditionalGet(t *testing.T) {
	srv := newChunkServer(t, 20_000, chunkTTL)
	etag := srv.meta.ETag()

	t.Logf("the chunk's ETag is %s", etag)

	cases := []struct {
		label      string
		ifNoneName string
		header     http.Header
		method     string
		wantStatus int
		wantBody   int
	}{
		{
			label:      "a matching ETag is a 304",
			header:     http.Header{"If-None-Match": {etag}},
			wantStatus: http.StatusNotModified,
			wantBody:   0,
		},
		{
			// The only coverage trimSpace has.
			label:      "a padded ETag still matches",
			header:     http.Header{"If-None-Match": {"  \t" + etag + " \t "}},
			wantStatus: http.StatusNotModified,
			wantBody:   0,
		},
		{
			label:      "a stale ETag gets the entity",
			header:     http.Header{"If-None-Match": {`"0123456789abcdef0123456789abcdef"`}},
			wantStatus: http.StatusOK,
			wantBody:   20_000,
		},
		{
			// RFC 9110 section 13.1.2 would make "*" match any existing entity,
			// so this row pins a deliberate deviation. RangeResponse.php:69
			// compares trim($inm) === $etag with no wildcard case, the eMuleQt
			// client never sends "*", and parity with the reference server is
			// the contract this port is judged against — so both answer 200.
			label:      "a wildcard is not treated as a match, matching the PHP server",
			header:     http.Header{"If-None-Match": {"*"}},
			wantStatus: http.StatusOK,
			wantBody:   20_000,
		},
		{
			// A 304 answering a Range request would tell a resuming downloader
			// nothing it can act on: it already knows the entity is unchanged,
			// it wants the bytes it is missing.
			label:      "a matching ETag on a range request still serves the range",
			header:     http.Header{"If-None-Match": {etag}, "Range": {"bytes=1000-1999"}},
			wantStatus: http.StatusPartialContent,
			wantBody:   1000,
		},
		{
			label:      "HEAD with a matching ETag is a 304",
			header:     http.Header{"If-None-Match": {etag}},
			method:     http.MethodHead,
			wantStatus: http.StatusNotModified,
			wantBody:   0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			t.Logf("input:  %s %s, If-None-Match: %q, Range: %q",
				method, srv.chunkPath(), tc.header.Get("If-None-Match"), tc.header.Get("Range"))

			resp, body, err := srv.do(t, method, srv.chunkPath(), tc.header)
			if err != nil {
				t.Fatalf("reading the body: %v", err)
			}
			t.Logf("output: %d, %d body bytes, ETag: %q", resp.StatusCode, len(body), resp.Header.Get("ETag"))

			if resp.StatusCode != tc.wantStatus {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if len(body) != tc.wantBody {
				t.Errorf("body is %d bytes, want %d", len(body), tc.wantBody)
			}

			// A 304 must still carry the validators, or the next request cannot
			// revalidate and the client refetches the whole 9.7 MB.
			//
			// Content-Type and Content-Length are deliberately not asserted
			// here: Go's server strips both for statuses that allow no body,
			// while PHP's header() calls have already fired by the time it
			// picks the status, so the reference server leaves Content-Type in
			// place. Go is the more correct of the two, and no client on this
			// path sends If-None-Match at all.
			if resp.StatusCode == http.StatusNotModified {
				if got := resp.Header.Get("ETag"); got != etag {
					t.Errorf("the 304 dropped its ETag: %q, want %q", got, etag)
				}
				if resp.Header.Get("Cache-Control") == "" {
					t.Errorf("the 304 carries no Cache-Control")
				}
			}
		})
	}
}

// TestExpiredChunkIsA404 covers the handler end of expiry. pkg/storage proves
// Meta hides a lapsed chunk; this proves the route answers 404 rather than
// serving it or failing some other way, and that the body is the uniform error
// shape every other refusal uses.
func TestExpiredChunkIsA404(t *testing.T) {
	srv := newChunkServer(t, 4096, -time.Hour)

	t.Logf("input:  GET %s (stored with a TTL of -1h, expiresAt %d)", srv.chunkPath(), srv.meta.ExpiresAt)

	resp, body := srv.get(t, srv.chunkPath(), http.Header{})
	t.Logf("output: %d, body: %s", resp.StatusCode, bytes.TrimSpace(body))

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}

	var payload ErrorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("the 404 body is not the uniform error shape: %v", err)
	}
	if payload.Status != http.StatusNotFound || payload.Error != "not found" {
		t.Errorf("body = %+v, want {Error: \"not found\", Status: 404}", payload)
	}

	// Expiry is not deletion: a GET must never do write work, and only a sweep
	// may reclaim the bytes.
	if !srv.store.Exists(srv.meta.ID) {
		t.Errorf("serving an expired chunk deleted it; only Gc.Sweep may do that")
	}
}

// TestShortBlobAbortsRatherThanTruncating covers the abort path in
// streamRange, reached by truncating a blob underneath a live server.
//
// The assertion is deliberately about an invariant rather than a status code.
// Headers — Content-Length included — are committed before the first byte is
// read, so once a blob turns out to be short there is no way to signal the
// fault in band. What must not happen is the client receiving a body it could
// mistake for the whole chunk: eMuleQt would decrypt the short buffer, get
// garbage, and report Corrupt, and three of those retire a healthy cache entry.
// A dropped connection is recoverable; a plausible-looking truncation is not.
func TestShortBlobAbortsRatherThanTruncating(t *testing.T) {
	srv := newChunkServer(t, 20_000, chunkTTL)

	// Meta resolves from the sidecar, so the handler commits headers before it
	// reads a byte. Truncating after ingest produces exactly that mismatch.
	if err := os.Truncate(srv.store.BlobPath(srv.meta.ID), srv.meta.Size-100); err != nil {
		t.Fatalf("truncating the blob: %v", err)
	}
	t.Logf("input:  GET %s, blob truncated to %d of the %d bytes the sidecar claims",
		srv.chunkPath(), srv.meta.Size-100, srv.meta.Size)

	resp, body, readErr := srv.do(t, http.MethodGet, srv.chunkPath(), http.Header{})
	declared := resp.Header.Get("Content-Length")
	t.Logf("output: %d, Content-Length: %q, %d body bytes, read error: %v",
		resp.StatusCode, declared, len(body), readErr)

	// Either the read fails, or it returns something visibly short of the
	// declared length. Both leave the client able to tell it was cut off.
	if readErr == nil && strconv.Itoa(len(body)) == declared {
		t.Errorf("a truncated chunk was served as a complete %d-byte body; "+
			"the client would decrypt it, get garbage, and blame the cache entry", len(body))
	}
}

// TestMissingBlobIsA404 pins the guard that stops the case above from being
// reachable the easy way.
//
// Store.Meta stats the blob as well as reading the sidecar, so a chunk whose
// bytes are gone resolves as absent and never reaches streamRange. Without that
// stat this would commit a 200 and a Content-Length it could not honour, which
// is strictly worse for the client than a clean refusal. The sidecar is left in
// place here precisely to prove the stat is what does the work.
func TestMissingBlobIsA404(t *testing.T) {
	srv := newChunkServer(t, 20_000, chunkTTL)

	if err := os.Remove(srv.store.BlobPath(srv.meta.ID)); err != nil {
		t.Fatalf("removing the blob: %v", err)
	}
	t.Logf("input:  GET %s, blob removed, sidecar left in place", srv.chunkPath())

	resp, body := srv.get(t, srv.chunkPath(), http.Header{})
	t.Logf("output: %d, body: %s", resp.StatusCode, bytes.TrimSpace(body))

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 — a chunk with no bytes must be refused, not half-served", resp.StatusCode)
	}

	var payload ErrorResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("the 404 body is not the uniform error shape: %v", err)
	}
	if payload.Status != http.StatusNotFound {
		t.Errorf("body = %+v, want status 404", payload)
	}
}
