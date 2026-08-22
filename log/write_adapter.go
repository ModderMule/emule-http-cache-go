package log

import (
	"io"
	"strings"
)

// ensure we always implement io.Writer (compile error otherwise)
var _ io.Writer = (*Writer)(nil)

// Writer adapts the Logger to io.Writer so libraries that write log lines to an
// io.Writer (gin, the stdlib log package, http.Server.ErrorLog, ...) route
// through our structured logger. Lines whose text starts with one of Filter is
// silently dropped.
type Writer struct {
	// config
	Filter []string // line prefixes to ignore (see log.New in the stdlib log package)

	// internals
	log Logger
}

func (w *Writer) Write(p []byte) (n int, err error) {
	line := string(p)
	for _, filter := range w.Filter {
		if strings.HasPrefix(line, filter) {
			return len(p), nil // filtered: report as written, but don't log
		}
	}
	// Info, not Debug. Everything reaching this writer is gin's panic recovery
	// or http.Server.ErrorLog — the two things an operator most needs to see —
	// and the default level is info, which would drop them silently.
	if w.log != nil {
		w.log.Infof("%s", strings.TrimRight(line, "\n"))
	}
	return len(p), nil
}
