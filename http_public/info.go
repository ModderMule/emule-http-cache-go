package http_public

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// serviceName is the handshake token. A client that does not see exactly this
// must refuse to use the endpoint, which is what stops a configuration link
// pointing a client at an arbitrary host.
const serviceName = "emule-http-cache"

// protocolVersion is the contract version this server speaks.
const protocolVersion = 1

// InfoResponse is the body of GET /v1/info.
type InfoResponse struct {
	Service        string `json:"service" example:"emule-http-cache"`
	Version        int    `json:"version" example:"1"`
	Implementation string `json:"implementation" example:"go"`
	MaxChunkSize   int64  `json:"maxChunkSize" example:"10485760"`
	DefaultTTL     int64  `json:"defaultTtl" example:"172800"`
	MaxTTL         int64  `json:"maxTtl" example:"604800"`
	RangeSupported bool   `json:"rangeSupported" example:"true"`

	// UploadRequiresAuth is false on a server that takes uploads without a
	// credential. It is never a reason for a client to drop the key it has — a
	// key is still what authorises DELETE, and the operator may close the
	// server again tomorrow.
	UploadRequiresAuth bool `json:"uploadRequiresAuth" example:"true"`
}

// handleInfo reports the limits a client needs before it spends an upload.
//
// @Summary     Server limits and handshake
// @Description Unauthenticated. A client must see "service":"emule-http-cache" and a version it understands before using the endpoint; this is what stops a configuration link pointing it at an arbitrary host. The probe deliberately carries no credential.
// @Tags        ops
// @Produce     json
// @Success     200 {object} InfoResponse
// @Failure     503 {object} ErrorResponse "server not installed"
// @Router      /v1/info [get]
func (s *Server) handleInfo(c *gin.Context) {
	if !s.requireInstalled(c) {
		return
	}

	cfg := s.now().cfg

	writeJSON(c, http.StatusOK, InfoResponse{
		Service:        serviceName,
		Version:        protocolVersion,
		Implementation: "go",
		MaxChunkSize:   cfg.Storage.MaxChunkSize,
		DefaultTTL:     int64(cfg.Storage.DefaultTTL.Seconds()),
		MaxTTL:         int64(cfg.Storage.MaxTTL.Seconds()),
		RangeSupported: true,

		// Saves a client discovering the answer by eating a 401.
		UploadRequiresAuth: !cfg.Upload.OpenUpload,
	})
}
