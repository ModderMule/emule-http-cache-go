package security

import (
	"net/http"
	"testing"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

func testConfig() *config.Config {
	return &config.Config{
		APIKeys: []config.APIKey{
			{ID: "live", Secret: "live-secret", Enabled: true},
			{ID: "revoked", Secret: "revoked-secret", Enabled: false},
		},
	}
}

func request(header, value string) *http.Request {
	r, _ := http.NewRequest("POST", "http://example.test/v1/chunks", nil)
	if header != "" {
		r.Header.Set(header, value)
	}

	return r
}

func TestIdentify(t *testing.T) {
	cfg := testConfig()

	cases := []struct {
		label   string
		header  string
		value   string
		wantID  string
		wantOK  bool
		wantHas bool
	}{
		{"a live key over Authorization", "Authorization", "Bearer live-secret", "live", true, true},
		{"the scheme is case-insensitive", "Authorization", "bearer live-secret", "live", true, true},
		{"surrounding whitespace is tolerated", "Authorization", "  Bearer   live-secret  ", "live", true, true},
		{"a live key over X-Api-Key", "X-Api-Key", "live-secret", "live", true, true},

		// A revoked key is not a credential at all: it loses DELETE too.
		{"a revoked key is rejected", "Authorization", "Bearer revoked-secret", "", false, true},

		{"a wrong secret is rejected", "Authorization", "Bearer nope", "", false, true},
		{"an absent credential", "", "", "", false, false},
		{"an empty bearer token", "Authorization", "Bearer ", "", false, false},
		{"a non-bearer scheme", "Authorization", "Basic dXNlcjpwYXNz", "", false, false},

		// A token may not contain whitespace: "Bearer a b" is malformed, not a
		// token of "a b" and not a token of "a".
		{"a token with whitespace in it", "Authorization", "Bearer live secret", "", false, false},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			r := request(tc.header, tc.value)
			t.Logf("input:  %s: %q", tc.header, tc.value)

			id, ok := Identify(cfg, r)
			has := HasCredential(r)
			t.Logf("output: id=%q ok=%t hasCredential=%t", id, ok, has)

			if id != tc.wantID || ok != tc.wantOK {
				t.Errorf("Identify = (%q, %t), want (%q, %t)", id, ok, tc.wantID, tc.wantOK)
			}
			if has != tc.wantHas {
				t.Errorf("HasCredential = %t, want %t", has, tc.wantHas)
			}
		})
	}
}

// Authorization wins over X-Api-Key when both are present, but a malformed
// Authorization falls through rather than swallowing the request.
func TestHeaderPrecedence(t *testing.T) {
	cfg := testConfig()

	t.Run("Authorization wins", func(t *testing.T) {
		r := request("Authorization", "Bearer live-secret")
		r.Header.Set("X-Api-Key", "nope")

		id, ok := Identify(cfg, r)
		t.Logf("output: id=%q ok=%t", id, ok)

		if id != "live" || !ok {
			t.Errorf("Identify = (%q, %t), want (live, true)", id, ok)
		}
	})

	t.Run("a malformed Authorization falls through to X-Api-Key", func(t *testing.T) {
		r := request("Authorization", "Basic whatever")
		r.Header.Set("X-Api-Key", "live-secret")

		id, ok := Identify(cfg, r)
		t.Logf("output: id=%q ok=%t", id, ok)

		if id != "live" || !ok {
			t.Errorf("Identify = (%q, %t), want (live, true)", id, ok)
		}
	})
}

// Two entries sharing a secret must resolve to the same id on every call. Go
// randomises map iteration, so this is what the ordered key slice buys.
func TestDuplicateSecretsResolveDeterministically(t *testing.T) {
	cfg := &config.Config{APIKeys: []config.APIKey{
		{ID: "aaa", Secret: "same", Enabled: true},
		{ID: "zzz", Secret: "same", Enabled: true},
	}}

	first, _ := Identify(cfg, request("Authorization", "Bearer same"))
	t.Logf("input:  two enabled keys sharing a secret; first resolution %q", first)

	for i := 0; i < 200; i++ {
		got, ok := Identify(cfg, request("Authorization", "Bearer same"))
		if !ok || got != first {
			t.Fatalf("resolution %d gave %q, want %q every time", i, got, first)
		}
	}

	t.Logf("output: stable across 200 calls")
}

func TestOwnsChunk(t *testing.T) {
	cases := []struct {
		owner, key string
		want       bool
	}{
		{"laptop", "laptop", true},
		{"laptop", "seedbox", false},
		{"laptop", "", false},
		{"anonymous", "anonymous", true},
	}

	for _, tc := range cases {
		got := OwnsChunk(tc.owner, tc.key)
		t.Logf("input: owner=%q key=%q -> output: %t", tc.owner, tc.key, got)

		if got != tc.want {
			t.Errorf("OwnsChunk(%q, %q) = %t, want %t", tc.owner, tc.key, got, tc.want)
		}
	}
}
