// Package security holds API-key authentication for the write endpoints, and
// the rules for working out who is calling.
//
// Only POST, DELETE and the operator's GET /v1/stats are authenticated.
// GET /v1/chunks/{id} is deliberately open: the 128-bit random id is the
// capability, and the body is ciphertext the server cannot read. Requiring a
// key on the download would mean sharing the uploader's credential with every
// downloader, which is strictly worse.
package security

import (
	"crypto/subtle"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"strings"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
)

// bearerPattern is the RFC 6750 credentials form: exactly one "Bearer" word,
// then a token with no whitespace in it, surrounding space tolerated.
//
// A regexp rather than strings.Fields because the details are an auth
// difference: a token may not contain whitespace, and nothing may follow it.
var bearerPattern = regexp.MustCompile(`(?i)^\s*Bearer\s+(\S+)\s*$`)

// forwardingHeaders are what a reverse proxy adds to say whom it is speaking
// for. The sample nginx config sets the first two.
var forwardingHeaders = []string{"X-Real-Ip", "X-Forwarded-For", "Forwarded"}

// Identify returns the configured key id behind a request's credential, or
// false when it is missing or wrong.
func Identify(cfg *config.Config, r *http.Request) (string, bool) {
	presented := PresentedSecret(r)
	if presented == "" {
		return "", false
	}

	// Compare against every configured key with a constant-time compare, and do
	// not break early: the number of comparisons must not depend on which key
	// matched. The enabled test sits after the compare for the same reason — a
	// revoked key must cost exactly what a live one costs.
	matched := ""
	found := false
	for _, key := range cfg.APIKeys {
		equal := subtle.ConstantTimeCompare([]byte(key.Secret), []byte(presented)) == 1
		if equal && key.Enabled {
			matched, found = key.ID, true
		}
	}

	return matched, found
}

// HasCredential reports whether the request offered a credential at all, right
// or wrong.
//
// Identify flattens "absent" and "wrong" to false, and an open server has to
// tell them apart: an absent key is an anonymous upload, a wrong one is still a
// 401.
func HasCredential(r *http.Request) bool {
	return PresentedSecret(r) != ""
}

// PresentedSecret reads the bearer token a request carries.
//
// Authorization wins; X-Api-Key is the fallback the PHP server also accepts.
func PresentedSecret(r *http.Request) string {
	if header := r.Header.Get("Authorization"); header != "" {
		if m := bearerPattern.FindStringSubmatch(header); m != nil {
			return m[1]
		}
	}

	return strings.TrimSpace(r.Header.Get("X-Api-Key"))
}

// OwnsChunk reports whether a key id is the recorded owner of a chunk.
//
// Constant time for symmetry with the credential compare, though neither value
// is secret here. What actually matters is the caller's response to false: a
// chunk belonging to another key is reported as 404, not 403, so a valid key
// cannot be used to probe the id space.
func OwnsChunk(ownerKeyID, keyID string) bool {
	return subtle.ConstantTimeCompare([]byte(ownerKeyID), []byte(keyID)) == 1
}

// ClientIP is the address a request should be attributed to. It is the zero
// Addr when the peer address cannot be parsed.
//
// That is the peer on the other end of the connection, with one exception: a
// loopback peer is a reverse proxy on this host, so what it says in X-Real-IP,
// or failing that the last hop of X-Forwarded-For, is believed. The last hop is
// the one the proxy itself appended; anything before it came from the client.
//
// A remote peer's headers are ignored outright, so nobody can attribute their
// requests to another address. gin's own ClientIP is not used because it trusts
// every peer by default.
func ClientIP(r *http.Request) netip.Addr {
	peer, ok := peerAddr(r)
	if !ok || !peer.IsLoopback() {
		return peer
	}

	if addr, ok := parseAddr(r.Header.Get("X-Real-Ip")); ok {
		return addr
	}

	hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	if addr, ok := parseAddr(hops[len(hops)-1]); ok {
		return addr
	}

	return peer
}

// IsLocalCaller reports whether a request was made on this host, by someone
// with a shell on it, rather than relayed here by a reverse proxy.
//
// A loopback peer is not enough: behind nginx every request arrives from
// 127.0.0.1. So any forwarding header disqualifies the request, whatever it
// says — its presence is the evidence, and its value is never consulted.
func IsLocalCaller(r *http.Request) bool {
	peer, ok := peerAddr(r)
	if !ok || !peer.IsLoopback() {
		return false
	}

	for _, name := range forwardingHeaders {
		if _, present := r.Header[name]; present {
			return false
		}
	}

	return true
}

// -- internals ---------------------------------------------------------------

// peerAddr is the address on the other end of the connection.
func peerAddr(r *http.Request) (netip.Addr, bool) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	return parseAddr(host)
}

// parseAddr reads one address, folding an IPv4-mapped IPv6 address back to
// IPv4 so ::ffff:127.0.0.1 is the loopback it is.
func parseAddr(raw string) (netip.Addr, bool) {
	addr, err := netip.ParseAddr(strings.TrimSpace(raw))
	if err != nil {
		return netip.Addr{}, false
	}

	return addr.Unmap(), true
}
