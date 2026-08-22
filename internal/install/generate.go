package install

import (
	_ "embed"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// embeddedExample is the canonical template.
//
// Compiled in because a Go deploy is one binary and must be able to install
// itself with no sibling files. A config.example.yaml next to the binary wins
// when one exists, so an operator can customise the template.
//
//go:embed assets/config.example.yaml
var embeddedExample string

// The two literals inside the sample's api_keys block. Each must appear exactly
// once, and the generator refuses to write anything if that stops being true.
const (
	sampleKeyID  = "local-test"
	sampleSecret = "dev-only-change-me-0123456789abcdef"
)

var sampleKeyLine = regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(sampleKeyID) + `:[ \t]*$`)

// render produces the finished config text.
//
// Substitution over the sample rather than emission from scratch, so the
// sample's comments survive into the operator's real config. Every needle is
// required to match exactly once; anything else aborts the whole install rather
// than writing a file that is quietly missing a setting.
func render(source string, s *Settings, secret string) (string, error) {
	out, err := replaceBanner(source)
	if err != nil {
		return "", err
	}

	if n := len(sampleKeyLine.FindAllString(out, -1)); n != 1 {
		return "", fmt.Errorf("the sample key id %q appears %d times in config.example.yaml, expected exactly once", sampleKeyID, n)
	}
	out = sampleKeyLine.ReplaceAllString(out, "${1}"+s.KeyID+":")

	if n := strings.Count(out, sampleSecret); n != 1 {
		return "", fmt.Errorf("the sample secret appears %d times in config.example.yaml, expected exactly once", n)
	}
	out = strings.Replace(out, sampleSecret, secret, 1)

	rewrites := []struct {
		key     string
		value   string
		comment string
	}{
		{"open_upload", strconv.FormatBool(s.OpenUpload), ""},
		{"open_upload_quota_bytes_per_day", strconv.FormatInt(s.OpenUploadQuotaBytesPerDay, 10), describeBytes(s.OpenUploadQuotaBytesPerDay)},
		{"quota_bytes_per_day", strconv.FormatInt(s.QuotaBytesPerDay, 10), describeBytes(s.QuotaBytesPerDay)},
		{"min_free_bytes", strconv.FormatInt(s.MinFreeBytes, 10), describeBytes(s.MinFreeBytes)},
		{"default_ttl", formatHours(s.DefaultTTL), describeHours(s.DefaultTTL)},
		{"max_ttl", formatHours(s.MaxTTL), describeHours(s.MaxTTL)},
		{"public_base_url", strconv.Quote(s.PublicBaseURL), ""},
	}

	for _, r := range rewrites {
		out, err = rewriteSetting(out, r.key, r.value, r.comment)
		if err != nil {
			return "", err
		}
	}

	// The shipped secret must not survive anywhere, comments included.
	if strings.Contains(out, sampleSecret) {
		return "", fmt.Errorf("the sample secret is still present in the generated config")
	}

	return out, nil
}

// rewriteSetting replaces one "key: value" line, keeping its indentation and
// rebuilding its trailing comment — which would otherwise still read "48 hours"
// next to a number that no longer means 48 hours.
//
// Anchored at the start of a line so the commented-out examples and the prose
// above them are never touched, and required to match exactly once.
func rewriteSetting(source, key, value, comment string) (string, error) {
	pattern := regexp.MustCompile(`(?m)^([ \t]*)` + regexp.QuoteMeta(key) + `:[^\n]*$`)

	if n := len(pattern.FindAllString(source, -1)); n != 1 {
		return "", fmt.Errorf("the setting %q appears %d times in config.example.yaml, expected exactly once", key, n)
	}

	replacement := "${1}" + key + ": " + value
	if comment != "" {
		replacement += "   # " + comment
	}

	return pattern.ReplaceAllString(source, replacement), nil
}

// replaceBanner swaps the sample's "copy me" header for one describing what the
// generated file is.
//
// The banner region is everything before the first line that is neither blank
// nor a comment — simpler and harder to break than matching a delimiter pair.
func replaceBanner(source string) (string, error) {
	lines := strings.Split(source, "\n")

	body := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		body = i
		break
	}

	if body < 0 {
		return "", fmt.Errorf("config.example.yaml has no settings in it")
	}

	banner := []string{
		"# ---------------------------------------------------------------------------",
		"# eMule HTTP Cache — configuration.",
		"#",
		"# Written by the installer on " + time.Now().UTC().Format(time.RFC3339) + ". Everything here is",
		"# safe to edit by hand; the comments below explain each setting.",
		"#",
		"# This file contains an API secret. Keep it out of version control — .gitignore",
		"# already covers it — and out of any backup you would hand to someone else.",
		"#",
		"# To start over with a fresh key: delete this file and open /install again.",
		"# ---------------------------------------------------------------------------",
		"",
	}

	return strings.Join(append(banner, lines[body:]...), "\n"), nil
}

// formatHours renders a whole number of hours the way the sample writes one.
func formatHours(d time.Duration) string {
	return strconv.Itoa(int(d.Hours())) + "h"
}

func describeHours(d time.Duration) string {
	h := int(d.Hours())
	if h >= 24 && h%24 == 0 {
		return fmt.Sprintf("%d hours (%d days)", h, h/24)
	}

	return fmt.Sprintf("%d hours", h)
}
