package http_public

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/gin-gonic/gin"
)

// embeddedTemplates is the compiled-in page set, so the binary renders its own
// install page with no sibling files.
//
//go:embed static/tpl/*.gohtml
var embeddedTemplates embed.FS

// templateDir is where the pages live, relative to a configured static root.
const templateDir = "static/tpl"

// layoutTemplate is the shell every page shares: doctype, stylesheet, heading.
// Each body template is parsed onto its own clone of it, so all seven can
// define "body" without colliding.
const layoutTemplate = "page.gohtml"

// pageSet is the parsed pages, keyed by file name without its extension.
type pageSet struct {
	pages map[string]*template.Template
}

// pageData is what every layout render receives. Title goes in the shell; Data
// is handed to the body template as its dot.
type pageData struct {
	Title string
	Data  any
}

// loadPages parses the page set, from disk when a static root is configured and
// from the embedded copy otherwise.
func loadPages(staticFilePath string) (*pageSet, error) {
	var source fs.FS = embeddedTemplates
	dir := templateDir

	if staticFilePath != "" {
		source = os.DirFS(staticFilePath)
		dir = "tpl"
	}

	layout, err := template.ParseFS(source, filepath.Join(dir, layoutTemplate))
	if err != nil {
		return nil, fmt.Errorf("parsing the page layout: %w", err)
	}

	names, err := fs.Glob(source, filepath.Join(dir, "*.gohtml"))
	if err != nil {
		return nil, fmt.Errorf("listing page templates: %w", err)
	}

	set := &pageSet{pages: map[string]*template.Template{}}
	for _, name := range names {
		base := filepath.Base(name)
		if base == layoutTemplate {
			continue
		}

		clone, err := layout.Clone()
		if err != nil {
			return nil, fmt.Errorf("cloning the layout for %s: %w", base, err)
		}
		if _, err := clone.ParseFS(source, name); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", base, err)
		}

		set.pages[base[:len(base)-len(".gohtml")]] = clone
	}

	if len(set.pages) == 0 {
		return nil, fmt.Errorf("no page templates found under %s", dir)
	}

	return set, nil
}

// -- internals ---------------------------------------------------------------

// renderPage sends one HTML page.
//
// Rendered into a buffer first, so a template failure becomes a clean 500
// rather than a half-sent page under a 200 — which is exactly what the PHP
// server, echoing as it goes, cannot do.
//
// sensitive marks a page with a credential on it: keep it out of caches, out of
// indexes, and out of the next site's Referer header.
func (s *Server) renderPage(c *gin.Context, status int, name, title string, data any, sensitive bool) {
	header := c.Writer.Header()
	header.Set("Content-Type", "text/html; charset=utf-8")
	header.Set("Cache-Control", "no-store")

	if sensitive {
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("X-Robots-Tag", "noindex, nofollow")
	}

	// A HEAD must never render, and must never claim the key.
	if isHead(c) {
		c.Status(status)
		return
	}

	tpl, ok := s.pages.pages[name]
	if !ok {
		s.logger.Errorf("no page template named %q", name)
		writeError(c, http.StatusInternalServerError, "internal error")
		return
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, pageData{Title: title, Data: data}); err != nil {
		s.logger.Errorf("rendering %s: %v", name, err)
		writeError(c, http.StatusInternalServerError, "internal error")
		return
	}

	header.Set("Content-Length", strconv.Itoa(buf.Len()))
	c.Status(status)
	_, _ = c.Writer.Write(buf.Bytes())
}
