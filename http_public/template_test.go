package http_public

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderedPages is every page name a renderPage call site asks for. The set is
// what loadPages has to guarantee: a page missing from here used to load fine
// and 500 on the one request that wanted it.
var renderedPages = []string{
	"status",
	"not-installed",
	"install-form",
	"installed",
	"install-failed",
	"already-installed",
	"configured-by-hand",
}

// TestLoadPagesEmbedded is the baseline: with no static root configured every
// page comes from the binary and nothing is reported as overridden.
func TestLoadPagesEmbedded(t *testing.T) {
	t.Logf("input:  staticFilePath %q", "")

	set, err := loadPages("")
	if err != nil {
		t.Fatalf("loadPages: %v", err)
	}
	t.Logf("output: %d pages, %d overridden", len(set.pages), len(set.overridden))

	for _, name := range renderedPages {
		tpl, ok := set.pages[name]
		if !ok {
			t.Errorf("page %q missing from the embedded set", name)
			continue
		}
		if tpl.Lookup(bodyBlock) == nil {
			t.Errorf("page %q defines no %q block", name, bodyBlock)
		}
	}

	if len(set.overridden) != 0 {
		t.Errorf("overridden = %v, want none", set.overridden)
	}
}

// TestLoadPagesPartialOverride is the case the change exists for: a directory
// holding one template overrides that page and leaves the rest embedded.
func TestLoadPagesPartialOverride(t *testing.T) {
	dir := stageTemplates(t, map[string]string{
		"status.gohtml": `{{ define "body" }}<p>OVERRIDDEN {{ .BaseURL }}</p>{{ end }}`,
	})
	t.Logf("input:  staticFilePath %q holding tpl/status.gohtml only", dir)

	set, err := loadPages(dir)
	if err != nil {
		t.Fatalf("loadPages: %v", err)
	}

	if want := []string{"status.gohtml"}; len(set.overridden) != 1 || set.overridden[0] != want[0] {
		t.Errorf("overridden = %v, want %v", set.overridden, want)
	}
	if len(set.pages) != len(renderedPages) {
		t.Errorf("loaded %d pages, want %d", len(set.pages), len(renderedPages))
	}

	overridden := renderOne(t, set, "status", statusData{BaseURL: "http://cache.test"})
	t.Logf("output: status page = %s", condense(overridden))
	if !strings.Contains(overridden, "OVERRIDDEN http://cache.test") {
		t.Errorf("status page did not come from disk: %s", overridden)
	}

	// The pages the directory does not hold must still be the embedded ones,
	// not a stub and not an error.
	embedded := renderOne(t, set, "not-installed", notInstalledData{BaseURL: "http://cache.test"})
	t.Logf("output: not-installed page = %s", condense(embedded))
	if !strings.Contains(embedded, "has not been configured yet") {
		t.Errorf("not-installed page did not fall back to the embedded copy: %s", embedded)
	}
}

// TestLoadPagesOverriddenLayout covers the shell, which every page shares: a
// disk page.gohtml has to wrap the bodies that stayed embedded.
func TestLoadPagesOverriddenLayout(t *testing.T) {
	dir := stageTemplates(t, map[string]string{
		"page.gohtml": `<h1>CUSTOM SHELL {{ .Title }}</h1>{{ template "body" .Data }}`,
	})
	t.Logf("input:  staticFilePath %q holding tpl/page.gohtml only", dir)

	set, err := loadPages(dir)
	if err != nil {
		t.Fatalf("loadPages: %v", err)
	}

	if want := []string{layoutTemplate}; len(set.overridden) != 1 || set.overridden[0] != want[0] {
		t.Errorf("overridden = %v, want %v", set.overridden, want)
	}

	out := renderOne(t, set, "not-installed", notInstalledData{BaseURL: "http://cache.test"})
	t.Logf("output: not-installed page = %s", condense(out))
	if !strings.Contains(out, "CUSTOM SHELL Not installed") {
		t.Errorf("layout did not come from disk: %s", out)
	}
	if !strings.Contains(out, "has not been configured yet") {
		t.Errorf("embedded body was not wrapped by the disk layout: %s", out)
	}
}

// TestLoadPagesRejectsBodylessOverride is the second half of the check: a page
// that parses but defines nothing renders an error at request time, so it has
// to be refused at startup instead.
func TestLoadPagesRejectsBodylessOverride(t *testing.T) {
	dir := stageTemplates(t, map[string]string{
		"status.gohtml": `<p>forgot the define block</p>`,
	})
	t.Logf("input:  staticFilePath %q holding a status.gohtml with no %q block", dir, bodyBlock)

	set, err := loadPages(dir)
	if err == nil {
		t.Fatalf("loadPages accepted a bodyless template, returned %d pages", len(set.pages))
	}
	t.Logf("output: error = %v", err)

	if !strings.Contains(err.Error(), "status.gohtml") {
		t.Errorf("error does not name the offending file: %v", err)
	}
}

// TestLoadPagesIgnoresUnknownDiskFiles keeps the embedded set authoritative: a
// stray template on disk is not a page, because nothing can ask for it.
func TestLoadPagesIgnoresUnknownDiskFiles(t *testing.T) {
	dir := stageTemplates(t, map[string]string{
		"not-a-page.gohtml": `{{ define "body" }}<p>stray</p>{{ end }}`,
	})
	t.Logf("input:  staticFilePath %q holding tpl/not-a-page.gohtml", dir)

	set, err := loadPages(dir)
	if err != nil {
		t.Fatalf("loadPages: %v", err)
	}
	t.Logf("output: %d pages, overridden %v", len(set.pages), set.overridden)

	if _, ok := set.pages["not-a-page"]; ok {
		t.Error("a disk template with no embedded counterpart became a page")
	}
	if len(set.overridden) != 0 {
		t.Errorf("overridden = %v, want none", set.overridden)
	}
}

// -- internals ---------------------------------------------------------------

// stageTemplates writes files into the tpl/ subdirectory of a fresh temp dir
// and returns the dir, which is what static_file_path points at.
func stageTemplates(t *testing.T, files map[string]string) string {
	t.Helper()

	root := t.TempDir()
	dir := filepath.Join(root, diskTemplateDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("staging %s: %v", dir, err)
	}

	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	return root
}

// renderOne executes one page of a set, the way the server does.
func renderOne(t *testing.T, set *pageSet, name string, data any) string {
	t.Helper()

	tpl, ok := set.pages[name]
	if !ok {
		t.Fatalf("no page named %q", name)
	}

	var buf bytes.Buffer
	if err := tpl.Execute(&buf, pageData{Title: pageTitles[name], Data: data}); err != nil {
		t.Fatalf("rendering %s: %v", name, err)
	}

	return buf.String()
}

// condense makes a rendered page readable in a test log: the layout inlines a
// 20-line stylesheet that says nothing about which template was used.
func condense(page string) string {
	if open := strings.Index(page, "<style>"); open >= 0 {
		if close := strings.Index(page, "</style>"); close > open {
			page = page[:open] + "<style>…</style>" + page[close+len("</style>"):]
		}
	}

	return strings.Join(strings.Fields(page), " ")
}

// pageTitles are the titles the handlers pass, so a rendered shell in a test
// matches what a request produces.
var pageTitles = map[string]string{
	"status":        "Status",
	"not-installed": "Not installed",
}
