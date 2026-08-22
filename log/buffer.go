package log

import (
	"io"
	"sync"
)

// ensure we always implement io.WriteCloser (compile error otherwise)
var _ io.WriteCloser = (*Buffer)(nil)

// Buffer keeps the most recent log lines in memory. It can be used to serve the
// recent logs to an admin interface (HTTP, gRPC, ...). It is safe for
// concurrent writes.
type Buffer struct {
	// config
	MaxSize   int // max number of stored lines before trimming
	CleanSize int // after reaching MaxSize, reduce stored lines to this count

	// internals
	logList   []BufferLogLine
	mu        sync.Mutex
	lineCount int64 // line number of the latest stored line
}

// BufferLogLine is one stored log line with its sequence number.
type BufferLogLine struct {
	Nr   int64  `json:"nr"`
	Line string `json:"line"`
}

func (b *Buffer) Write(p []byte) (n int, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.lineCount++
	b.logList = append(b.logList, BufferLogLine{
		Nr:   b.lineCount,
		Line: string(p),
	})

	// Trim to the most recent CleanSize lines once we hit MaxSize.
	if b.MaxSize > 0 && len(b.logList) >= b.MaxSize && b.CleanSize > 0 {
		tail := b.logList[len(b.logList)-b.CleanSize:]
		next := make([]BufferLogLine, len(tail))
		copy(next, tail)
		b.logList = next
	}

	return len(p), nil
}

func (b *Buffer) Close() error { return nil }

// GetLines returns a snapshot of the currently buffered lines.
func (b *Buffer) GetLines() []BufferLogLine {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]BufferLogLine, len(b.logList))
	copy(out, b.logList)
	return out
}
