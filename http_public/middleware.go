package http_public

import (
	"regexp"
	"time"

	"github.com/gin-gonic/gin"
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

// scrubPath replaces a chunk id with a placeholder, leaving the endpoint
// legible.
func scrubPath(path string) string {
	return chunkIDInPath.ReplaceAllString(path, "${1}<id>")
}
