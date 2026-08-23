package http_public

import (
	"net/http"
	"strconv"
	"time"

	"github.com/ModderMule/emule-http-cache-go/pkg/storage"
	"github.com/gin-gonic/gin"
)

// sliceWriteTimeout bounds one 512 KiB write, not the whole transfer.
//
// http.Server.WriteTimeout is an absolute deadline from the start of the
// request, so any value large enough for 9.7 MB on a slow link stops being a
// stall detector. Rolling a deadline per slice is what distinguishes "slow but
// progressing" from "stalled", and it is the closest analogue of the PHP
// server's connection_aborted() check.
const sliceWriteTimeout = 60 * time.Second

// downloadSlice is the write slice size, matching the PHP server's SEND_CHUNK
// so neither implementation ever holds a whole chunk in memory.
const downloadSlice = 512 * 1024

// serveChunk answers a chunk download.
//
// Every response carries an explicit Content-Length. That is not tidiness: with
// none set, net/http switches a body this size to Transfer-Encoding: chunked,
// and eMuleQt reads chunk responses over a hand-built socket with no chunked
// decoder — it would feed the hex length lines straight into SHA-256 and
// AES-CBC. The failure surfaces as a corrupt part, not as an HTTP error.
func (s *Server) serveChunk(c *gin.Context, store *storage.Store, meta *storage.ChunkMeta) {
	now := time.Now()
	etag := meta.ETag()

	header := c.Writer.Header()
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Accept-Ranges", "bytes")
	header.Set("ETag", etag)
	header.Set("Cache-Control", "public, max-age="+strconv.FormatInt(meta.MaxAge(now), 10)+", immutable")
	header.Set("X-Chunk-Expires", strconv.FormatInt(meta.ExpiresAt, 10))
	header.Set("X-Content-Type-Options", "nosniff")

	span, verdict := parseByteRange(c.GetHeader("Range"), meta.Size)

	switch verdict {
	case rangeUnsatisfiable:
		header.Set("Content-Range", "bytes */"+strconv.FormatInt(meta.Size, 10))
		header.Set("Content-Length", "0")
		c.Status(http.StatusRequestedRangeNotSatisfiable)

	case rangeSatisfiable:
		header.Set("Content-Range", "bytes "+
			strconv.FormatInt(span.from, 10)+"-"+
			strconv.FormatInt(span.to, 10)+"/"+
			strconv.FormatInt(meta.Size, 10))
		header.Set("Content-Length", strconv.FormatInt(span.length(), 10))
		c.Status(http.StatusPartialContent)

		if !isHead(c) {
			s.streamRange(c, store, meta.ID, span)
		}

	default:
		// A conditional GET is only meaningful for the full entity here: a 304
		// answering a Range request would tell a resuming downloader nothing it
		// can act on.
		if match := c.GetHeader("If-None-Match"); match != "" && trimSpace(match) == etag {
			c.Status(http.StatusNotModified)
			return
		}

		header.Set("Content-Length", strconv.FormatInt(meta.Size, 10))
		c.Status(http.StatusOK)

		if !isHead(c) {
			s.streamRange(c, store, meta.ID, byteRange{from: 0, to: meta.Size - 1})
		}
	}
}

// -- internals ---------------------------------------------------------------

// streamRange copies the requested span of a stored blob to the client in
// 512 KiB slices, matching the PHP server's SEND_CHUNK so neither ever holds a
// whole chunk in memory.
func (s *Server) streamRange(c *gin.Context, store *storage.Store, id string, span byteRange) {
	f, err := store.OpenBlob(id)
	if err != nil {
		// The sidecar said this chunk exists, so the blob vanishing under us is
		// a real fault. Headers are already committed and the status cannot be
		// changed, so the only honest answer is to drop the connection: the
		// client then resumes from what it has, which beats leaving a
		// Content-Length-framed response hanging forever.
		panic(http.ErrAbortHandler)
	}
	defer f.Close()

	ctrl := http.NewResponseController(c.Writer)
	ctx := c.Request.Context()

	buf := make([]byte, downloadSlice)
	offset, remaining := span.from, span.length()

	for remaining > 0 {
		// Client gone. The analogue of PHP's connection_aborted().
		if err := ctx.Err(); err != nil {
			return
		}

		size := int64(len(buf))
		if remaining < size {
			size = remaining
		}

		// ReadAt rather than Seek+Read: no shared cursor, so the same handle
		// stays safe if it is ever cached across requests.
		n, err := f.ReadAt(buf[:size], offset)
		if n > 0 {
			// Ignored deliberately: a ResponseWriter that cannot carry a
			// deadline is not a reason to refuse the transfer.
			_ = ctrl.SetWriteDeadline(time.Now().Add(sliceWriteTimeout))

			if _, werr := c.Writer.Write(buf[:n]); werr != nil {
				return // the client hung up mid-transfer
			}

			offset += int64(n)
			remaining -= int64(n)
		}

		if err != nil {
			// ReadAt reports io.EOF as soon as it returns a short slice, so an
			// error with nothing left to send is ordinary completion. An error
			// with bytes still owed means the blob is shorter than its sidecar
			// promised, and the client must not be left waiting on a
			// Content-Length that will never arrive.
			if remaining > 0 {
				panic(http.ErrAbortHandler)
			}
			return
		}
	}
}

func isHead(c *gin.Context) bool {
	return c.Request.Method == http.MethodHead
}

func trimSpace(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}

	return s
}
