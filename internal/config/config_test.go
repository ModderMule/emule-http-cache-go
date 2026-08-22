package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// write stages a config file and parses it through a private viper instance, so
// the cases never leak into each other through the global one.
func write(t *testing.T, body string) (*Config, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging a config: %v", err)
	}

	return ParseFile(path)
}

// TestFallbacks pins every defaulting rule the PHP reference server has, each
// of which has a test behind it there too. A config written before a key
// existed must still load sanely.
func TestFallbacks(t *testing.T) {
	const minimal = "api_keys:\n  k:\n    secret: s\n"
	t.Logf("input:  %q", minimal)

	cfg, err := write(t, minimal)
	if err != nil {
		t.Fatalf("a minimal config must load: %v", err)
	}
	t.Logf("output: %+v", cfg.Storage)

	checks := []struct {
		label string
		got   any
		want  any
	}{
		{"an absent min_free_bytes means no floor", cfg.Storage.MinFreeBytes, int64(0)},
		{"an absent default_ttl is 48 hours", cfg.Storage.DefaultTTL, 48 * time.Hour},
		{"an absent max_ttl is one week", cfg.Storage.MaxTTL, 7 * 24 * time.Hour},
		{"an absent max_chunk_size is 10 MiB", cfg.Storage.MaxChunkSize, int64(10 * 1024 * 1024)},
		{"an absent gc.interval is hourly", cfg.GC.Interval, time.Hour},
		{"an absent gc.max_deletes is 200", cfg.GC.MaxDeletes, 200},
		{"an absent open_upload is closed", cfg.Upload.OpenUpload, false},
		{"an absent fsync is on", cfg.Storage.Fsync, true},
		{"an absent addr is :8080", cfg.Server.Addr, ":8080"},
	}

	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s: got %v, want %v", c.label, c.got, c.want)
		}
	}
}

