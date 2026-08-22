package install

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

// newInstaller builds an installer over a throwaway directory holding the real
// config.example.yaml.
//
// The *real* sample, not a copy written here: the generator works by
// substituting into that file, so if someone renames a setting or the sample
// key, this is the suite that says so instead of a server that silently
// installs with the shipped secret.
func newInstaller(t *testing.T) (*Installer, string) {
	t.Helper()

	dir := t.TempDir()

	source, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Fatalf("cannot read the real config.example.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.example.yaml"), source, 0o644); err != nil {
		t.Fatalf("cannot stage the sample: %v", err)
	}

	return New(dir, filepath.Join(dir, "config.yaml"), filepath.Join(dir, "var")), dir
}

func settings(t *testing.T, overrides map[string]string) *Settings {
	t.Helper()

	form := FormDefaults()
	for k, v := range overrides {
		form[k] = v
	}

	s, errs := FromForm(form)
	if s == nil {
		t.Fatalf("fixture does not validate: %v", errs)
	}

	return s
}

func TestFreshInstall(t *testing.T) {
	installer, dir := newInstaller(t)
	in := settings(t, nil)
	t.Logf("input:  %+v", in)

	secret, state, err := installer.Install(in)
	t.Logf("output: secret=%s state=%+v err=%v", secret, state, err)

	if err != nil {
		t.Fatalf("a fresh directory must install: %v", err)
	}
	if len(secret) != 48 {
		t.Errorf("generated key is %d characters, want 48", len(secret))
	}
	if state.IsClaimed() {
		t.Errorf("the key must start unclaimed")
	}

	text, err := os.ReadFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("config.yaml was not written: %v", err)
	}
	written := string(text)
	t.Logf("generated config is %d bytes", len(written))

	if strings.Contains(written, sampleSecret) {
		t.Errorf("the shipped secret survived into the generated config")
	}
	if !strings.Contains(written, "Written by the installer") {
		t.Errorf("the generated config does not say where it came from")
	}
	if !strings.Contains(written, "Largest single chunk the server accepts") {
		t.Errorf("the sample's comments did not come along with it")
	}
	if !strings.Contains(written, "\n  default:\n") {
		t.Errorf("the key id from the form is not the key id in the config")
	}

	cfg, err := config.ParseFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
	stored, ok := cfg.SecretFor("default")
	t.Logf("reloaded: keys=%d secretMatches=%t", len(cfg.APIKeys), stored == secret)

	if !ok || stored != secret {
		t.Errorf("the key shown to the operator is not the key that was stored")
	}
}

func TestSettingsSurvive(t *testing.T) {
	installer, dir := newInstaller(t)

	in := settings(t, map[string]string{
		"keyId":             "seedbox",
		"openUpload":        "1",
		"openUploadQuotaGb": "2.5",
		"quotaGb":           "6",
		"minFreeGb":         "0.5",
		"defaultTtlHours":   "12",
		"maxTtlHours":       "72",
		"publicBaseUrl":     "https://cache.example.com/emule/",
	})
	t.Logf("input:  %+v", in)

	if _, _, err := installer.Install(in); err != nil {
		t.Fatalf("a fully customised install must succeed: %v", err)
	}

	cfg, err := config.ParseFile(filepath.Join(dir, "config.yaml"))
	if err != nil {
		t.Fatalf("the generated config does not load: %v", err)
	}
	t.Logf("output: %+v", cfg.Storage)

	checks := []struct {
		field string
		got   any
		want  any
	}{
		{"key id", cfg.APIKeys[0].ID, "seedbox"},
		{"that key's daily quota", cfg.QuotaFor("seedbox"), int64(6 * gib)},
		{"the open-upload checkbox", cfg.Upload.OpenUpload, true},
		{"the anonymous allowance", cfg.Upload.OpenUploadQuotaBytesPerDay, int64(2.5 * gib)},
		{"the free-space floor", cfg.Storage.MinFreeBytes, int64(0.5 * gib)},
		{"the default TTL", cfg.Storage.DefaultTTL, 12 * time.Hour},
		{"the maximum TTL", cfg.Storage.MaxTTL, 72 * time.Hour},
		// Pinned with its trailing slash removed, which is what the chunk URL
		// is appended to.
		{"the pinned base URL", cfg.Server.PublicBaseURL, "https://cache.example.com/emule"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.field, c.got, c.want)
		}
	}
}

func TestMarkerAndClaim(t *testing.T) {
	installer, dir := newInstaller(t)

	secret, _, err := installer.Install(settings(t, nil))
	if err != nil {
		t.Fatalf("install: %v", err)
	}

	marker, err := os.ReadFile(filepath.Join(dir, "var", markerFile))
	if err != nil {
		t.Fatalf("the marker was not written: %v", err)
	}
	t.Logf("marker: %s", marker)

	if strings.Contains(string(marker), secret) {
		t.Errorf("the marker holds the secret; it must hold none")
	}

	state, ok := installer.State()
	t.Logf("before claim: %+v ok=%t", state, ok)
	if !ok || state.IsClaimed() {
		t.Fatalf("the key must start unclaimed")
	}

	if !installer.Claim() {
		t.Fatalf("claiming must succeed")
	}

	state, _ = installer.State()
	t.Logf("after claim:  %+v", state)
	if !state.IsClaimed() {
		t.Errorf("the claim did not reach disk")
	}

	got, ok := installer.SecretFor("default")
	t.Logf("SecretFor(default) matches=%t", got == secret)
	if !ok || got != secret {
		t.Errorf("SecretFor did not read the key back")
	}
}

