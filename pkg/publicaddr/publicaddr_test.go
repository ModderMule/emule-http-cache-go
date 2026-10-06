package publicaddr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
	"time"
)

const cloudflareTrace = "fl=123f456\nh=www.cloudflare.com\nip=203.0.113.9\nts=1791369600.123\nvisit_scheme=https\nuag=Go-http-client/1.1\n"

func TestIsPublic(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"203.0.113.9", true},
		{"8.8.8.8", true},
		{"::ffff:8.8.8.8", true},
		{"2001:db8::1", true},
		{"127.0.0.1", false},
		{"10.0.0.5", false},
		{"172.16.4.4", false},
		{"192.168.1.20", false},
		{"100.64.0.1", false},
		{"100.127.255.254", false},
		{"169.254.10.10", false},
		{"0.0.0.0", false},
		{"224.0.0.1", false},
		{"::1", false},
		{"fe80::1", false},
		{"fd00::1", false},
	}

	for _, tc := range cases {
		got := IsPublic(netip.MustParseAddr(tc.addr))
		t.Logf("input:  %s", tc.addr)
		t.Logf("output: %v", got)

		if got != tc.want {
			t.Errorf("IsPublic(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}

	if IsPublic(netip.Addr{}) {
		t.Errorf("the zero Addr is not public")
	}
}

func TestProbe(t *testing.T) {
	cases := []struct {
		label  string
		bodies []string // one echo service each; "!500" answers with that status
		want   string   // "" means the probe must fail
	}{
		{"a bare address", []string{"203.0.113.9\n"}, "203.0.113.9"},
		{"a quoted address", []string{`"203.0.113.9"`}, "203.0.113.9"},
		{"a Cloudflare trace", []string{cloudflareTrace}, "203.0.113.9"},
		{"garbage", []string{"<html>blocked</html>"}, ""},
		{"a private address", []string{"192.168.1.20"}, ""},
		{"a carrier-grade NAT address", []string{"100.64.0.1"}, ""},
		{"an IPv6 address", []string{"2001:db8::1"}, ""},
		{"an error status", []string{"!500"}, ""},
		{"one good service among bad ones", []string{"!500", "nope", "203.0.113.9"}, "203.0.113.9"},
		{"every service failing", []string{"!500", "nope"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			var urls []string
			for _, body := range tc.bodies {
				urls = append(urls, echoServer(t, body, 0))
			}

			addr, by, err := Probe(context.Background(), urls, time.Second)
			t.Logf("input:  echo bodies %q", tc.bodies)
			t.Logf("output: addr=%v by=%q err=%v", addr, by, err)

			if tc.want == "" {
				if err == nil {
					t.Fatalf("Probe succeeded with %s, want an error", addr)
				}
				return
			}

			if err != nil {
				t.Fatalf("Probe: %v", err)
			}
			if addr.String() != tc.want {
				t.Errorf("addr = %s, want %s", addr, tc.want)
			}
			if by == "" {
				t.Errorf("the answering URL was not reported")
			}
		})
	}
}

func TestProbeDoesNotWaitForASlowService(t *testing.T) {
	slow := echoServer(t, "203.0.113.1", 2*time.Second)
	fast := echoServer(t, "203.0.113.2", 0)

	started := time.Now()
	addr, by, err := Probe(context.Background(), []string{slow, fast}, 5*time.Second)
	took := time.Since(started)

	t.Logf("input:  a service answering after 2s, and one answering at once")
	t.Logf("output: addr=%v by=%q err=%v after %s", addr, by, err, took.Round(time.Millisecond))

	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if addr.String() != "203.0.113.2" || by != fast {
		t.Errorf("answer came from %s (%s), want the fast service", by, addr)
	}
	if took > time.Second {
		t.Errorf("Probe took %s: it waited for the slow service", took)
	}
}

func TestProbeGivesUpAtTheTimeout(t *testing.T) {
	slow := echoServer(t, "203.0.113.1", 2*time.Second)

	started := time.Now()
	addr, _, err := Probe(context.Background(), []string{slow}, 100*time.Millisecond)
	took := time.Since(started)

	t.Logf("input:  one service answering after 2s, timeout 100ms")
	t.Logf("output: addr=%v err=%v after %s", addr, err, took.Round(time.Millisecond))

	if err == nil {
		t.Fatalf("Probe succeeded with %s, want a timeout", addr)
	}
	if took > time.Second {
		t.Errorf("Probe took %s, want about 100ms", took)
	}
}

func TestProbeWithNothingToAsk(t *testing.T) {
	addr, _, err := Probe(context.Background(), []string{"", "  "}, time.Second)
	t.Logf("input:  only blank URLs")
	t.Logf("output: addr=%v err=%v", addr, err)

	if err == nil {
		t.Errorf("Probe succeeded with no service to ask")
	}
}

func TestChoose(t *testing.T) {
	cases := []struct {
		label   string
		probed  string
		locals  []string
		listen  string
		kind    Kind
		baseURL string
		lan     string
		proxied bool
	}{
		{"a server with its own public address", "203.0.113.9", []string{"203.0.113.9"}, ":8080",
			Verified, "http://203.0.113.9:8080", "", false},
		{"a host behind a router", "203.0.113.9", []string{"192.168.1.20"}, ":8080",
			BehindNAT, "http://203.0.113.9:8080", "192.168.1.20", false},
		{"behind NAT with no interface found", "203.0.113.9", nil, ":8080",
			BehindNAT, "http://203.0.113.9:8080", "", false},
		{"offline, with a public address", "", []string{"10.0.0.5", "203.0.113.9"}, ":8080",
			Unverified, "http://203.0.113.9:8080", "", false},
		{"offline, on a LAN", "", []string{"192.168.1.20", "10.0.0.5"}, ":8080",
			LANOnly, "http://192.168.1.20:8080", "", false},
		{"offline, behind carrier-grade NAT", "", []string{"100.64.0.7"}, ":8080",
			LANOnly, "http://100.64.0.7:8080", "", false},
		{"no address at all", "", nil, ":8080",
			None, "", "", false},

		{"listening on port 80", "203.0.113.9", []string{"203.0.113.9"}, ":80",
			Verified, "http://203.0.113.9", "", false},
		{"listening on every address, spelled out", "203.0.113.9", []string{"203.0.113.9"}, "0.0.0.0:9000",
			Verified, "http://203.0.113.9:9000", "", false},
		{"bound to loopback, so behind a proxy", "203.0.113.9", []string{"203.0.113.9"}, "127.0.0.1:8080",
			Verified, "http://203.0.113.9", "", true},
		{"bound to localhost by name", "", []string{"192.168.1.20"}, "localhost:8080",
			LANOnly, "http://192.168.1.20", "", true},
		{"bound to one LAN address of two", "", []string{"192.168.1.20", "10.0.0.5"}, "10.0.0.5:8080",
			LANOnly, "http://10.0.0.5:8080", "", false},
		{"bound to a LAN address behind NAT", "203.0.113.9", []string{"192.168.1.20", "10.0.0.5"}, "10.0.0.5:8080",
			BehindNAT, "http://203.0.113.9:8080", "10.0.0.5", false},
		{"an unreadable listen address", "203.0.113.9", []string{"203.0.113.9"}, "nonsense",
			Verified, "http://203.0.113.9", "", false},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := Choose(addrOrZero(tc.probed), addrs(tc.locals), tc.listen)
			t.Logf("input:  probed=%q locals=%v listen=%q", tc.probed, tc.locals, tc.listen)
			t.Logf("output: %+v → %q", got, got.BaseURL())

			if got.Kind != tc.kind {
				t.Errorf("kind = %d, want %d", got.Kind, tc.kind)
			}
			if got.BaseURL() != tc.baseURL {
				t.Errorf("base URL = %q, want %q", got.BaseURL(), tc.baseURL)
			}
			if got.LAN != addrOrZero(tc.lan) {
				t.Errorf("LAN = %v, want %q", got.LAN, tc.lan)
			}
			if got.Proxied != tc.proxied {
				t.Errorf("proxied = %v, want %v", got.Proxied, tc.proxied)
			}
		})
	}
}

func TestLocalListsNothingUnusable(t *testing.T) {
	got := Local()
	t.Logf("input:  this machine's interfaces")
	t.Logf("output: %v", got)

	for _, addr := range got {
		if !addr.Is4() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
			t.Errorf("%s is not an address another machine could use", addr)
		}
	}
}

// -- the harness -------------------------------------------------------------

// echoServer answers every request with body after a delay. A body of the form
// "!500" answers with that status instead.
func echoServer(t *testing.T, body string, delay time.Duration) string {
	t.Helper()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}

		if body == "!500" {
			http.Error(w, "broken", http.StatusInternalServerError)
			return
		}

		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(ts.Close)

	return ts.URL
}

func addrOrZero(raw string) netip.Addr {
	if raw == "" {
		return netip.Addr{}
	}

	return netip.MustParseAddr(raw)
}

func addrs(raw []string) []netip.Addr {
	var out []netip.Addr
	for _, r := range raw {
		out = append(out, netip.MustParseAddr(r))
	}

	return out
}