func TestClampsAndTrims(t *testing.T) {
	cfg, err := write(t, `
storage:
  min_free_bytes: -5
server:
  public_base_url: "http://cache.example.com/emule/"
upload:
  open_upload_quota_bytes_per_day: -1
api_keys:
  k:
    secret: s
    quota_bytes_per_day: -7
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Logf("output: minFree=%d baseURL=%q anonQuota=%d keyQuota=%d",
		cfg.Storage.MinFreeBytes, cfg.Server.PublicBaseURL,
		cfg.Upload.OpenUploadQuotaBytesPerDay, cfg.QuotaFor("k"))

	if cfg.Storage.MinFreeBytes != 0 {
		t.Errorf("a negative floor must clamp to zero, got %d", cfg.Storage.MinFreeBytes)
	}
	if cfg.Server.PublicBaseURL != "http://cache.example.com/emule" {
		t.Errorf("the base URL keeps its trailing slash: %q", cfg.Server.PublicBaseURL)
	}
	if cfg.Upload.OpenUploadQuotaBytesPerDay != 0 || cfg.QuotaFor("k") != 0 {
		t.Errorf("negative quotas must clamp to zero")
	}
}

// A zero gc.interval means "disable the sweeper", so it must be told apart from
// an absent one, which means hourly.
func TestExplicitZeroIntervalDisablesTheSweeper(t *testing.T) {
	cfg, err := write(t, "gc:\n  interval: 0s\napi_keys:\n  k:\n    secret: s\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	t.Logf("output: interval=%s", cfg.GC.Interval)

	if cfg.GC.Interval != 0 {
		t.Errorf("an explicit zero interval became %s; it must stay 0", cfg.GC.Interval)
	}
}

func TestKeyModel(t *testing.T) {
	cfg, err := write(t, `
api_keys:
  plain:
    secret: s
  off:
    secret: s2
    enabled: false
  nosecret:
    quota_bytes_per_day: 5
  anonymous:
    secret: sneaky
upload:
  open_upload: true
  open_upload_quota_bytes_per_day: 4096
`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var ids []string
	for _, k := range cfg.APIKeys {
		ids = append(ids, k.ID)
	}
	t.Logf("output: keys=%v", ids)

	// Sorted, so two keys sharing a secret always resolve to the same id rather
	// than a different one per request.
	if len(cfg.APIKeys) != 2 || cfg.APIKeys[0].ID != "off" || cfg.APIKeys[1].ID != "plain" {
		t.Fatalf("keys = %v, want [off plain] in that order", ids)
	}

	if !cfg.APIKeys[1].Enabled {
		t.Errorf("a key without an enabled flag must be enabled")
	}
	if cfg.APIKeys[0].Enabled {
		t.Errorf("enabled: false must survive the load")
	}

	// A key by this name could delete every chunk an open server accepted
	// without a credential, so it must never load.
	for _, k := range cfg.APIKeys {
		if k.ID == AnonymousKeyID {
			t.Errorf("a key named %q was loaded", AnonymousKeyID)
		}
	}

	if got := cfg.QuotaFor(AnonymousKeyID); got != 4096 {
		t.Errorf("quotaFor(anonymous) = %d, want the open allowance 4096", got)
	}
	if got := cfg.QuotaFor("nobody"); got != 0 {
		t.Errorf("quotaFor on an unknown key = %d, want 0 (unlimited)", got)
	}
}

func TestClampTTL(t *testing.T) {
	cfg, err := write(t, "storage:\n  default_ttl: 48h\n  max_ttl: 168h\napi_keys:\n  k:\n    secret: s\n")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	cases := []struct {
		requested time.Duration
		want      time.Duration
	}{
		{0, 48 * time.Hour},
		{-time.Hour, 48 * time.Hour},
		{6 * time.Hour, 6 * time.Hour},
		{1000 * time.Hour, 168 * time.Hour},
	}

	for _, tc := range cases {
		got := cfg.ClampTTL(tc.requested)
		t.Logf("input: %s -> output: %s", tc.requested, got)

		if got != tc.want {
			t.Errorf("ClampTTL(%s) = %s, want %s", tc.requested, got, tc.want)
		}
	}
}

// A config copied over from the PHP server carries gcProbability. A migrating
// operator must never be blocked by a key we chose to stop honouring.
func TestPHPShapedConfigStillLoads(t *testing.T) {
	cfg, err := write(t, "gcProbability: 0.01\napi_keys:\n  k:\n    secret: s\n")
	if err != nil {
		t.Fatalf("a PHP-shaped config must still load: %v", err)
	}

	warnings := cfg.Warnings()
	t.Logf("output: %d warning(s): %v", len(warnings), warnings)

	found := false
	for _, w := range warnings {
		if len(w) > 14 && w[:14] == "gcProbability " {
			found = true
		}
	}
	if !found {
		t.Errorf("loading a config with gcProbability must warn that it is ignored")
	}
}

func TestValidationRefusals(t *testing.T) {
	cases := map[string]string{
		"an unknown gin mode":         "server:\n  mode: sideways\n",
		"a base path with no slash":   "server:\n  base_path: cache\n",
		"a base path with a wildcard": "server:\n  base_path: \"/{id}\"\n",
		"a relative public base URL":  "server:\n  public_base_url: \"/cache\"\n",
		"a ceiling below the default": "storage:\n  default_ttl: 72h\n  max_ttl: 12h\n",
	}

	for label, body := range cases {
		t.Run(label, func(t *testing.T) {
			t.Logf("input:  %q", body)

			cfg, err := write(t, body)
			t.Logf("output: cfg=%v err=%v", cfg, err)

			if err == nil {
				t.Errorf("must be refused rather than started with")
			}
		})
	}
}

// An https base URL loads, but has to say something: eMuleQt fetches chunks
// over a plain socket with no TLS, so every peer would fail.
func TestHTTPSBaseURLWarns(t *testing.T) {
	cfg, err := write(t, "server:\n  public_base_url: \"https://cache.example.com\"\napi_keys:\n  k:\n    secret: s\n")
	if err != nil {
		t.Fatalf("an https base URL must load, not fail: %v", err)
	}

	warnings := cfg.Warnings()
	t.Logf("output: %v", warnings)

	found := false
	for _, w := range warnings {
		if len(w) > 20 && w[:21] == "server.public_base_ur" {
			found = true
		}
	}
	if !found {
		t.Errorf("an https base URL must warn that peers cannot fetch over it")
	}
}
