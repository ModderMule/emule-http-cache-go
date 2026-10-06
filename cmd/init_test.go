package cmd

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/ModderMule/emule-http-cache-go/pkg/publicaddr"
)

func TestDescribeChoice(t *testing.T) {
	public := netip.MustParseAddr("203.0.113.9")
	lan := netip.MustParseAddr("192.168.1.20")

	cases := []struct {
		label  string
		choice publicaddr.Choice
		by     string
		want   []string // each must appear somewhere in the notes
	}{
		{"confirmed", publicaddr.Choice{Kind: publicaddr.Verified, Host: public, Port: 8080},
			"https://echo.example", []string{"203.0.113.9", "confirmed by https://echo.example"}},
		{"behind a router", publicaddr.Choice{Kind: publicaddr.BehindNAT, Host: public, Port: 8080, LAN: lan},
			"https://echo.example", []string{"Forward TCP port 8080 on it to 192.168.1.20", "http://192.168.1.20:8080"}},
		{"behind a router, own address unknown", publicaddr.Choice{Kind: publicaddr.BehindNAT, Host: public, Port: 8080},
			"https://echo.example", []string{"Forward TCP port 8080 on it to this machine"}},
		{"behind a router and a local proxy", publicaddr.Choice{Kind: publicaddr.BehindNAT, Host: public, LAN: lan, Proxied: true},
			"https://echo.example", []string{"Forward TCP port 80 ", "http://192.168.1.20.", "reverse proxy"}},
		{"unconfirmed", publicaddr.Choice{Kind: publicaddr.Unverified, Host: public, Port: 8080},
			"", []string{"203.0.113.9", "unconfirmed"}},
		{"private", publicaddr.Choice{Kind: publicaddr.LANOnly, Host: lan, Port: 8080},
			"", []string{"192.168.1.20 is a private address"}},
		{"nothing found", publicaddr.Choice{},
			"", []string{"none was pinned"}},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			notes := strings.Join(describeChoice(tc.choice, tc.by), "\n")
			t.Logf("input:  %+v by=%q", tc.choice, tc.by)
			t.Logf("output:\n%s", notes)

			for _, want := range tc.want {
				if !strings.Contains(notes, want) {
					t.Errorf("the notes do not mention %q", want)
				}
			}
		})
	}
}

func TestLocalBaseURL(t *testing.T) {
	cases := map[string]string{
		":8080":          "http://localhost:8080",
		"0.0.0.0:9000":   "http://localhost:9000",
		"127.0.0.1:8080": "http://localhost:8080",
		":80":            "http://localhost",
		"nonsense":       "http://localhost",
	}

	for listen, want := range cases {
		got := localBaseURL(listen)
		t.Logf("input:  %q", listen)
		t.Logf("output: %q", got)

		if got != want {
			t.Errorf("localBaseURL(%q) = %q, want %q", listen, got, want)
		}
	}
}
