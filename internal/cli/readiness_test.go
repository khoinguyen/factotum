package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTaskGetShowsNotReadyReason(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--dep", blocker))

	out := r.run("task", "get", taskID)
	want := "not ready because: dep_unresolved: " + blocker
	if !strings.Contains(out, want) {
		t.Fatalf("task get missing %q:\n%s", want, out)
	}

	ready := r.run("task", "get", blocker)
	if strings.Contains(ready, "not ready because") {
		t.Fatalf("ready task must not carry a not-ready reason:\n%s", ready)
	}
}

func TestTaskGetNotReadyReasons(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	makeTask := func(title string) string {
		return firstField(t, r.run("task", "create", "-p", projectID, "-t", title))
	}

	blocker := makeTask("blocker")
	dependent := makeTask("dependent")
	r.run("task", "dep", "create", dependent, blocker)

	blocked := makeTask("blocked")
	r.run("task", "block", blocked)

	doing := makeTask("doing")
	r.run("task", "start", doing)

	snoozed := makeTask("snoozed")
	r.run("task", "snooze", snoozed, "--indefinite")

	deferred := makeTask("deferred")
	r.run("task", "set", deferred, "not_before=2030-01-01")

	tests := []struct {
		name string
		id   string
		want string
	}{
		{"dep unresolved", dependent, "not ready because: dep_unresolved: " + blocker},
		{"blocked", blocked, "not ready because: blocked"},
		{"in progress", doing, "not ready because: in_progress"},
		{"snoozed", snoozed, "not ready because: snoozed: indefinitely"},
		{"not before", deferred, "not ready because: not_before: 2030-01-01T00:00:00Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := r.run("task", "get", tt.id)
			if !strings.Contains(out, tt.want) {
				t.Fatalf("task get missing %q:\n%s", tt.want, out)
			}
		})
	}
}

func TestTaskGetJSONCarriesNotReadyReason(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--dep", blocker))

	out := r.run("task", "get", taskID, "-o", "json")
	var doc struct {
		NotReady *struct {
			ReasonCode string `json:"reason_code"`
			Detail     string `json:"detail"`
		} `json:"not_ready"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("task get json: %v\n%s", err, out)
	}
	if doc.NotReady == nil {
		t.Fatalf("task get json missing not_ready:\n%s", out)
	}
	if doc.NotReady.ReasonCode != "dep_unresolved" || doc.NotReady.Detail != blocker {
		t.Fatalf("not_ready = %+v, want dep_unresolved/%s", doc.NotReady, blocker)
	}
}

func TestTaskNextExplainListsExcludedTasks(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ready := firstField(t, r.run("task", "create", "-p", projectID, "-t", "ready"))
	blocked := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocked"))
	r.run("task", "block", blocked)

	out := r.run("task", "next", "-p", projectID, "--explain")
	if !strings.Contains(out, blocked) || !strings.Contains(out, "blocked") {
		t.Fatalf("explain missing the excluded task and its reason:\n%s", out)
	}
	if strings.Contains(out, ready) {
		t.Fatalf("explain should list only excluded tasks, got the ready one too:\n%s", out)
	}
}

func TestTaskNextExplainJSONReasons(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	blocker := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))
	dependent := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work", "--dep", blocker))

	out := r.run("task", "next", "-p", projectID, "--explain", "-o", "json")
	var entries []struct {
		TaskID     string `json:"task_id"`
		Status     string `json:"status"`
		ReasonCode string `json:"reason_code"`
		Detail     string `json:"detail"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("task next --explain json: %v\n%s", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want the single excluded task", entries)
	}
	got := entries[0]
	if got.TaskID != dependent || got.ReasonCode != "dep_unresolved" || got.Detail != blocker {
		t.Fatalf("entry = %+v, want %s dep_unresolved/%s", got, dependent, blocker)
	}
}

func TestTaskNextExplainRejectsProjectWithAll(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	if err := r.runErr("task", "next", "--all", "-p", projectID, "--explain"); err == nil {
		t.Fatal("expected --all with --project to fail")
	}
}

func TestTaskNextExplainHonorsLabelFilter(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	tagged := firstField(t, r.run("task", "create", "-p", projectID, "-t", "tagged", "--label", "ops"))
	plain := firstField(t, r.run("task", "create", "-p", projectID, "-t", "plain"))
	r.run("task", "block", tagged)
	r.run("task", "block", plain)

	out := r.run("task", "next", "-p", projectID, "--explain", "--label", "ops")
	if !strings.Contains(out, tagged) {
		t.Fatalf("explain --label kept no tagged task:\n%s", out)
	}
	if strings.Contains(out, plain) {
		t.Fatalf("explain --label leaked an unlabeled task:\n%s", out)
	}
}

func TestTaskNextExplainHonorsRepoFilter(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "backend")
	inRepo := firstField(t, r.run("task", "create", "-p", projectID, "-t", "in-repo", "--repo", "backend"))
	other := firstField(t, r.run("task", "create", "-p", projectID, "-t", "other"))
	r.run("task", "block", inRepo)
	r.run("task", "block", other)

	out := r.run("task", "next", "-p", projectID, "--explain", "--repo", "backend")
	if !strings.Contains(out, inRepo) {
		t.Fatalf("explain --repo kept no matching task:\n%s", out)
	}
	if strings.Contains(out, other) {
		t.Fatalf("explain --repo leaked a task from another repo:\n%s", out)
	}
}

func TestTaskNextExplainHonorsActorFilter(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "create", "--kind", "agent", "claude")
	r.run("actor", "create", "--kind", "agent", "other")
	mine := firstField(t, r.run("task", "create", "-p", projectID, "-t", "mine"))
	r.run("task", "assign", mine, "--actor", "claude")
	theirs := firstField(t, r.run("task", "create", "-p", projectID, "-t", "theirs"))
	r.run("task", "assign", theirs, "--actor", "other")
	r.run("task", "block", mine)
	r.run("task", "block", theirs)

	out := r.run("task", "next", "-p", projectID, "--explain", "--for", "claude")
	if !strings.Contains(out, mine) {
		t.Fatalf("explain --for kept no task for the actor:\n%s", out)
	}
	if strings.Contains(out, theirs) {
		t.Fatalf("explain --for leaked a task assigned to another actor:\n%s", out)
	}
}

func TestTaskNextExplainHonorsLimitInJSON(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	for _, title := range []string{"a", "b", "c"} {
		id := firstField(t, r.run("task", "create", "-p", projectID, "-t", title))
		r.run("task", "block", id)
	}

	out := r.run("task", "next", "-p", projectID, "--explain", "-n", "1", "-o", "json")
	var entries []struct {
		TaskID string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("task next --explain json: %v\n%s", err, out)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %+v, want the limit to keep exactly one", entries)
	}
}
