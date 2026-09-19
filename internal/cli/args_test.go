package cli

import (
	"strings"
	"testing"
)

func TestWrongArgCountPrintsHelp(t *testing.T) {
	r := newRunner(t)
	stdout, stderr := r.runSplit("task", "assign")

	if strings.Contains(stderr, "accepts 1 arg") {
		t.Fatalf("arg error should not be terse:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Usage:") || !strings.Contains(stderr, "ft task assign <task>") {
		t.Fatalf("expected assign help on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "expected 1 argument(s), got 0") {
		t.Fatalf("expected the reason alongside the help:\n%s", stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Fatalf("help must go to stderr, stdout =\n%s", stdout)
	}
}

func TestMissingRequiredFlagPrintsHelp(t *testing.T) {
	r := newRunner(t)
	_, stderr := r.runSplit("task", "create", "--project", "acme")

	if strings.Contains(stderr, "required flag") {
		t.Fatalf("required-flag error should not be terse:\n%s", stderr)
	}
	if !strings.Contains(stderr, "Usage:") || !strings.Contains(stderr, "ft task create") {
		t.Fatalf("expected create help on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "title required") {
		t.Fatalf("expected the missing flag named alongside the help:\n%s", stderr)
	}
}
