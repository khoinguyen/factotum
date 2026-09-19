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
	if !strings.Contains(stderr.String(), "expected 1 argument(s), got 0") {
		t.Fatalf("expected the reason alongside the help:\n%s", stderr.String())
	}
}

func TestRunNotfoundExitCodeAndJSONEnvelope(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run([]string{"task", "get", "t-nope"}, &stdout, &stderr, func(string) string { return "" })
	if code != 3 {
		t.Fatalf("exit code = %d, want 3\nstderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "not found") {
		t.Fatalf("expected a not-found message:\n%s", stderr.String())
	}

	stdout.Reset()
	stderr.Reset()
	code = run([]string{"task", "get", "t-nope", "-o", "json"}, &stdout, &stderr, func(string) string { return "" })
	if code != 3 {
		t.Fatalf("json exit code = %d, want 3", code)
	}
	if !strings.Contains(stderr.String(), `"code":"not_found"`) {
		t.Fatalf("expected a json error envelope:\n%s", stderr.String())
	}
}
