package cli

import (
	"bytes"
	"strings"
	"testing"
)

// countingWriter records how many Write calls reach the underlying writer, so a
// test can prove the table renderer coalesces output. text/tabwriter writes once
// per cell, which becomes a syscall per cell when the destination is a file or
// pipe; buffering the destination is what keeps large lists fast.
type countingWriter struct {
	writes int
	buf    bytes.Buffer
}

func (c *countingWriter) Write(p []byte) (int, error) {
	c.writes++
	return c.buf.Write(p)
}

func TestPrintTableBuffersOutput(t *testing.T) {
	const rows = 1000
	deps := &Deps{Out: &countingWriter{}}
	rec := deps.Out.(*countingWriter)

	table := make([][]string, 0, rows)
	for i := 0; i < rows; i++ {
		table = append(table, []string{"t-000000", "task", "task 0", "todo", "perf", "-"})
	}
	deps.printTable([]string{"ID", "KIND", "TITLE", "STATUS", "PROJECT", "REPO"}, table)

	// Unbuffered, tabwriter issues roughly six writes per row (one per cell).
	// Buffered, the whole table is a handful of writes.
	if rec.writes > 16 {
		t.Fatalf("printTable issued %d writes for %d rows; want buffered output", rec.writes, rows)
	}
	got := rec.buf.String()
	first, _, _ := strings.Cut(got, "\n")
	if fields := strings.Join(strings.Fields(first), " "); fields != "ID KIND TITLE STATUS PROJECT REPO" {
		t.Fatalf("table header = %q, want the six columns", first)
	}
	if lines := strings.Count(got, "\n"); lines != rows+1 {
		t.Fatalf("table has %d lines, want %d", lines, rows+1)
	}
}
