package ed2k

import "testing"

// TestRoundTrip walks the reference table in docs/ed2k-httpcache-link.md. The
// third and fourth vectors are the ones worth having: a literal "|" in the name
// that must survive as %7C, and a non-ASCII name that must be encoded as UTF-8
// octets rather than as anything else.
func TestRoundTrip(t *testing.T) {
	cases := []struct {
		label string
		link  Link
		want  string
	}{
		{
			label: "the plain form",
			link:  Link{Name: DefaultName, BaseURL: "https://cache.example.com", Secret: "1f4b9c02d7e35a68"},
			want:  "ed2k://|httpcache|HTTP%20Cache%20upload%20config|https://cache.example.com|1f4b9c02d7e35a68|/",
		},
		{
			label: "with a key id",
			link:  Link{Name: DefaultName, BaseURL: "http://192.168.1.10/emule-http-cache-php", Secret: "1f4b9c02d7e35a68", KeyID: "default"},
			want:  "ed2k://|httpcache|HTTP%20Cache%20upload%20config|http://192.168.1.10/emule-http-cache-php|1f4b9c02d7e35a68|k=default|/",
		},
		{
			label: "a pipe in the name",
			link:  Link{Name: "Nachbars WLAN | Cache", BaseURL: "https://cache.example.com", Secret: "abc123", KeyID: "seedbox"},
			want:  "ed2k://|httpcache|Nachbars%20WLAN%20%7C%20Cache|https://cache.example.com|abc123|k=seedbox|/",
		},
		{
			label: "a non-ASCII name",
			link:  Link{Name: "Zwischenspeicher für eMule", BaseURL: "https://cache.example.com", Secret: "abc123"},
			want:  "ed2k://|httpcache|Zwischenspeicher%20f%C3%BCr%20eMule|https://cache.example.com|abc123|/",
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Logf("input:  %+v", tc.link)

			got := tc.link.String()
			t.Logf("output: %s", got)

			if got != tc.want {
				t.Errorf("String() = %s, want %s", got, tc.want)
			}

			back, ok := Parse(got)
			t.Logf("reparsed: %+v ok=%t", back, ok)

			if !ok {
				t.Fatalf("Parse(%s) refused a link we built", got)
			}
			if back != tc.link {
				t.Errorf("round trip changed the link: got %+v, want %+v", back, tc.link)
			}
		})
	}
}

// TestRefusals is the rejection table from the same document. Each of these
// must refuse the whole link — never partially apply one.
func TestRefusals(t *testing.T) {
	cases := map[string]string{
		"the wrong link type":         "ed2k://|file|x|1|abc|/",
		"too few fields":              "ed2k://|httpcache|name|https://h|/",
		"no terminator":               "ed2k://|httpcache|n|https://h|s",
		"a non-http scheme":           "ed2k://|httpcache|n|ftp://h|s|/",
		"a relative base URL":         "ed2k://|httpcache|n|/relative|s|/",
		"credentials in the base URL": "ed2k://|httpcache|n|https://u:p@h|s|/",
		"a query string":              "ed2k://|httpcache|n|https://h?q=1|s|/",
		`a tail field without "="`:    "ed2k://|httpcache|n|https://h|s|junk|/",
		"an empty secret":             "ed2k://|httpcache|n|https://h||/",
		"a malformed key id":          "ed2k://|httpcache|n|https://h|s|k=has spaces|/",
		"a broken percent escape":     "ed2k://|httpcache|n|https://h|s%ZZ|/",
		"a secret with whitespace":    "ed2k://|httpcache|n|https://h|a%20b|/",
	}

	for label, link := range cases {
		t.Run(label, func(t *testing.T) {
			t.Logf("input:  %s", link)

			got, ok := Parse(link)
			t.Logf("output: %+v ok=%t", got, ok)

			if ok {
				t.Errorf("Parse accepted a link it must refuse")
			}
		})
	}
}

// TestAccepted covers the two the document says must be accepted, plus the
// oversize bound.
func TestAccepted(t *testing.T) {
	t.Run("an unknown option is skipped, not fatal", func(t *testing.T) {
		const link = "ed2k://|httpcache|n|https://h|s|x=1|k=abc|/"
		t.Logf("input:  %s", link)

		got, ok := Parse(link)
		t.Logf("output: %+v ok=%t", got, ok)

		if !ok || got.KeyID != "abc" {
			t.Errorf("want keyId abc, got %+v ok=%t", got, ok)
		}
	})

	t.Run("the scheme and type are case-insensitive", func(t *testing.T) {
		const link = "ED2K://|HTTPCACHE|n|https://h|s|/"
		t.Logf("input:  %s", link)

		got, ok := Parse(link)
		t.Logf("output: %+v ok=%t", got, ok)

		if !ok {
			t.Errorf("Parse refused a link differing only in case")
		}
	})

	t.Run("an absurdly long link is refused", func(t *testing.T) {
		link := "ed2k://|httpcache|" + string(make([]byte, 0, 5000))
		for i := 0; i < 5000; i++ {
			link += "a"
		}
		link += "|https://h|s|/"
		t.Logf("input:  a %d-octet link", len(link))

		_, ok := Parse(link)
		t.Logf("output: ok=%t", ok)

		if ok {
			t.Errorf("Parse accepted a link over the 4096-octet cap")
		}
	})
}

// TestRedact proves the secret is hidden in a malformed link as well as a valid
// one, which is exactly when an error message wants to quote one.
func TestRedact(t *testing.T) {
	cases := []struct {
		label string
		in    string
	}{
		{"a valid link", "ed2k://|httpcache|n|https://h|s3cr3t|k=default|/"},
		{"a malformed link", "ed2k://|httpcache|n|ftp://h|s3cr3t|junk|/"},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Logf("input:  %s", tc.in)

			got := Redact(tc.in)
			t.Logf("output: %s", got)

			if got == tc.in {
				t.Errorf("Redact left the link unchanged")
			}
			if contains(got, "s3cr3t") {
				t.Errorf("Redact left the secret in place: %s", got)
			}
		})
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}

	return false
}
