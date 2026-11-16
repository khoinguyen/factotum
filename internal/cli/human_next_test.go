package cli

import (
	"strings"
	"testing"
)

// Khoi 2026-10-14: "human next should show up as next, because it is block the
// agent". A human-next item is ready work in its own right, so the default
// ranking already lists it and never lists the agent work it gates. Asking for
// the gated agent's ranking must surface that same blocker, so the agent sees the
// human decision standing between it and its work; an unrelated human-next item
// is still not the agent's to pick up.
func TestTaskNextSurfacesTheHumanBlocker(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	projectID := firstField(t, r.run("project", "create", "Acme"))
	human := firstField(t, r.run("task", "create", "-p", projectID, "-t", "decide the approach", "--groomed", "--acceptance", "decided"))
	unrelated := firstField(t, r.run("task", "create", "-p", projectID, "-t", "unrelated human work", "--groomed", "--acceptance", "done"))
	gated := firstField(t, r.run("task", "create", "-p", projectID, "-t", "build it", "--groomed", "--acceptance", "works", "--dep", human))
	r.run("task", "assign", gated, "--actor", "builder-t-1")

	base := r.run("task", "next", "-p", projectID)
	if !strings.Contains(base, human) {
		t.Fatalf("task next missing the human-next blocker %s:\n%s", human, base)
	}
	if strings.Contains(base, gated) {
		t.Fatalf("task next offered the gated task %s:\n%s", gated, base)
	}

	forAgent := r.run("task", "next", "-p", projectID, "--for", "builder-t-1")
	if !strings.Contains(forAgent, human) {
		t.Fatalf("task next --for agent missing the blocking human-next %s:\n%s", human, forAgent)
	}
	if strings.Contains(forAgent, gated) {
		t.Fatalf("task next --for agent offered the gated task %s:\n%s", gated, forAgent)
	}
	if strings.Contains(forAgent, unrelated) {
		t.Fatalf("task next --for agent listed unrelated human work %s:\n%s", unrelated, forAgent)
	}
}
