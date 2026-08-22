package http_public

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/security"
	"github.com/ModderMule/emule-http-cache-go/internal/storage"
)

// storageFullRetryAfter is what a 507 suggests waiting.
//
// Free space is not time-based, but a 507 falls in the client's escalating 5xx
// bucket (1 -> 5 -> 15 -> 30 min) and it takes the longer of its own backoff
// and ours, so this only ever pins the first retry at something more realistic
// for a full disk than one minute.
const storageFullRetryAfter = 300

// UploadResponse is the body of a successful POST /v1/chunks.
//
// Size and Expires are integers so they marshal as JSON numbers: eMuleQt reads
// them with a numeric accessor that silently yields 0 for a string. Expires is
// an absolute unix timestamp, never a duration — a client declines any offer
// whose expiry is less than 120 seconds away, so a TTL here would make every
// chunk unusable.
type UploadResponse struct {
	ID      string `json:"id" example:"87d7f7573b0263fc9faf9ed65cb62841"`
	URL     string `json:"url" example:"http://localhost:8080/v1/chunks/87d7f7573b0263fc9faf9ed65cb62841"`
	Size    int64  `json:"size" example:"9728016"`
	SHA256  string `json:"sha256" example:"e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"`
	Expires int64  `json:"expires" example:"1755500000"`
}

// handleUpload stores one encrypted chunk.
//
// @Summary     Store a chunk
// @Description Stores one AES-256-CBC ciphertext blob and returns the absolute URL peers should fetch it from. The URL is the only field a client may use — never rebuild it from the id, since a backend may serve blobs from another host. Content-Length is required; chunked uploads are refused. The server is given no file hash, no part index and no key.
// @Tags        chunks
// @Accept      octet-stream
// @Produce     json
// @Param       Authorization header string false "Bearer <apiKey>. Optional only when the server has open_upload on."
// @Param       X-Chunk-TTL   header int    false "Requested lifetime in seconds, clamped to maxTtl"
// @Param       chunk         body   string true  "Raw ciphertext"
// @Success     201 {object} UploadResponse
// @Header      201 {string} Location "The same absolute URL as the url field"
// @Failure     400 {object} ErrorResponse "empty body, or a body that ended short of its Content-Length"
// @Failure     401 {object} ErrorResponse "bad key, or a missing one on a server that requires it"
// @Failure     411 {object} ErrorResponse "no Content-Length"
// @Failure     413 {object} ErrorResponse "over maxChunkSize"
// @Failure     429 {object} ErrorResponse "daily upload quota exhausted"
// @Failure     503 {object} ErrorResponse "server not installed"
// @Failure     507 {object} ErrorResponse "storage failure, or free space below min_free_bytes"
// @Router      /v1/chunks [post]
func (s *Server) handleUpload(c *gin.Context) {
	if !s.requireInstalled(c) {
		return
	}

	st := s.now()

	keyID, ok := s.uploaderKeyID(c, st.cfg)
	if !ok {
		return
	}

	declared, ok := declaredLength(c)
	if !ok {
		writeError(c, http.StatusLengthRequired, "Content-Length required")
		return
	}

	if declared > st.cfg.Storage.MaxChunkSize {
		writeError(c, http.StatusRequestEntityTooLarge, "chunk exceeds maxChunkSize")
		return
	}

	// Refuse before the volume fills rather than after, measured against the
	// declared length so the floor holds for the largest body this request
	// could deliver. Checked before the quota so a refusal costs the key
	// nothing.
	if !st.store.HasRoomFor(declared) {
		c.Writer.Header().Set("Retry-After", strconv.Itoa(storageFullRetryAfter))
		writeError(c, http.StatusInsufficientStorage, "insufficient storage")
		return
	}

	// Reserved before the body is read so a flood of concurrent uploads cannot
	// collectively overshoot the daily allowance.
	if !st.quota.Consume(keyID, declared) {
		// The allowance is per UTC day, so this is the honest answer rather
		// than a guess, and it always lands inside the 1..86400 delta-seconds
		// window the client accepts.
		c.Writer.Header().Set("Retry-After", strconv.Itoa(storage.SecondsUntilReset(time.Now())))
		writeError(c, http.StatusTooManyRequests, "daily upload quota exhausted")
		return
	}

	ttl := st.cfg.ClampTTL(requestedTTL(c))

	meta, err := st.store.Ingest(c.Request.Body, keyID, ttl, declared)
	if err != nil {
		st.quota.Refund(keyID, declared)
		writeError(c, storage.HTTPStatus(err), err.Error())
		return
	}

	url := s.publicBaseURL(c, st.cfg) + "/v1/chunks/" + meta.ID

	c.Writer.Header().Set("Location", url)
	writeJSON(c, http.StatusCreated, UploadResponse{
		ID:      meta.ID,
		URL:     url,
		Size:    meta.Size,
		SHA256:  meta.SHA256,
		Expires: meta.ExpiresAt,
	})
}

// -- internals ---------------------------------------------------------------

// uploaderKeyID decides who to charge and record this upload against, or
// answers 401 and reports false.
//
// A credential that was offered and rejected is a 401 even on an open server:
// downgrading a mistyped key to anonymous would hand the client a chunk it can
// never delete, and hide the typo for good.
func (s *Server) uploaderKeyID(c *gin.Context, cfg *config.Config) (string, bool) {
	if keyID, ok := security.Identify(cfg, c.Request); ok {
		return keyID, true
	}

	if cfg.Upload.OpenUpload && !security.HasCredential(c.Request) {
		return config.AnonymousKeyID, true
	}

	writeUnauthorized(c)

	return "", false
}

// declaredLength reads the request's Content-Length.
//
// Go reports -1 for a chunked request and 0 for both an absent header and an
// explicit "Content-Length: 0", so the raw header has to be consulted to tell
// the last two apart: absent is a 411, zero is a 400 once the empty body is
// read.
func declaredLength(c *gin.Context) (int64, bool) {
	if c.GetHeader("Content-Length") == "" || c.Request.ContentLength < 0 {
		return 0, false
	}

	return c.Request.ContentLength, true
}

// requestedTTL reads X-Chunk-TTL. Anything that is not a plain count of seconds
// means "no preference", which ClampTTL turns into the configured default.
func requestedTTL(c *gin.Context) time.Duration {
	raw := strings.TrimSpace(c.GetHeader("X-Chunk-TTL"))
	if raw == "" {
		return 0
	}

	seconds, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seconds <= 0 {
		return 0
	}

	return time.Duration(seconds) * time.Second
}

// publicBaseURL is the absolute base clients should fetch chunks from.
//
// Derived from the request unless the config pins it — which it must behind a
// reverse proxy that rewrites Host, or peers are handed URLs pointing at the
// wrong place.
//
// A derived URL is always http, even on a TLS listener. eMuleQt downloads
// chunks over a hand-built socket with no TLS at all, dialling port 80, so an
// https URL it cannot use is worse than an http one it can. An operator who
// genuinely terminates TLS in front pins the value instead, and startup warns
// about it.
func (s *Server) publicBaseURL(c *gin.Context, cfg *config.Config) string {
	if cfg.Server.PublicBaseURL != "" {
		return cfg.Server.PublicBaseURL + cfg.Server.BasePath
	}

	host := c.Request.Host
	if host == "" {
		host = "localhost"
	}

	return "http://" + host + cfg.Server.BasePath
}
