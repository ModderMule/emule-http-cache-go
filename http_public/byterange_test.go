package http_public

import "testing"

// TestParseByteRange is the grammar table from the PHP server's ByteRange, plus
// the four cases where Go's own net/http range parser diverges from it.
func TestParseByteRange(t *testing.T) {
	const size = 9_728_016

	cases := []struct {
		label   string
		header  string
		verdict rangeVerdict
		from    int64
		to      int64
	}{
		{"absent header serves the whole entity", "", rangeWhole, 0, 0},
		{"a non-bytes unit is ignored", "items=0-99", rangeWhole, 0, 0},

		// RFC 9110 section 14.2 permits answering a multi-range request with
		// the full entity, and the real client cannot parse multipart at all.
		{"multi-range falls back to the whole entity", "bytes=0-99, 200-299", rangeWhole, 0, 0},

		{"a plain span", "bytes=1000-1999", rangeSatisfiable, 1000, 1999},
		{"the first request a downloader makes", "bytes=0-9728015", rangeSatisfiable, 0, size - 1},
		{"an open-ended resume", "bytes=9723920-", rangeSatisfiable, 9723920, size - 1},
		{"a suffix range", "bytes=-16", rangeSatisfiable, size - 16, size - 1},
		{"a suffix longer than the entity clamps to it", "bytes=-99999999", rangeSatisfiable, 0, size - 1},
		{"a last-byte-pos past the end clamps", "bytes=0-99999999", rangeSatisfiable, 0, size - 1},

		{"a first-byte-pos past the end is unsatisfiable", "bytes=999999999-", rangeUnsatisfiable, 0, 0},
		{"a reversed span is unsatisfiable", "bytes=500-100", rangeUnsatisfiable, 0, 0},
		{"a bare dash is unsatisfiable", "bytes=-", rangeUnsatisfiable, 0, 0},

		// Go's parseRange would call this a valid zero-length range and answer
		// 206 with "bytes 9728016-9728015/9728016".
		{"bytes=-0 is unsatisfiable, not a zero-length 206", "bytes=-0", rangeUnsatisfiable, 0, 0},

		// Go's parseRange requires a lowercase "bytes=" with no padding.
		{"the unit is case-insensitive and may be padded", "Bytes = 0-99", rangeSatisfiable, 0, 99},

		{"an unparseable spec is unsatisfiable", "bytes=abc", rangeUnsatisfiable, 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			t.Logf("input:  Range: %q against %d bytes", tc.header, size)

			span, verdict := parseByteRange(tc.header, size)
			t.Logf("output: verdict=%d span=%+v", verdict, span)

			if verdict != tc.verdict {
				t.Fatalf("verdict = %d, want %d", verdict, tc.verdict)
			}
			if verdict == rangeSatisfiable && (span.from != tc.from || span.to != tc.to) {
				t.Errorf("span = %d-%d, want %d-%d", span.from, span.to, tc.from, tc.to)
			}
		})
	}
}

func TestScrubPath(t *testing.T) {
	cases := map[string]string{
		"/v1/chunks/87d7f7573b0263fc9faf9ed65cb62841": "/v1/chunks/<id>",
		"/v1/chunks":           "/v1/chunks",
		"/v1/info":             "/v1/info",
		"/v1/chunks/not-an-id": "/v1/chunks/not-an-id",
	}

	for in, want := range cases {
		t.Logf("input:  %s", in)

		got := scrubPath(in)
		t.Logf("output: %s", got)

		if got != want {
			t.Errorf("scrubPath(%q) = %q, want %q — a chunk id is a bearer token and must not reach a log", in, got, want)
		}
	}
}
