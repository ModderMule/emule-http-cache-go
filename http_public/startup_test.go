package http_public

import (
	"net/netip"
	"testing"
)

func TestStartupURL(t *testing.T) {
	public := netip.MustParseAddr("203.0.113.9")
	lan := netip.MustParseAddr("192.168.1.20")

	cases := []struct {
		label    string
		pinned   string
		listen   string
		basePath string
		locals   []netip.Addr
		want     string
	}{
		{"a pinned URL wins", "http://cache.example.com", ":8080", "", []netip.Addr{lan}, "http://cache.example.com"},
		{"a pinned URL with a base path", "http://cache.example.com", ":8080", "/cache", []netip.Addr{lan}, "http://cache.example.com/cache"},
		{"every interface, a public address among them", "", ":8080", "", []netip.Addr{lan, public}, "http://203.0.113.9:8080"},
		{"every interface, private addresses only", "", ":8080", "", []netip.Addr{lan}, "http://192.168.1.20:8080"},
		{"every interface, under a base path", "", ":8080", "/cache", []netip.Addr{lan}, "http://192.168.1.20:8080/cache"},
		{"every interface, spelled out", "", "0.0.0.0:9000", "", []netip.Addr{lan}, "http://192.168.1.20:9000"},
		{"every interface, IPv6 spelling", "", "[::]:8080", "", []netip.Addr{lan}, "http://192.168.1.20:8080"},
		{"port 80 is left out", "", ":80", "", []netip.Addr{lan}, "http://192.168.1.20"},
		{"bound to loopback", "", "127.0.0.1:8080", "", []netip.Addr{lan}, "http://127.0.0.1:8080"},
		{"bound to one LAN address", "", "10.0.0.5:8080", "", []netip.Addr{lan}, "http://10.0.0.5:8080"},
		{"bound to an IPv6 address", "", "[2001:db8::1]:8080", "", []netip.Addr{lan}, "http://[2001:db8::1]:8080"},
		{"no interface address", "", ":8080", "", nil, "http://localhost:8080"},
		{"an unreadable listen address", "", "nonsense", "", []netip.Addr{lan}, "http://nonsense"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got := startupURL(tc.pinned, tc.listen, tc.basePath, tc.locals)
			t.Logf("input:  pinned=%q listen=%q basePath=%q locals=%v", tc.pinned, tc.listen, tc.basePath, tc.locals)
			t.Logf("output: %q", got)

			if got != tc.want {
				t.Errorf("startupURL = %q, want %q", got, tc.want)
			}
		})
	}
}
