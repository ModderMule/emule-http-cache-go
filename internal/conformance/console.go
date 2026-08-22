package conformance

import (
	"fmt"
	"io"
	"os"
)

// Console reports to a terminal, for the `conformance` subcommand.
//
// Verbose is off by default: the per-request trace is what `go test -v` wants,
// not what an operator running a one-line check wants.
type Console struct {
	Out     io.Writer
	Colour  bool
	Verbose bool
}

// NewConsole builds a reporter over stdout, colouring only on a terminal.
func NewConsole(verbose bool) *Console {
	info, err := os.Stdout.Stat()
	colour := err == nil && info.Mode()&os.ModeCharDevice != 0

	return &Console{Out: os.Stdout, Colour: colour, Verbose: verbose}
}

func (c *Console) Section(title string) {
	fmt.Fprintf(c.Out, "\n%s\n", title)
}

func (c *Console) Pass(label string) {
	fmt.Fprintf(c.Out, "  %s   %s\n", c.paint("ok", "32"), label)
}

func (c *Console) Fail(label, detail string) {
	if detail != "" {
		label = label + " (" + detail + ")"
	}
	fmt.Fprintf(c.Out, "  %s %s\n", c.paint("FAIL", "31"), label)
}

func (c *Console) Skip(label, why string) {
	fmt.Fprintf(c.Out, "  %s %s (%s)\n", c.paint("skip", "33"), label, why)
}

func (c *Console) Logf(format string, args ...any) {
	if !c.Verbose {
		return
	}
	fmt.Fprintf(c.Out, "       "+format+"\n", args...)
}

// Summary prints the tally.
func (c *Console) Summary(r Result) {
	fmt.Fprintf(c.Out, "\n%d passed, %d failed\n\n", r.Passed, r.Failed)
}

// -- internals ---------------------------------------------------------------

func (c *Console) paint(text, code string) string {
	if !c.Colour {
		return text
	}

	return "\033[" + code + "m" + text + "\033[0m"
}
