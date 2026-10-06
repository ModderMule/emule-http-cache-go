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

// callerCase is one request as the server sees it: who is on the other end of
// the connection, and what forwarding headers came with it.
type callerCase struct {
	label   string
	remote  string
	headers map[string]string
}

func (tc callerCase) request() *http.Request {
	r, _ := http.NewRequest("GET", "http://example.test/v1/stats", nil)
	r.RemoteAddr = tc.remote
	for name, value := range tc.headers {
		r.Header.Set(name, value)
	}

	return r
}

func TestClientIP(t *testing.T) {
	cases := []struct {
		callerCase
		want string
	}{
		{callerCase{"a direct caller", "203.0.113.9:4711", nil}, "203.0.113.9"},
		{callerCase{"a direct IPv6 caller", "[2001:db8::7]:4711", nil}, "2001:db8::7"},
		{callerCase{"a local caller", "127.0.0.1:4711", nil}, "127.0.0.1"},

		// The sample nginx config: both headers, from a loopback peer.
		{callerCase{"nginx on this host, X-Real-IP", "127.0.0.1:4711",
			map[string]string{"X-Real-IP": "198.51.100.7", "X-Forwarded-For": "10.0.0.1, 198.51.100.7"}}, "198.51.100.7"},
		{callerCase{"nginx on this host, X-Forwarded-For only", "127.0.0.1:4711",
			map[string]string{"X-Forwarded-For": "198.51.100.7"}}, "198.51.100.7"},
		{callerCase{"nginx over IPv6 loopback", "[::1]:4711",
			map[string]string{"X-Real-IP": "198.51.100.7"}}, "198.51.100.7"},

		// Everything but the last hop was supplied by the client, so a caller
		// who prepends an address does not get to be that address.
		{callerCase{"a client-supplied first hop is not believed", "127.0.0.1:4711",
			map[string]string{"X-Forwarded-For": "192.0.2.66, 198.51.100.7"}}, "198.51.100.7"},

		{callerCase{"a remote peer's headers are ignored", "203.0.113.9:4711",
			map[string]string{"X-Real-IP": "192.0.2.66", "X-Forwarded-For": "192.0.2.66"}}, "203.0.113.9"},
		{callerCase{"a malformed header falls back to the peer", "127.0.0.1:4711",
			map[string]string{"X-Real-IP": "not-an-address", "X-Forwarded-For": "unknown"}}, "127.0.0.1"},
		{callerCase{"a mapped IPv4 peer is IPv4", "[::ffff:203.0.113.9]:4711", nil}, "203.0.113.9"},
		{callerCase{"an unparsable peer", "@", nil}, "invalid IP"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := ClientIP(tc.request()).String()
			t.Logf("input:  peer=%s headers=%v", tc.remote, tc.headers)
			t.Logf("output: %s", got)

			if got != tc.want {
				t.Errorf("ClientIP = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestIsLocalCaller(t *testing.T) {
	cases := []struct {
		callerCase
		want bool
	}{
		{callerCase{"loopback IPv4", "127.0.0.1:4711", nil}, true},
		{callerCase{"loopback IPv6", "[::1]:4711", nil}, true},
		{callerCase{"mapped loopback", "[::ffff:127.0.0.1]:4711", nil}, true},
		{callerCase{"a remote peer", "203.0.113.9:4711", nil}, false},
		{callerCase{"a LAN peer is not local", "192.168.1.20:4711", nil}, false},
		{callerCase{"an unparsable peer", "@", nil}, false},

		// Behind nginx every request arrives from loopback. The header is what
		// gives it away.
		{callerCase{"loopback with X-Forwarded-For", "127.0.0.1:4711",
			map[string]string{"X-Forwarded-For": "198.51.100.7"}}, false},
		{callerCase{"loopback with X-Real-IP", "127.0.0.1:4711",
			map[string]string{"X-Real-IP": "198.51.100.7"}}, false},
		{callerCase{"loopback with Forwarded", "127.0.0.1:4711",
			map[string]string{"Forwarded": "for=198.51.100.7"}}, false},

		// Presence is the evidence: a proxy relaying a request whose own
		// address it reports as loopback, or as nothing, is still a proxy.
		{callerCase{"a forwarding header naming loopback", "127.0.0.1:4711",
			map[string]string{"X-Forwarded-For": "127.0.0.1"}}, false},
		{callerCase{"an empty forwarding header", "127.0.0.1:4711",
			map[string]string{"X-Real-IP": ""}}, false},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := IsLocalCaller(tc.request())
			t.Logf("input:  peer=%s headers=%v", tc.remote, tc.headers)
			t.Logf("output: %t", got)

			if got != tc.want {
				t.Errorf("IsLocalCaller = %t, want %t", got, tc.want)
			}
		})
	}
}
