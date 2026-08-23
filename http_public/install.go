package http_public

import (
	"html"
	"html/template"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/ModderMule/emule-http-cache-go/internal/install"
	"github.com/ModderMule/emule-http-cache-go/pkg/ed2k"
)

// installFormData renders the settings form.
type installFormData struct {
	Action          string
	Fields          []install.FormField
	Values          map[string]string
	Errors          map[string]string
	DetectedBaseURL string
}

// installedData renders the one page that shows the key.
type installedData struct {
	BaseURL       string
	KeyID         string
	Secret        string
	OpenUpload    bool
	ClaimRecorded bool

	// Ed2kHref is the whole href attribute, not just its value.
	//
	// template.URL is not enough here. It gets the link past html/template's
	// scheme filter, which allows only http, https, mailto and relative
	// references and would otherwise rewrite an ed2k:// href to #ZgotmplZ — but
	// the URL *normalizer* still runs afterwards and percent-encodes "|" to
	// %7C. That silently breaks the link: a conforming parser splits on literal
	// "|" before decoding anything, so a link whose separators are escaped
	// reads as a single malformed field and is refused outright.
	//
	// Handing over the finished attribute skips normalisation. The value is
	// built by pkg/ed2k, which percent-encodes every field, and the
	// remaining HTML metacharacters are escaped below.
	Ed2kHref     template.HTMLAttr
	Ed2kLinkText string
}

// alreadyInstalledData renders the page shown once the key has been disclosed.
type alreadyInstalledData struct {
	BaseURL    string
	KeyID      string
	ShownAt    string
	OpenUpload bool
	ConfigPath string
}

// installFailedData renders a refusal, with the commands that would fix it.
type installFailedData struct {
	BaseURL string
	Message string
	Hints   []string
}

// handleInstall is every state /install can be in.
//
//	no config file     GET   the settings form; nothing is written
//	                   POST  write the config, then show the key once
//	marker unclaimed   show the key once — the install ran but the page did not
//	marker claimed     say so, and never show the key again
//	no marker          a hand-written config: this page will not read it back
//
// @Summary     Setup page
// @Description This implementation's own setup page. It writes the config file, then shows the generated API key exactly once alongside a clickable ed2k:// link that configures eMuleQt in one step. Not part of the machine contract.
// @Tags        install
// @Accept      x-www-form-urlencoded
// @Produce     html
// @Success     200 {string} html
// @Failure     503 {string} html "the install was refused; nothing was written"
// @Router      /install [get]
func (s *Server) handleInstall(c *gin.Context) {
	base := s.pageBaseURL(c)

	if !s.installer.IsInstalled() {
		if c.Request.Method == http.MethodPost {
			s.runInstall(c, base)
			return
		}

		s.renderPage(c, http.StatusOK, "install-form", "Install", installFormData{
			Action:          base + "/install",
			Fields:          install.FormFields,
			Values:          install.FormDefaults(),
			Errors:          map[string]string{},
			DetectedBaseURL: base,
		}, false)

		return
	}

	state, ok := s.installer.State()
	if !ok {
		s.renderPage(c, http.StatusOK, "configured-by-hand", "Already configured",
			notInstalledData{BaseURL: base}, false)
		return
	}

	if !state.IsClaimed() {
		s.disclose(c, base, state.KeyID)
		return
	}

	s.renderPage(c, http.StatusOK, "already-installed", "Already installed", alreadyInstalledData{
		BaseURL:    base,
		KeyID:      state.KeyID,
		ShownAt:    state.ClaimedAtTime().Format("2006-01-02 15:04:05"),
		OpenUpload: s.now().cfg.Upload.OpenUpload,
		ConfigPath: s.installer.ConfigPath(),
	}, false)
}

// -- internals ---------------------------------------------------------------

// runInstall validates the submission and writes the config.
func (s *Server) runInstall(c *gin.Context, base string) {
	submitted := map[string]string{}
	for name := range install.FormDefaults() {
		submitted[name] = strings.TrimSpace(c.PostForm(name))
	}

	settings, errs := install.FromForm(submitted)
	if settings == nil {
		// Re-render with what they actually typed, not with what it clamped to.
		s.renderPage(c, http.StatusOK, "install-form", "Install", installFormData{
			Action:          base + "/install",
			Fields:          install.FormFields,
			Values:          submitted,
			Errors:          errs,
			DetectedBaseURL: base,
		}, false)

		return
	}

	secret, _, err := s.installer.Install(settings)
	if err != nil {
		data := installFailedData{BaseURL: base, Message: err.Error()}
		if failure, ok := err.(*install.Error); ok {
			data.Hints = failure.Hints
		}

		s.renderPage(c, http.StatusServiceUnavailable, "install-failed", "Install failed", data, false)

		return
	}

	s.activate()
	s.show(c, base, settings.KeyID, secret)
}

// disclose shows a key whose install ran but whose page never rendered.
func (s *Server) disclose(c *gin.Context, base, keyID string) {
	secret, ok := s.installer.SecretFor(keyID)
	if !ok {
		s.renderPage(c, http.StatusOK, "configured-by-hand", "Already configured",
			notInstalledData{BaseURL: base}, false)
		return
	}

	s.show(c, base, keyID, secret)
}

// show claims first and renders second.
//
// "Shown once" is the promise, so the disclosure has to be recorded before the
// secret can reach the page. A claim that could not be written is said out loud
// rather than papered over.
func (s *Server) show(c *gin.Context, base, keyID, secret string) {
	claimed := s.installer.Claim()

	link := ed2k.Link{
		Name:    ed2k.DefaultName,
		BaseURL: base,
		Secret:  secret,
		KeyID:   keyID,
	}.String()

	s.renderPage(c, http.StatusOK, "installed", "Installed", installedData{
		BaseURL:       base,
		KeyID:         keyID,
		Secret:        secret,
		OpenUpload:    s.now().cfg.Upload.OpenUpload,
		ClaimRecorded: claimed,
		// EscapeString handles &, <, >, ' and " — everything that could break
		// out of the attribute — and leaves "|" alone, which is the whole
		// point. The link text goes through the template's own escaping.
		Ed2kHref:     template.HTMLAttr(`href="` + html.EscapeString(link) + `"`),
		Ed2kLinkText: link,
	}, true)
}

// activate brings the freshly written config into service without a restart.
//
// The PHP server re-reads its config on every request and so needs nothing
// here; a daemon that loaded once at boot would otherwise answer 503 to every
// /v1 route until somebody restarted it, which is a poor end to a setup flow
// that just told the operator everything worked.
func (s *Server) activate() {
	if s.reload == nil {
		s.logger.Warnf("installed, but this process cannot reload its own config — restart to start serving /v1")
		return
	}

	cfg, store, quota, err := s.reload()
	if err != nil {
		s.logger.Errorf("installed, but the new config could not be loaded: %v", err)
		return
	}

	s.current.Store(&state{cfg: cfg, store: store, quota: quota, installed: true})
	s.logger.Infof("configuration installed; /v1 routes are now live")
}
