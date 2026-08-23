package http_public

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// embeddedTemplates is the compiled-in page set, so the binary renders its own
// install page with no sibling files.
//
//go:embed static/tpl/*.gohtml
var embeddedTemplates embed.FS

// templateDir is where the pages live inside the embedded copy.
const templateDir = "static/tpl"

// diskTemplateDir is where they live under a configured static root.
const diskTemplateDir = "tpl"

// layoutTemplate is the shell every page shares: doctype, stylesheet, heading.
// Each body template is parsed onto its own clone of it, so all seven can
// define "body" without colliding.
const layoutTemplate = "page.gohtml"

// bodyBlock is what a page template must define for the layout to render it.
// Checked at load, because a page missing it parses cleanly and only fails when
// someone asks for that page.
const bodyBlock = "body"

// pageSet is the parsed pages, keyed by file name without its extension.
type pageSet struct {
	pages map[string]*template.Template

	// overridden names the pages that came from the configured directory
	// rather than the embedded copy, so startup can report what it picked up.
	overridden []string
}

// pageData is what every layout render receives. Title goes in the shell; Data
// is handed to the body template as its dot.
type pageData struct {
	Title string
	Data  any
}

// loadPages parses the page set, taking each page from the configured static
// root when it holds that file and from the embedded copy otherwise.
//
// The embedded set is the reference: it is compiled in, so it is complete by
// construction. That makes a configured directory an overlay rather than a
// replacement — it may hold one template or all eight — and it means a page
// can never go missing, only be overridden.
func loadPages(staticFilePath string) (*pageSet, error) {
	var disk fs.FS
	if staticFilePath != "" {
		disk = os.DirFS(staticFilePath)
	}

	names, err := fs.Glob(embeddedTemplates, path.Join(templateDir, "*.gohtml"))
	if err != nil {
		return nil, fmt.Errorf("listing page templates: %w", err)
	}

	set := &pageSet{pages: map[string]*template.Template{}}

	layoutFS, layoutPath, fromDisk := resolvePage(disk, layoutTemplate)
	layout, err := template.ParseFS(layoutFS, layoutPath)
	if err != nil {
		return nil, fmt.Errorf("parsing the page layout: %w", err)
	}
	if fromDisk {
		set.overridden = append(set.overridden, layoutTemplate)
	}

	for _, name := range names {
		base := path.Base(name)
		if base == layoutTemplate {
			continue
		}

		source, sourcePath, fromDisk := resolvePage(disk, base)

		clone, err := layout.Clone()
		if err != nil {
			return nil, fmt.Errorf("cloning the layout for %s: %w", base, err)
		}
		if _, err := clone.ParseFS(source, sourcePath); err != nil {
			return nil, fmt.Errorf("parsing %s: %w", sourcePath, err)
		}
		if clone.Lookup(bodyBlock) == nil {
			return nil, fmt.Errorf("%s defines no %q block, so it would render nothing", sourcePath, bodyBlock)
		}

		if fromDisk {
			set.overridden = append(set.overridden, base)
		}
		set.pages[strings.TrimSuffix(base, ".gohtml")] = clone
	}

	if len(set.pages) == 0 {
		return nil, fmt.Errorf("no page templates found under %s", templateDir)
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

// resolvePage picks the FS one page is parsed from: the configured directory
// when it holds the file, the embedded copy otherwise. The bool reports which,
// so the caller can log what an operator actually overrode.
func resolvePage(disk fs.FS, base string) (fs.FS, string, bool) {
	if disk != nil {
		name := path.Join(diskTemplateDir, base)
		if _, err := fs.Stat(disk, name); err == nil {
			return disk, name, true
		}
	}

	return embeddedTemplates, path.Join(templateDir, base), false
}
