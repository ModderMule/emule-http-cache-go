package install

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ModderMule/emule-http-cache-go/internal/config"
	"github.com/ModderMule/emule-http-cache-go/pkg/baseurl"
)

const (
	gib          = 1024 * 1024 * 1024
	maxGigabytes = 1_000_000
	maxHours     = 8760
)

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)

// Settings is the install form once it has survived validation.
//
// The form asks in the units an operator thinks in — gigabytes and hours — and
// this is where they become the bytes and durations the config file stores.
// Nothing reaches the Installer until every field has parsed.
type Settings struct {
	KeyID                      string
	OpenUpload                 bool
	OpenUploadQuotaBytesPerDay int64
	QuotaBytesPerDay           int64
	MinFreeBytes               int64
	DefaultTTL                 time.Duration
	MaxTTL                     time.Duration
	PublicBaseURL              string
}

// FormField names one control on the install page, in render order.
type FormField struct {
	Name  string
	Label string
	Hint  string
	Kind  string // "text", "number" or "checkbox"
	Min   string
}

// FormFields is the install form, in the order it is rendered. The page walks
// this rather than concatenating markup, so a field cannot be added in one
// place and forgotten in the other.
var FormFields = []FormField{
	{Name: "keyId", Kind: "text", Label: "Key id",
		Hint: `Names this uploader in chunk metadata and in its quota counter. "anonymous" is reserved.`},
	{Name: "openUpload", Kind: "checkbox", Label: "Anyone can upload",
		Hint: "Accept uploads with no API key at all. Convenient, and it means any stranger who finds the URL can spend your disk and your bandwidth. Anonymous chunks cannot be deleted through the API — they only lapse at their TTL."},
	{Name: "openUploadQuotaGb", Kind: "number", Min: "0", Label: "Daily limit for anonymous uploads (GB)",
		Hint: `Only applies when the box above is ticked. 0 means unlimited, which on an open server means "please fill my disk".`},
	{Name: "quotaGb", Kind: "number", Min: "0", Label: "Daily limit for this key (GB)",
		Hint: "0 means unlimited. Counted per UTC day."},
	{Name: "minFreeGb", Kind: "number", Min: "0", Label: "Keep this much disk free (GB)",
		Hint: "Uploads are refused with 507 once free space would drop below it, whoever is asking."},
	{Name: "defaultTtlHours", Kind: "number", Min: "1", Label: "Default lifetime (hours)",
		Hint: "Applied when the client asks for nothing specific."},
	{Name: "maxTtlHours", Kind: "number", Min: "1", Label: "Longest lifetime a client may ask for (hours)",
		Hint: "Any longer request is clamped to this."},
	{Name: "publicBaseUrl", Kind: "text", Label: "Public base URL",
		Hint: "Leave blank to work it out from each request. Pin it when a reverse proxy rewrites Host, or peers get URLs pointing at the wrong place. Keep it http:// — eMuleQt fetches chunks over a plain socket with no TLS."},
}

// FormDefaults is what the empty form shows. Raw strings, because that is what
// a form holds.
func FormDefaults() map[string]string {
	return map[string]string{
		"keyId":             "default",
		"openUpload":        "",
		"openUploadQuotaGb": "10",
		"quotaGb":           "0",
		"minFreeGb":         "1",
		"defaultTtlHours":   "48",
		"maxTtlHours":       "168",
		"publicBaseUrl":     "",
	}
}

// FromForm validates one submission.
//
// Errors are keyed by field name and every field is reported at once, because
// one page has to be able to fix everything. Settings is nil unless every field
// passed, so a partial config can never be written.
func FromForm(input map[string]string) (*Settings, map[string]string) {
	errs := map[string]string{}

	keyID := strings.TrimSpace(input["keyId"])
	switch {
	case !keyIDPattern.MatchString(keyID):
		errs["keyId"] = "Letters, digits, dot, dash or underscore. 32 characters at most."
	case keyID == config.AnonymousKeyID:
		errs["keyId"] = `"anonymous" is reserved for uploads that arrive without a key.`
	}

	// An unticked checkbox is simply absent from the submission.
	openUpload := strings.TrimSpace(input["openUpload"]) != ""

	openQuota := gigabytes(input, "openUploadQuotaGb", errs)
	quota := gigabytes(input, "quotaGb", errs)
	minFree := gigabytes(input, "minFreeGb", errs)
	defaultTTL := hours(input, "defaultTtlHours", errs)
	maxTTL := hours(input, "maxTtlHours", errs)

	if defaultTTL > 0 && maxTTL > 0 && maxTTL < defaultTTL {
		errs["maxTtlHours"] = "The ceiling cannot be lower than the default."
	}

	publicBaseURL := strings.TrimSpace(input["publicBaseUrl"])
	if publicBaseURL != "" {
		normalised, ok := baseurl.Normalize(publicBaseURL)
		if !ok {
			errs["publicBaseUrl"] = "An absolute http:// or https:// URL, with no query string."
		}
		publicBaseURL = normalised
	}

	if len(errs) > 0 {
		return nil, errs
	}

	return &Settings{
		KeyID:                      keyID,
		OpenUpload:                 openUpload,
		OpenUploadQuotaBytesPerDay: openQuota,
		QuotaBytesPerDay:           quota,
		MinFreeBytes:               minFree,
		DefaultTTL:                 defaultTTL,
		MaxTTL:                     maxTTL,
		PublicBaseURL:              publicBaseURL,
	}, nil
}

// -- internals ---------------------------------------------------------------

func gigabytes(input map[string]string, field string, errs map[string]string) int64 {
	raw := strings.TrimSpace(input[field])

	value, err := strconv.ParseFloat(raw, 64)
	if raw == "" || err != nil || value < 0 {
		errs[field] = "A number of gigabytes, 0 or more."
		return 0
	}
	if value > maxGigabytes {
		errs[field] = "That is more storage than anyone has."
		return 0
	}

	return int64(value*gib + 0.5)
}

func hours(input map[string]string, field string, errs map[string]string) time.Duration {
	raw := strings.TrimSpace(input[field])

	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		errs[field] = "A whole number of hours, 1 or more."
		return 0
	}
	if value > maxHours {
		errs[field] = "A year is the ceiling."
		return 0
	}

	return time.Duration(value) * time.Hour
}

// describeBytes is the trailing comment written next to a byte count, so the
// generated config still reads in the units the operator typed.
func describeBytes(n int64) string {
	if n == 0 {
		return "unlimited"
	}

	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", float64(n)/gib), "0"), ".") + " GiB"
}
