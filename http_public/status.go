package http_public

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// statusData is what the status page renders.
type statusData struct {
	BaseURL    string
	OpenUpload bool
}

// notInstalledData is what every page shows before the server has a config.
type notInstalledData struct {
	BaseURL string
}

// handleStatus renders the human-facing status page.
//
// @Summary     Server status page
// @Description An HTML summary of the endpoints this server exposes. Not part of the machine contract — a non-Go backend reimplements /v1/* and nothing more.
// @Tags        ops
// @Produce     html
// @Success     200 {string} html
// @Failure     503 {string} html "server not installed"
// @Router      / [get]
func (s *Server) handleStatus(c *gin.Context) {
	st := s.now()
	base := s.pageBaseURL(c)

	if !st.installed {
		// A machine client gets the uniform error shape rather than a page.
		s.renderPage(c, http.StatusServiceUnavailable, "not-installed", "Not installed",
			notInstalledData{BaseURL: base}, false)
		return
	}

	s.renderPage(c, http.StatusOK, "status", "Status", statusData{
		BaseURL:    base,
		OpenUpload: st.cfg.Upload.OpenUpload,
	}, false)
}

// -- internals ---------------------------------------------------------------

// pageBaseURL is the absolute base the HTML pages link to.
//
// Unlike the chunk URL in a 201, this one may be https: it is followed by a
// browser, not by eMuleQt's TLS-less chunk socket, so honouring the scheme the
// operator actually reached the page over is the friendlier answer.
func (s *Server) pageBaseURL(c *gin.Context) string {
	if pinned := s.now().cfg.Server.PublicBaseURL; pinned != "" {
		return pinned + s.timeouts.BasePath
	}

	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}

	host := c.Request.Host
	if host == "" {
		host = "localhost"
	}

	return scheme + "://" + host + s.timeouts.BasePath
}
