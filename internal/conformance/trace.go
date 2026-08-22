package conformance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

// tracer reports every exchange to the Reporter.
//
// Bodies are never logged, only their length and digest: a request body is
// ciphertext and a chunk URL is a bearer token, so the trace stays useful
// without becoming a way to leak either. Credential headers are redacted for
// the same reason.
type tracer struct {
	next     http.RoundTripper
	reporter Reporter
}

func (t *tracer) RoundTrip(req *http.Request) (*http.Response, error) {
	t.reporter.Logf("--> %s %s", req.Method, req.URL)
	for name, values := range req.Header {
		t.reporter.Logf("    %s: %s", name, redact(name, strings.Join(values, ", ")))
	}
	if req.ContentLength >= 0 {
		t.reporter.Logf("    (request body %d bytes)", req.ContentLength)
	} else {
		t.reporter.Logf("    (request body of unknown length — sent chunked)")
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		t.reporter.Logf("<-- transport error: %v", err)
		return nil, err
	}

	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if readErr != nil {
		t.reporter.Logf("<-- %s (body unreadable: %v)", resp.Status, readErr)
		return nil, readErr
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))

	t.reporter.Logf("<-- %s", resp.Status)
	for _, name := range []string{
		"Content-Type", "Content-Length", "Content-Range", "Accept-Ranges",
		"ETag", "Cache-Control", "X-Chunk-Expires", "Location", "Allow",
		"Retry-After", "WWW-Authenticate", "Transfer-Encoding",
	} {
		if value := resp.Header.Get(name); value != "" {
			t.reporter.Logf("    %s: %s", name, value)
		}
	}

	digest := sha256.Sum256(body)
	t.reporter.Logf("    (response body %d bytes, sha256 %s)", len(body), hex.EncodeToString(digest[:]))

	return resp, nil
}

// redact hides a credential that would otherwise be written to a log or a
// terminal for every single request.
func redact(name, value string) string {
	switch strings.ToLower(name) {
	case "authorization", "x-api-key":
		return "<redacted>"
	default:
		return value
	}
}
