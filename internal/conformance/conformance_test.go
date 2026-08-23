package conformance_test

import (
	"flag"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ModderMule/emule-http-cache-go/http_public"
	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/internal/conformance"
	"github.com/ModderMule/emule-http-cache-go/internal/install"
	"github.com/ModderMule/emule-http-cache-go/log"
	"github.com/ModderMule/emule-http-cache-go/pkg/storage"
)

var (
	baseURL = flag.String("base", "", "run against this backend instead of an in-process one")
	apiKey  = flag.String("key", "", "API key for -base")
)

// tReporter routes the suite's own trace into the test log, which is how all 31
// assertions satisfy the project's "log program input and output" rule without
// a single per-assertion logging call.
type tReporter struct{ t *testing.T }

func (r tReporter) Section(title string)      { r.t.Logf("== %s ==", title) }
func (r tReporter) Pass(label string)         { r.t.Logf("ok   %s", label) }
func (r tReporter) Fail(label, detail string) { r.t.Errorf("FAIL %s (%s)", label, detail) }
func (r tReporter) Skip(label, why string)    { r.t.Logf("skip %s (%s)", label, why) }
func (r tReporter) Logf(f string, a ...any)   { r.t.Logf(f, a...) }

// TestConformance runs the whole contract.
//
// With no -base it installs and starts a server in-process over a temp
// directory, so a plain `go test ./...` covers install, serve and the contract
// together. With -base it runs the identical assertions against any external
// backend — pointing it at the PHP reference server is the regression harness
// for the port.
func TestConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("the contract test moves two 9.28 MB parts")
	}

	base, key := *baseURL, *apiKey
	if base == "" {
		base, key = startInProcess(t)
	}

	t.Logf("input:  base=%s", base)

	result := (&conformance.Suite{BaseURL: base, APIKey: key}).Run(tReporter{t})

	t.Logf("output: %d passed, %d failed", result.Passed, result.Failed)

	if !result.OK() {
		t.Errorf("%d assertion(s) failed", result.Failed)
	}
	if result.Passed == 0 {
		t.Errorf("no assertions ran")
	}
}

// startInProcess installs a throwaway server and returns its base URL and key.
func startInProcess(t *testing.T) (string, string) {
	t.Helper()

	dir := t.TempDir()

	sample, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("cannot read config.example.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), sample, 0o644); err != nil {
		t.Fatalf("cannot stage the sample: %v", err)
	}

	configFile := filepath.Join(dir, "config.yaml")
	varDir := filepath.Join(dir, "var")

	installer := install.New(dir, configFile, varDir)
	settings, errs := install.FromForm(install.FormDefaults())
	if settings == nil {
		t.Fatalf("default form does not validate: %v", errs)
	}

	secret, _, err := installer.Install(settings)
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	cfg, err := config.ParseFile(configFile)
	if err != nil {
		t.Fatalf("loading the generated config: %v", err)
	}

	// Point the store at the temp tree rather than the sample's ./data, so the
	// suite never writes into the repository it is testing.
	cfg.Storage.DataDir = filepath.Join(dir, "storage")
	cfg.Storage.VarDir = varDir

	store := storage.NewStore(cfg)
	quota := storage.NewQuota(cfg)

	srv, err := http_public.New(http_public.Deps{
		Config:    cfg,
		Store:     store,
		Quota:     quota,
		GC:        storage.NewGc(cfg, store, quota),
		Installer: installer,
		Logger:    log.NewNop(),
		Installed: true,
	})
	if err != nil {
		t.Fatalf("building the server: %v", err)
	}

	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	return ts.URL, secret
}
