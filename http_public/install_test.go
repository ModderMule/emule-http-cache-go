package http_public

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/install"
	"github.com/ModderMule/emule-http-cache-go/log"
	"github.com/ModderMule/emule-http-cache-go/pkg/ed2k"
	"github.com/ModderMule/emule-http-cache-go/pkg/storage"
)

var (
	copyPattern   = regexp.MustCompile(`<code id="ed2kLink">([^<]*)</code>`)
	secretPattern = regexp.MustCompile(`<span class="key">([0-9a-f]{48})</span>`)
)

// newInstallServer starts a server with no config, so /install is the whole
// user interface until the form is submitted.
func newInstallServer(t *testing.T) *httptest.Server {
	t.Helper()

	cfg, dir := testConfig(t)

	sample, err := os.ReadFile(filepath.Join("..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("reading config.example.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), sample, 0o644); err != nil {
		t.Fatalf("staging the sample: %v", err)
	}

	store := storage.NewStore(cfg)
	quota := storage.NewQuota(cfg)
	installer := install.New(dir, filepath.Join(dir, "config.yaml"), cfg.Storage.VarDir)

	srv, err := New(Deps{
		Config: cfg, Store: store, Quota: quota,
		GC: storage.NewGc(cfg, store, quota), Installer: installer,
		Logger: log.NewNop(), Installed: false,

		Reload: func() (*config.Config, *storage.Store, *storage.Quota, error) {
			fresh, err := config.ParseFile(filepath.Join(dir, "config.yaml"))
			if err != nil {
				return nil, nil, nil, err
			}
			fresh.Storage.DataDir = cfg.Storage.DataDir
			fresh.Storage.VarDir = cfg.Storage.VarDir

			return fresh, storage.NewStore(fresh), storage.NewQuota(fresh), nil
		},
	})
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts
}

func submitInstallForm(t *testing.T, ts *httptest.Server) string {
	t.Helper()

	form := url.Values{
		"keyId":             {"default"},
		"openUploadQuotaGb": {"10"},
		"quotaGb":           {"0"},
		"minFreeGb":         {"1"},
		"defaultTtlHours":   {"48"},
		"maxTtlHours":       {"168"},
		"publicBaseUrl":     {""},
	}

	resp, err := ts.Client().PostForm(ts.URL+"/install", form)
	if err != nil {
		t.Fatalf("submitting the form: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /install = %d, want 200. body:\n%s", resp.StatusCode, body)
	}

	return string(body)
}

// TestInstalledPageOffersTheLinkForCopying covers the one handover the page
// has, and the bug that only shows up in a real client.
//
// Copying is it: eMuleQt's clipboard watcher matches on the ed2k://|httpcache|
// prefix and offers to apply what it finds, so the copied text has to be the
// canonical link, separators and all. A Chromium since version 130 cannot parse
// an ed2k:// URL at all — "|" is a forbidden host code point — so an anchor
// there takes the tab to about:blank#blocked and hands over nothing.
//
// The separator check is the part worth keeping honest. html/template's URL
// normalizer percent-encodes "|" to %7C, and template.URL does not stop it: it
// only gets the ed2k scheme past the filter that would otherwise rewrite an
// href to #ZgotmplZ. An escaped separator silently breaks the link, because a
// conforming parser splits on literal "|" *before* decoding and so sees one
// malformed field. The page would look perfectly fine while handing out a link
// nothing can use — which is why the last assertion here refuses an href
// outright rather than trusting one to survive.
func TestInstalledPageOffersTheLinkForCopying(t *testing.T) {
	ts := newInstallServer(t)
	page := submitInstallForm(t, ts)

	if !strings.Contains(page, `id="copyLink"`) {
		t.Errorf("the installed page has no copy control, so it has no handover at all")
	}

	m := copyPattern.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no copyable ed2k link on the installed page:\n%s", page)
	}
	copied := m[1]
	t.Logf("output: copyable=%s", ed2k.Redact(copied))

	if !strings.HasPrefix(copied, "ed2k://|httpcache|") {
		t.Errorf("the clipboard watcher matches on the ed2k://|httpcache| prefix, which this lacks: %s", ed2k.Redact(copied))
	}
	if strings.Contains(strings.ToLower(copied), "%7c") {
		t.Errorf("the link has percent-encoded separators; a parser splitting on | sees one field: %s", ed2k.Redact(copied))
	}
	if got := strings.Count(copied, "|"); got != 6 {
		t.Errorf("the link has %d literal separators, want 6: %s", got, ed2k.Redact(copied))
	}

	link, ok := ed2k.Parse(copied)
	t.Logf("parsed: ok=%t keyId=%s baseURL=%s", ok, link.KeyID, link.BaseURL)

	if !ok {
		t.Fatalf("the link the install page prints does not parse")
	}
	if link.KeyID != "default" {
		t.Errorf("keyId = %q, want default", link.KeyID)
	}
	if link.String() != copied {
		t.Errorf("the printed link does not round-trip")
	}

	// An anchor is inert in Chromium and re-introduces the %7C bug above, so
	// the page must not grow one back.
	if strings.Contains(page, `href="ed2k`) {
		t.Errorf("the installed page has an ed2k:// href; Chromium cannot follow one and html/template escapes its separators")
	}
}

