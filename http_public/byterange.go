package http_public

import (
	"regexp"
	"strconv"
	"strings"
)

// byteRange is a single parsed range, per RFC 9110 section 14, inclusive of
// both ends.
type byteRange struct {
	from int64
	to   int64
}

func (r byteRange) length() int64 { return r.to - r.from + 1 }

// rangeVerdict says what a Range header asked for.
type rangeVerdict int

const (
	// rangeWhole means serve the entire entity with a 200. It covers an absent
	// header, a unit other than bytes, and a multi-range request — RFC 9110
	// section 14.2 explicitly permits answering that last one with the full
	// entity, and the eMuleQt downloader cannot parse multipart/byteranges, so
	// it is the only safe answer here.
	rangeWhole rangeVerdict = iota

	// rangeSatisfiable means serve a 206 for the parsed span.
	rangeSatisfiable

	// rangeUnsatisfiable means 416.
	rangeUnsatisfiable
)

// rangeSpecPattern is the first-byte-pos/last-byte-pos grammar, either side
// optional but not both.
var rangeSpecPattern = regexp.MustCompile(`^(\d*)-(\d*)$`)

// rangeUnitPattern is deliberately laxer than the stdlib's, matching the PHP
// server: leading whitespace, any case, and spaces around "=" are all accepted.
var rangeUnitPattern = regexp.MustCompile(`(?i)^\s*bytes\s*=\s*(.+)$`)

// parseByteRange resolves a Range header against an entity size.
//
// This is a port of the PHP server's ByteRange::parse rather than a call to
// http.ServeContent, which diverges in four ways that matter to a client with
// no error recovery: it answers a multi-range request with multipart/byteranges,
// strips Cache-Control and ETag on its 416 path, treats "bytes=-0" as a valid
// zero-length range, and adds a Last-Modified header the contract does not
// describe.
func parseByteRange(header string, size int64) (byteRange, rangeVerdict) {
	if header == "" || size <= 0 {
		return byteRange{}, rangeWhole
	}

	m := rangeUnitPattern.FindStringSubmatch(header)
	if m == nil {
		return byteRange{}, rangeWhole // not a byte range unit — ignore it
	}

	spec := strings.TrimSpace(m[1])
	if strings.Contains(spec, ",") {
		return byteRange{}, rangeWhole // multi-range: fall back to the full entity
	}

	parts := rangeSpecPattern.FindStringSubmatch(spec)
	if parts == nil {
		return byteRange{}, rangeUnsatisfiable
	}

	rawFirst, rawLast := parts[1], parts[2]
	if rawFirst == "" && rawLast == "" {
		return byteRange{}, rangeUnsatisfiable
	}

	// Suffix form: "bytes=-500" means the final 500 bytes.
	if rawFirst == "" {
		length, err := strconv.ParseInt(rawLast, 10, 64)
		if err != nil || length <= 0 {
			return byteRange{}, rangeUnsatisfiable
		}

		from := size - length
		if from < 0 {
			from = 0
		}

		return byteRange{from: from, to: size - 1}, rangeSatisfiable
	}

	from, err := strconv.ParseInt(rawFirst, 10, 64)
	if err != nil || from >= size {
		return byteRange{}, rangeUnsatisfiable
	}

	to := size - 1
	if rawLast != "" {
		// An unparseable last-byte-pos is one too large to hold, which clamps
		// to the end of the entity exactly as an in-range large value would.
		if parsed, err := strconv.ParseInt(rawLast, 10, 64); err == nil && parsed < to {
			to = parsed
		}
	}

	if to < from {
		return byteRange{}, rangeUnsatisfiable
	}

	return byteRange{from: from, to: to}, rangeSatisfiable
}
