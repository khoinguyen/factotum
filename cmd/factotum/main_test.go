package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunUsageErrorExitsNonZeroWithHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"task", "assign"}, &stdout, &stderr, func(string) string { return "" })
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if strings.Contains(stderr.String(), "accepts 1 arg") {
		t.Fatalf("arg error should not be terse:\n%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "Usage:") {
		t.Fatalf("expected help on stderr:\n%s", stderr.String())
	}
}