// The key is shown exactly once. That promise is the reason the disclosure is
// recorded before the secret reaches the page.
func TestKeyIsShownOnlyOnce(t *testing.T) {
	ts := newInstallServer(t)
	page := submitInstallForm(t, ts)

	m := secretPattern.FindStringSubmatch(page)
	if m == nil {
		t.Fatalf("no secret on the installed page")
	}
	secret := m[1]
	t.Logf("output: secret shown, %d characters", len(secret))

	resp, err := ts.Client().Get(ts.URL + "/install")
	if err != nil {
		t.Fatalf("reloading /install: %v", err)
	}
	defer resp.Body.Close()

	again, _ := io.ReadAll(resp.Body)
	t.Logf("reload: %d, secret present = %t", resp.StatusCode, strings.Contains(string(again), secret))

	if strings.Contains(string(again), secret) {
		t.Errorf("reloading /install showed the key a second time")
	}
	if !strings.Contains(string(again), "was shown once") {
		t.Errorf("the reloaded page does not say the key was already shown")
	}
}

// A server with no config answers 503 on every machine route, and starts
// serving them the moment the form is submitted — without a restart.
func TestInstallActivatesTheAPI(t *testing.T) {
	ts := newInstallServer(t)

	before, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	body, _ := io.ReadAll(before.Body)
	_ = before.Body.Close()
	t.Logf("before install: %d %s", before.StatusCode, strings.TrimSpace(string(body)))

	if before.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("GET /v1/info before install = %d, want 503", before.StatusCode)
	}
	if !strings.Contains(string(body), `"error"`) {
		t.Errorf("a machine route must answer with the uniform error shape, got: %s", body)
	}

	page := submitInstallForm(t, ts)
	secret := secretPattern.FindStringSubmatch(page)[1]

	after, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer after.Body.Close()
	t.Logf("after install:  %d", after.StatusCode)

	if after.StatusCode != http.StatusOK {
		t.Errorf("GET /v1/info after install = %d, want 200 without a restart", after.StatusCode)
	}

	// And the key that was just shown actually works.
	req, _ := http.NewRequest("POST", ts.URL+"/v1/chunks", strings.NewReader("ciphertext"))
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", "application/octet-stream")

	upload, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("POST /v1/chunks: %v", err)
	}
	defer upload.Body.Close()
	t.Logf("upload with the shown key: %d", upload.StatusCode)

	if upload.StatusCode != http.StatusCreated {
		t.Errorf("POST /v1/chunks with the freshly shown key = %d, want 201", upload.StatusCode)
	}
}

// A form that does not validate must re-render with what was typed and write
// nothing at all.
func TestBadFormWritesNothing(t *testing.T) {
	ts := newInstallServer(t)

	resp, err := ts.Client().PostForm(ts.URL+"/install", url.Values{
		"keyId":           {"anonymous"},
		"defaultTtlHours": {"0"},
	})
	if err != nil {
		t.Fatalf("submitting: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	t.Logf("output: %d, mentions the reserved id = %t", resp.StatusCode, strings.Contains(string(body), "reserved"))

	if !strings.Contains(string(body), "Nothing has been written") {
		t.Errorf("a refused form must say nothing was written")
	}

	info, err := ts.Client().Get(ts.URL + "/v1/info")
	if err != nil {
		t.Fatalf("GET /v1/info: %v", err)
	}
	defer info.Body.Close()

	if info.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("a refused form installed something: /v1/info = %d, want 503", info.StatusCode)
	}
}
