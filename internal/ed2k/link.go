// Package ed2k builds and parses the ed2k://|httpcache| configuration link.
//
//	ed2k://|httpcache|<name>|<baseUrl>|<secret>[|k=<keyId>][|<opt>=<val>]|/
//
// Three positional fields, then optional key=value ones of which only k=<keyId>
// is defined — the same extensible shape eMule already uses for
// ed2k://|file|name|size|hash|p=…|/. The format is specified in
// docs/ed2k-httpcache-link.md, which is normative; this is its reference
// implementation.
//
// The link is a credential. Anyone holding one can upload under that key and
// delete anything it uploaded, so it is never logged and never echoed back into
// an error message — see Redact.
package ed2k

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ModderMule/emule-http-cache-go/internal/baseurl"
)

// Type is the link's type token, matched case-insensitively.
const Type = "httpcache"

// DefaultName is what the link is called when nothing better is to hand.
const DefaultName = "HTTP Cache upload config"

// maxLength caps the whole link in octets, not characters: the limit is on what
// crosses the wire, and a multibyte name must not buy extra room.
const maxLength = 4096

// maxSecret is the longest opaque secret a link may carry.
const maxSecret = 512

var (
	prefix = "ed2k://|" + Type + "|"

	keyIDPattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	optNamePatten = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9]*)=(.*)$`)
)

// Link is one parsed or buildable configuration link.
type Link struct {
	// Name is a display label for a confirmation dialog. Never an identifier —
	// two links may share a name — and it may be empty.
	Name string

	// BaseURL is the absolute http(s) root the /v1 routes hang off.
	BaseURL string

	// Secret is the API key. Opaque; never parse it.
	Secret string

	// KeyID labels which credential this is. Display only — a client never
	// sends it anywhere. It exists so a user with three caches can tell the
	// entries apart.
	KeyID string
}

// String renders the link.
func (l Link) String() string {
	var b strings.Builder

	b.WriteString(prefix)
	b.WriteString(encodeField(l.Name))
	b.WriteByte('|')
	b.WriteString(encodeField(l.BaseURL))
	b.WriteByte('|')
	b.WriteString(encodeField(l.Secret))

	if l.KeyID != "" {
		b.WriteString("|k=")
		b.WriteString(encodeField(l.KeyID))
	}

	b.WriteString("|/")

	return b.String()
}

// Parse reads a link, returning false for anything that is not a well-formed
// one of this type.
//
// It splits on "|" before decoding anything. In that order, always: decoding
// first would let a %7C inside a name re-split the link into the wrong number
// of fields, which is the classic way a link format grows an injection bug.
func Parse(raw string) (Link, bool) {
	raw = strings.TrimSpace(raw)

	if len(raw) > maxLength {
		return Link{}, false
	}
	if len(raw) < len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return Link{}, false
	}
	if !strings.HasSuffix(raw, "|/") {
		return Link{}, false
	}

	body := raw[len(prefix) : len(raw)-2]
	fields := strings.Split(body, "|")
	if len(fields) < 3 {
		return Link{}, false
	}

	name, ok := decodeField(fields[0])
	if !ok {
		return Link{}, false
	}
	rawBase, ok := decodeField(fields[1])
	if !ok {
		return Link{}, false
	}
	secret, ok := decodeField(fields[2])
	if !ok {
		return Link{}, false
	}

	base, ok := baseurl.Normalize(rawBase)
	if !ok || !plausibleSecret(secret) {
		return Link{}, false
	}

	link := Link{Name: name, BaseURL: base, Secret: secret}

	for _, option := range fields[3:] {
		m := optNamePatten.FindStringSubmatch(option)
		if m == nil {
			// A tail field that is not key=value is a malformed link, not an
			// extension: silently ignoring it would let a typo swallow a real
			// option one day, and the user would never learn why.
			return Link{}, false
		}

		value, ok := decodeField(m[2])
		if !ok {
			return Link{}, false
		}

		// Unknown options are the extension point, and are skipped.
		if m[1] == "k" {
			if !keyIDPattern.MatchString(value) {
				return Link{}, false
			}
			link.KeyID = value
		}
	}

	return link, true
}

// Redact replaces a link's secret with an ellipsis so link text can be logged
// or shown in an error without handing out the credential. It works on a
// malformed link too, which is exactly when an error message wants to quote
// one.
func Redact(raw string) string {
	fields := strings.Split(raw, "|")

	// ed2k://, type, name, baseUrl, secret — the secret is the fifth field.
	const secretField = 4
	if len(fields) > secretField {
		fields[secretField] = "…"
	}

	return strings.Join(fields, "|")
}

// -- internals ---------------------------------------------------------------

// encodeField percent-encodes everything that is not a printable ASCII
// character, plus the two that would otherwise be read as syntax.
//
// Byte-wise on purpose: percent-encoding is defined over octets, so a
// multibyte name is encoded as its UTF-8 octets rather than one escape per
// rune. ":" and "/" are left literal, which keeps a base URL readable.
func encodeField(value string) string {
	var b strings.Builder

	for i := 0; i < len(value); i++ {
		c := value[i]
		if c >= 0x21 && c <= 0x7E && c != '|' && c != '%' {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}

	return b.String()
}

// decodeField percent-decodes one field.
//
// It refuses a "%" that does not start a valid escape, and any octet that
// decodes to a control character — both of which make the whole link invalid.
func decodeField(value string) (string, bool) {
	var b strings.Builder

	for i := 0; i < len(value); i++ {
		c := value[i]

		if c != '%' {
			if isControl(c) {
				return "", false
			}
			b.WriteByte(c)
			continue
		}

		if i+2 >= len(value) {
			return "", false
		}
		hi, ok := unhex(value[i+1])
		if !ok {
			return "", false
		}
		lo, ok := unhex(value[i+2])
		if !ok {
			return "", false
		}

		decoded := hi<<4 | lo
		if isControl(decoded) {
			return "", false
		}

		b.WriteByte(decoded)
		i += 2
	}

	return b.String(), true
}

// plausibleSecret checks the shape a secret must have. It stays opaque
// otherwise: never empty, never whitespace-bearing, printable ASCII only.
func plausibleSecret(secret string) bool {
	if secret == "" || len(secret) > maxSecret {
		return false
	}

	for i := 0; i < len(secret); i++ {
		if secret[i] < 0x21 || secret[i] > 0x7E {
			return false
		}
	}

	return true
}

func isControl(c byte) bool {
	return c < 0x20 || c == 0x7F
}

func unhex(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
