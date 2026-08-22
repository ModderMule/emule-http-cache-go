// Package security holds API-key authentication for the write endpoints.
//
// Only POST and DELETE are authenticated. GET /v1/chunks/{id} is deliberately
// open: the 128-bit random id is the capability, and the body is ciphertext the
// server cannot read. Requiring a key on the download would mean sharing the
// uploader's credential with every downloader, which is strictly worse.
package security

import (
	"crypto/subtle"
	"net/http"
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
