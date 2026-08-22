// Package baseurl defines what counts as the base URL of a cache, in one place.
//
// Two callers need the same answer and must not disagree: the install form,
// where an operator pins one by hand, and the ed2k config link, where the same
// URL arrives from a stranger's clipboard.
package baseurl

import (
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
