package http_public

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/ModderMule/emule-http-cache-go/internal/security"
	"github.com/ModderMule/emule-http-cache-go/internal/storage"
)

// handleDownload serves a stored chunk.
//
// @Summary     Fetch a chunk
// @Description Unauthenticated by design: the 128-bit id is the capability, and the body is ciphertext the server cannot read. Requiring a key here would mean sharing the uploader's credential with every downloader. Single-range requests are answered with a 206; a multi-range request gets the whole entity, which RFC 9110 section 14.2 permits and which is what the eMuleQt downloader can actually parse. HEAD behaves identically without a body.
// @Tags        chunks
// @Produce     octet-stream
// @Param       id    path   string true  "Chunk id, 32 lowercase hex characters"
// @Param       Range header string false "Single byte range, e.g. bytes=0-9728015"
// @Success     200 {string} binary "the whole ciphertext"
// @Success     206 {string} binary "the requested span"
// @Header      200 {string} ETag            "First 32 hex characters of the ciphertext digest"
// @Header      200 {string} X-Chunk-Expires "Unix time the chunk lapses"
// @Header      206 {string} Content-Range   "bytes <first>-<last>/<total>"
// @Failure     404 {object} ErrorResponse "unknown, expired, or a malformed id"
// @Failure     416 {object} ErrorResponse "range not satisfiable"
// @Failure     503 {object} ErrorResponse "server not installed"
// @Router      /v1/chunks/{id} [get]
func (s *Server) handleDownload(c *gin.Context) {
	if !s.requireInstalled(c) {
		return
	}

	st := s.now()

	meta, ok := s.chunk(c, st.store)
	if !ok {
		return
	}

	s.serveChunk(c, st.store, meta)
}

// handleDelete drops a chunk on behalf of its uploader.
//
// @Summary     Delete a chunk
// @Description Only the uploader's key works. A chunk belonging to another key reports 404, not 403, so a valid key cannot be used to probe the id space. Nobody can authenticate as the reserved "anonymous" owner, so an open server's chunks are undeletable by design and lapse at their TTL. A 204 guarantees the chunk is gone; a backend that could not remove it says so rather than let the client believe a still-downloadable chunk was deleted.
// @Tags        chunks
// @Produce     json
// @Param       id            path   string true "Chunk id, 32 lowercase hex characters"
// @Param       Authorization header string true "Bearer <apiKey>"
// @Success     204 "the chunk is gone"
// @Failure     401 {object} ErrorResponse "bad or missing API key"
// @Failure     404 {object} ErrorResponse "unknown, expired, already deleted, or owned by another key"
// @Failure     500 {object} ErrorResponse "the chunk could not be removed and is still on disk"
// @Failure     503 {object} ErrorResponse "server not installed"
// @Router      /v1/chunks/{id} [delete]
func (s *Server) handleDelete(c *gin.Context) {
	if !s.requireInstalled(c) {
		return
	}

	st := s.now()

	id := c.Param("id")
	if !storage.IsValidID(id) {
		writeNotFound(c)
		return
	}

	keyID, ok := security.Identify(st.cfg, c.Request)
	if !ok {
		writeUnauthorized(c)
		return
	}

	meta, found := st.store.Meta(id)
	if !found || !security.OwnsChunk(meta.OwnerKeyID, keyID) {
		writeNotFound(c)
		return
	}

	// A delete that did not happen must not be reported as done: an unwritable
	// shard directory would leave the client believing the chunk is gone while
	// it stays downloadable until its TTL lapses. A concurrent delete that got
	// there first is still a success — the caller asked for the chunk to be
	// absent, and it is.
	if !st.store.Delete(id) && st.store.Exists(id) {
		writeError(c, http.StatusInternalServerError, "chunk could not be removed")
		return
	}

	writeNoContent(c)
}

// -- internals ---------------------------------------------------------------

// chunk resolves the :id path parameter to a live chunk, answering 404 and
// reporting false when there is none.
//
// Validating the id is the first thing every chunk route does, and it is the
// whole defence against a hostile path reaching the filesystem: gin hands back
// a decoded parameter, so "%2e%2e%2f" arrives as "../". A well-formed path with
// a malformed id is a 404 rather than a 400, because the id space is opaque to
// clients and must not leak validation detail.
func (s *Server) chunk(c *gin.Context, store *storage.Store) (*storage.ChunkMeta, bool) {
	id := c.Param("id")
	if !storage.IsValidID(id) {
		writeNotFound(c)
		return nil, false
	}

	meta, found := store.Meta(id)
	if !found {
		writeNotFound(c)
		return nil, false
	}

	return meta, true
}
