package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// runsJSON decodes `ft msg runs -o json` into the command's list shape.
func runsJSON(t *testing.T, r *runner, args ...string) []messageRunEntry {
	t.Helper()
	out := r.run(append(args, "-o", "json")...)
	var entries []messageRunEntry
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decode runs json %q: %v", out, err)
	}
	return entries
}

// TestMessageRunsListsRegisteredRuns guards the documented liveness check the
// role skills rely on: `ft msg runs` shows a registered receiver by run id and
// actor, so an agent can confirm its own channel is live.
func TestMessageRunsListsRegisteredRuns(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	actorID := firstField(t, r.run("actor", "create", "builder-t-1", "--kind", "agent"))

	registered := runJSON(t, r, "msg", "agent", "register", "--actor", actorID,
		"--harness", "opencode", "--host", "host-a", "--pid", "42", "--project", projectID)
	runID, _ := registered["run_id"].(string)
	if runID == "" {
		t.Fatalf("register json missing run_id: %v", registered)
	}

	entries := runsJSON(t, r, "msg", "runs", "--project", projectID)
	if len(entries) != 1 {
		t.Fatalf("runs = %+v, want the one registered run", entries)
	}
	entry := entries[0]
	if entry.ID != runID || entry.Actor != actorID || entry.Project != projectID {
		t.Fatalf("run entry = %+v, want id %s actor %s project %s", entry, runID, actorID, projectID)
	}
	if !entry.Live {
		t.Fatalf("run entry Live = false, want a fresh registration live: %+v", entry)
	}
	if entry.Harness != "opencode" || entry.Host != "host-a" || entry.PID != 42 {
		t.Fatalf("run entry = %+v, want the registered harness/host/pid", entry)
	}

	table := r.run("msg", "runs", "--project", projectID)
	for _, want := range []string{"ID", "ACTOR", "LIVE", "PROJECT", runID, actorID, projectID} {
		if !strings.Contains(table, want) {
			t.Fatalf("runs table missing %q:\n%s", want, table)
		}
	}
}

// TestMessageRunsListsEveryProjectWithoutADefault guards the list-command
// contract: with no -p and no configured default, a list command falls back to
// all projects rather than erroring like a mutating one.
func TestMessageRunsListsEveryProjectWithoutADefault(t *testing.T) {
	r := newRunner(t)
	first := firstField(t, r.run("project", "create", "Acme"))
	second := firstField(t, r.run("project", "create", "Beta"))
	runJSON(t, r, "msg", "agent", "register", "--actor", "builder-t-1", "--pid", "1", "--project", first)
	runJSON(t, r, "msg", "agent", "register", "--actor", "builder-t-2", "--pid", "2", "--project", second)

	entries := runsJSON(t, r, "msg", "runs")
	seen := map[string]bool{}
	for _, entry := range entries {
		seen[entry.Project] = true
	}
	if !seen[first] || !seen[second] {
		t.Fatalf("runs = %+v, want both projects %s and %s", entries, first, second)
	}
}

// TestMessageRunsLiveFiltersExpiredRuns guards --live: it keeps only runs whose
// lease is valid, which is what tells an agent its receiver is actually
// running rather than a dead registration left behind.
func TestMessageRunsLiveFiltersExpiredRuns(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	live := runJSON(t, r, "msg", "agent", "register", "--actor", "builder-t-1",
		"--pid", "1", "--ttl", "1h", "--project", projectID)
	// A 1ns lease is already past by the time the next command reads it, so the
	// registration stays in the list but is not live.
	runJSON(t, r, "msg", "agent", "register", "--actor", "builder-t-2",
		"--pid", "2", "--ttl", "1ns", "--project", projectID)

	if all := runsJSON(t, r, "msg", "runs", "--project", projectID); len(all) != 2 {
		t.Fatalf("runs = %+v, want both registrations without --live", all)
	}
	liveEntries := runsJSON(t, r, "msg", "runs", "--live", "--project", projectID)
	if len(liveEntries) != 1 {
		t.Fatalf("runs --live = %+v, want only the leased run", liveEntries)
	}
	liveID, _ := live["run_id"].(string)
	if liveEntries[0].ID != liveID {
		t.Fatalf("runs --live = %+v, want the live run %s", liveEntries, liveID)
	}
}
