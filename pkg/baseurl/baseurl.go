// Package baseurl defines what counts as the base URL of a cache, in one place.
//
// Two callers need the same answer and must not disagree: the install form,
// where an operator pins one by hand, and the ed2k config link, where the same
// URL arrives from a stranger's clipboard.
package baseurl

import (
	"net/netip"
	"net/url"
	"strings"
)

// Normalize returns the trimmed base URL, or false when it is not one.
//
// Accepted: an absolute http or https URL with a host. Refused: any other
// scheme, a relative URL, and anything carrying credentials, a query or a
// fragment — those mean the sender is describing something other than the root
// of an API.
func Normalize(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return "", false
	}

	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return "", false
	}

	if parsed.Host == "" {
		return "", false
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", false
	}

	return strings.TrimRight(raw, "/"), true
}

// IsLoopback reports whether a base URL names the machine it is opened on:
// localhost, a name under .localhost, or a loopback address.
//
// Such a URL is fine in the operator's own browser and useless in a link meant
// for a client somewhere else.
func IsLoopback(base string) bool {
	parsed, err := url.Parse(strings.TrimSpace(base))
	if err != nil {
		return false
	}

	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}

	addr, err := netip.ParseAddr(host)

	return err == nil && addr.Unmap().IsLoopback()
}
