package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestTaskGetDistinguishesCaptureEdgesFromDependencies proves a task promoted
// from an idea and grouped under a second idea shows those capture edges as
// non-blocking provenance, not as "Depends on" edges; only the task dependency
// is a blocking dep.
func TestTaskGetDistinguishesCaptureEdgesFromDependencies(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "claude", "-k", "agent")

	origin := firstField(t, r.run("task", "create", "-p", projectID, "-k", "idea", "-t", "origin idea"))
	umbrella := firstField(t, r.run("task", "create", "-p", projectID, "-k", "idea", "-t", "umbrella idea"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	taskID := firstField(t, r.run("task", "promote", origin, "--actor", "claude", "--acceptance", "it works"))
	r.run("task", "dep", "create", taskID, umbrella)
	r.run("task", "dep", "create", taskID, blocker)

	got := r.run("task", "get", taskID)
	deps := fieldLine(got, "deps")
	if !strings.Contains(deps, blocker) {
		t.Fatalf("deps should list the blocking task %s:\n%s", blocker, got)
	}
	if strings.Contains(deps, origin) || strings.Contains(deps, umbrella) {
		t.Fatalf("deps must not list capture edges (%s, %s):\n%s", origin, umbrella, got)
	}
	if !strings.Contains(got, "origin: "+origin+" ") {
		t.Fatalf("task get should show the origin idea:\n%s", got)
	}
	if grouped := fieldLine(got, "grouped_under"); !strings.Contains(grouped, umbrella) {
		t.Fatalf("grouped_under should list the umbrella idea %s:\n%s", umbrella, got)
	} else if strings.Contains(grouped, origin) {
		t.Fatalf("grouped_under must not repeat the origin %s:\n%s", origin, got)
	}

	var doc struct {
		Deps         []string `json:"deps"`
		GroupedUnder []string `json:"grouped_under"`
		Origin       string   `json:"origin"`
	}
	if err := json.Unmarshal([]byte(r.run("task", "get", taskID, "-o", "json")), &doc); err != nil {
		t.Fatalf("task get -o json: %v", err)
	}
	if len(doc.Deps) != 1 || doc.Deps[0] != blocker {
		t.Fatalf("json deps = %v, want [%s]", doc.Deps, blocker)
	}
	if len(doc.GroupedUnder) != 1 || doc.GroupedUnder[0] != umbrella {
		t.Fatalf("json grouped_under = %v, want [%s]", doc.GroupedUnder, umbrella)
	}
	if doc.Origin != origin {
		t.Fatalf("json origin = %q, want %s", doc.Origin, origin)
	}
}

// TestTaskGetCrossProjectCaptureIsOrigin pins that ft task get resolves a
// capture dep in another project as the task's origin, not as a grouping edge,
// so it agrees with the serve /api/task detail on the same task. Provenance does
// not depend on the reader's project scope.
func TestTaskGetCrossProjectCaptureIsOrigin(t *testing.T) {
	r := newRunner(t)
	mineID := firstField(t, r.run("project", "create", "Acme"))
	otherID := firstField(t, r.run("project", "create", "Other"))

	foreign := firstField(t, r.run("task", "create", "-p", otherID, "-k", "idea", "-t", "foreign idea"))
	taskID := firstField(t, r.run("task", "create", "-p", mineID, "-t", "task in mine"))
	r.run("task", "dep", "create", taskID, foreign)

	got := r.run("task", "get", taskID)
	if !strings.Contains(got, "origin: "+foreign+" ") {
		t.Fatalf("task get should show the cross-project origin idea:\n%s", got)
	}
	if strings.Contains(fieldLine(got, "grouped_under"), foreign) {
		t.Fatalf("grouped_under must not repeat the origin:\n%s", got)
	}
}

// fieldLine returns the value of the first "key: value" line, or "" when the
// line is absent.
func fieldLine(out, key string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, key+":") {
			return strings.TrimSpace(strings.TrimPrefix(line, key+":"))
		}
	}
	return ""
}