// A config nobody generated has no marker, which is what stops /install reading
// a hand-written key back to whoever asks.
func TestHandWrittenConfigHasNoMarker(t *testing.T) {
	installer, dir := newInstaller(t)

	source, _ := os.ReadFile(filepath.Join(dir, "config.example.yaml"))
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), source, 0o600); err != nil {
		t.Fatalf("staging a hand-written config: %v", err)
	}

	state, ok := installer.State()
	t.Logf("output: state=%+v ok=%t", state, ok)

	if ok {
		t.Errorf("a hand-written config must have no marker")
	}
}

func TestRefusals(t *testing.T) {
	installer, dir := newInstaller(t)

	if _, _, err := installer.Install(settings(t, nil)); err != nil {
		t.Fatalf("first install: %v", err)
	}

	before, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))
	_, _, err := installer.Install(settings(t, nil))
	after, _ := os.ReadFile(filepath.Join(dir, "config.yaml"))

	t.Logf("output: err=%v unchanged=%t", err, string(before) == string(after))

	if err == nil {
		t.Errorf("installing over an existing config must be refused")
	}
	if string(before) != string(after) {
		t.Errorf("the refused install rewrote the config")
	}

	a, _ := newInstaller(t)
	b, _ := newInstaller(t)
	first, _, _ := a.Install(settings(t, nil))
	second, _, _ := b.Install(settings(t, nil))
	t.Logf("two installs: %s vs %s", first, second)

	if first == second {
		t.Errorf("two installs generated the same key")
	}
}

func TestFormValidation(t *testing.T) {
	refused := map[string]map[string]string{
		"a ceiling below the default TTL": {"defaultTtlHours": "72", "maxTtlHours": "12"},
		"the reserved key id":             {"keyId": "anonymous"},
		"a key id with a slash in it":     {"keyId": "my/key"},
		"an empty key id":                 {"keyId": ""},
		"a relative base URL":             {"publicBaseUrl": "/emule"},
		"a base URL with a query string":  {"publicBaseUrl": "https://h/?x=1"},
		"a quota that is not a number":    {"quotaGb": "lots"},
		"a negative quota":                {"minFreeGb": "-1"},
		"a zero-hour TTL":                 {"defaultTtlHours": "0"},
	}

	for label, overrides := range refused {
		t.Run(label, func(t *testing.T) {
			form := FormDefaults()
			for k, v := range overrides {
				form[k] = v
			}
			t.Logf("input:  %v", overrides)

			s, errs := FromForm(form)
			t.Logf("output: settings=%v errors=%v", s, errs)

			if s != nil || len(errs) == 0 {
				t.Errorf("must be refused so nothing is written")
			}
		})
	}

	t.Run("the untouched form is valid as it stands", func(t *testing.T) {
		s, errs := FromForm(FormDefaults())
		t.Logf("output: settings=%+v errors=%v", s, errs)

		if s == nil {
			t.Fatalf("the default form must validate: %v", errs)
		}
		if s.PublicBaseURL != "" {
			t.Errorf(`a blank base URL must mean "work it out per request", got %q`, s.PublicBaseURL)
		}
	})

	t.Run("a wholly bad form reports every field", func(t *testing.T) {
		_, errs := FromForm(map[string]string{
			"keyId":             "anonymous",
			"openUploadQuotaGb": "x",
			"quotaGb":           "y",
			"minFreeGb":         "z",
			"defaultTtlHours":   "0",
			"maxTtlHours":       "0",
			"publicBaseUrl":     "nope",
		})
		t.Logf("output: %d errors: %v", len(errs), errs)

		if len(errs) != 7 {
			t.Errorf("got %d errors, want 7 — one page has to fix everything", len(errs))
		}
	})
}

// The embedded template and the repo-root copy must not drift: the installer
// prefers the file on disk, so a stale embedded copy would only surface on a
// deploy that shipped the binary alone.
func TestEmbeddedTemplateMatchesRepoCopy(t *testing.T) {
	onDisk, err := os.ReadFile(filepath.Join("..", "..", "config.example.yaml"))
	if err != nil {
		t.Skipf("no repo-root config.example.yaml to compare against: %v", err)
	}

	t.Logf("input:  repo copy %d bytes, embedded %d bytes", len(onDisk), len(embeddedExample))

	if string(onDisk) != embeddedExample {
		t.Errorf("internal/install/assets/config.example.yaml has drifted from the repo-root copy")
	}
}
