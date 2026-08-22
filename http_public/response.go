package http_public

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// ErrorResponse is the uniform error body every failing route returns.
//
// A non-Go backend has something exact to mirror, and eMuleQt reads the "error"
// key when it classifies a refused upload.
type ErrorResponse struct {
	Error  string `json:"error" example:"not found"`
	Status int    `json:"status" example:"404"`
}

// writeJSON sends a JSON body with an explicit Content-Length.
//
// Encoded into a buffer first so a marshalling failure becomes a clean 500
// rather than a half-written body under a 200, and so Content-Length is exact.
// HTML escaping is off: the PHP server does not escape <, > or & either, and
// these bodies are never interpolated into a page.
func writeJSON(c *gin.Context, code int, payload any) {
	var buf bytes.Buffer

	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(payload); err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}

	header := c.Writer.Header()
	header.Set("Content-Type", "application/json; charset=utf-8")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Length", strconv.Itoa(buf.Len()))

	c.Status(code)
	_, _ = c.Writer.Write(buf.Bytes())
}

// writeError sends the uniform error shape.
func writeError(c *gin.Context, code int, message string) {
	writeJSON(c, code, ErrorResponse{Error: message, Status: code})
}

// writeNoContent answers 204, which must carry no body at all.
func writeNoContent(c *gin.Context) {
	c.Writer.Header().Set("Cache-Control", "no-store")
	c.Status(http.StatusNoContent)
}

// writeUnauthorized answers 401 with the challenge the contract specifies.
func writeUnauthorized(c *gin.Context) {
	c.Writer.Header().Set("WWW-Authenticate", `Bearer realm="emule-http-cache"`)
	writeError(c, http.StatusUnauthorized, "invalid or missing API key")
}

// writeNotFound answers 404.
//
// Every "you may not have this" path funnels through here, including a chunk
// owned by another key: reporting a foreign chunk as absent rather than
// forbidden is what stops a valid key being used to probe the id space.
func writeNotFound(c *gin.Context) {
	writeError(c, http.StatusNotFound, "not found")
}

// writeMethodNotAllowed answers 405 with the Allow header the RFC requires and
// the same JSON body as every other error.
func writeMethodNotAllowed(c *gin.Context, allowed string) {
	c.Writer.Header().Set("Allow", allowed)
	writeError(c, http.StatusMethodNotAllowed, "method not allowed")
}
