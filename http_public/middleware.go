package http_public

import (
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ModderMule/emule-http-cache-go/internal/security"
	"github.com/ModderMule/emule-http-cache-go/internal/traffic"
)

// chunkIDInPath matches the capability inside a chunk URL so it can be kept out
// of the logs.
var chunkIDInPath = regexp.MustCompile(`(/v1/chunks/)[0-9a-f]{32}`)

// requestLogger logs one line per request, with chunk ids scrubbed.
//
// A chunk URL is a bearer token: the 128-bit id is the only thing guarding that
// chunk's ciphertext, so writing it to a log file — or to whatever ships those
// files onward — would hand the capability to anyone who can read them. This is
// the same substitution the PHP server's nginx sample makes with its
// "map $request_uri $scrubbed_uri" block. Credentials and bodies are never
// logged at all.
func (s *Server) requestLogger() gin.HandlerFunc {
	if !s.accessLog {
		return func(c *gin.Context) { c.Next() }
	}

	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		s.logger.Infof("%s %s -> %d (%s)",
			c.Request.Method,
			scrubPath(c.Request.URL.Path),
			c.Writer.Status(),
			time.Since(start).Round(time.Millisecond))
	}
}

// trafficRecorder counts what each /v1 request turned out to be.
//
// It reads the outcome off the response rather than being called from the
// handlers, so a new refusal path is counted without anyone remembering to.
// The counting is deferred because a download whose blob vanishes mid-transfer
// ends in a panic, and the bytes it did send still left the building.
func (s *Server) trafficRecorder() gin.HandlerFunc {
	base := s.timeouts.BasePath

	return func(c *gin.Context) {
		defer func() {
			path, ok := strings.CutPrefix(c.Request.URL.Path, base)
			if !ok || !strings.HasPrefix(path, "/v1/") {
				return
			}

			now := time.Now()
			status := c.Writer.Status()

			// The route, not the path: an unmatched path has none and a chunk
			// id must not need parsing twice.
			route := strings.TrimPrefix(c.FullPath(), base)

			switch {
			case route == "/v1/chunks" && status == http.StatusCreated:
				s.traffic.Upload(c.Request.ContentLength, now)

			case route == "/v1/chunks/:id" && c.Request.Method == http.MethodGet &&
				(status == http.StatusOK || status == http.StatusPartialContent):
				// What was written, not what was promised: a client that hung
				// up halfway downloaded half.
				s.traffic.Download(int64(max(c.Writer.Size(), 0)), now)

			case route == "/v1/chunks/:id" && c.Request.Method == http.MethodDelete &&
				status == http.StatusNoContent:
				s.traffic.Delete(now)
			}

			if cause, refused := rejectionCause(status); refused {
				s.traffic.Reject(cause, now)
			}

			// The operator reading the figures is not one of the clients they
			// describe.
			if route != "/v1/stats" {
				s.traffic.Seen(security.ClientIP(c.Request), now)
			}
		}()

		c.Next()
	}
}

// rejectionCause maps a response status to the refusal it stands for.
func rejectionCause(status int) (traffic.Cause, bool) {
	switch status {
	case http.StatusUnauthorized:
		return traffic.Unauthorized, true
	case http.StatusNotFound:
		return traffic.NotFound, true
	case http.StatusRequestEntityTooLarge:
		return traffic.TooLarge, true
	case http.StatusTooManyRequests:
		return traffic.QuotaExceeded, true
	case http.StatusInsufficientStorage:
		return traffic.StorageFull, true
	}

	return 0, false
}

// scrubPath replaces a chunk id with a placeholder, leaving the endpoint
// legible.
func scrubPath(path string) string {
	return chunkIDInPath.ReplaceAllString(path, "${1}<id>")
}
