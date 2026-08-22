// Package conformance is the portable contract test for an eMule HTTP Cache
// backend.
//
// It speaks nothing but HTTP, so it is equally valid against a Go, PHP, Rust or
// object-store implementation: point it at any of them and it must pass
// unchanged. That is the whole reason it is a library with no testing import —
// `go test` drives it through one reporter, and the `conformance` subcommand
// drives it through another for an operator with no Go toolchain.
package conformance

// Reporter receives everything the suite observes.
//
// The test reporter forwards Logf to t.Logf, which is what gives the project's
// "log program input and output" rule to all 31 assertions at once: the traced
// RoundTripper reports every request and response through here, so no
// assertion has to remember to log anything itself.
type Reporter interface {
	Section(title string)
	Pass(label string)
	Fail(label, detail string)
	Skip(label, why string)
	Logf(format string, args ...any)
}

// Result is the tally a run produces.
type Result struct {
	Passed int
	Failed int
}

// OK reports whether every assertion passed.
func (r Result) OK() bool { return r.Failed == 0 }
