package cli

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestActorVerbsEmitActorDocSchema pins one snake_case actor document on every
// actor verb, so a parser written against `actor create` also reads
// `actor list`. Before the fix both emitted the core actor's Go field names.
func TestActorVerbsEmitActorDocSchema(t *testing.T) {
	r := newRunner(t)

	created := jsonObject(t, r.run("actor", "create", "claude", "-k", "agent", "-o", "json"))
	assertKeySet(t, "actor create", created, "id", "kind", "name", "active", "created_at")
	if got := created["id"]; got != "claude" {
		t.Errorf("actor create -o json id = %v, want claude", got)
	}
	if got := created["kind"]; got != "agent" {
		t.Errorf("actor create -o json kind = %v, want agent", got)
	}
	if got := created["name"]; got != "claude" {
		t.Errorf("actor create -o json name = %v, want claude", got)
	}

	entries := jsonObjects(t, r.run("actor", "list", "-o", "json"))
	for _, entry := range entries {
		assertKeySet(t, "actor list", entry, "id", "kind", "name", "active", "created_at")
	}
}

// TestProjectVerbsEmitProjectDocSchema pins the snake_case project document on
// every project verb, including its nested repository documents.
func TestProjectVerbsEmitProjectDocSchema(t *testing.T) {
	r := newRunner(t)

	projectKeys := []string{"id", "name", "description", "repos", "policy", "created_at", "updated_at"}
	created := jsonObject(t, r.run("project", "create", "Acme", "--description", "the wire API", "-o", "json"))
	assertKeySet(t, "project create", created, projectKeys...)
	if got := created["id"]; got != "acme" {
		t.Errorf("project create -o json id = %v, want acme", got)
	}

	get := jsonObject(t, r.run("project", "get", "acme", "-o", "json"))
	assertKeySet(t, "project get", get, projectKeys...)

	listed := jsonObjects(t, r.run("project", "list", "-o", "json"))
	for _, entry := range listed {
		assertKeySet(t, "project list", entry, projectKeys...)
	}

	added := jsonObject(t, r.run("project", "repo", "create", "acme", "api",
		"--url", "git@github.com:acme/api.git", "--path", "repos/api", "--brief", "the API", "-o", "json"))
	assertKeySet(t, "project repo create", added, projectKeys...)
	repos, ok := added["repos"].([]any)
	if !ok || len(repos) != 1 {
		t.Fatalf("project repo create -o json repos = %v, want one repository", added["repos"])
	}
	repo, ok := repos[0].(map[string]any)
	if !ok {
		t.Fatalf("project repo create -o json repo = %v, want an object", repos[0])
	}
	assertKeySet(t, "project repo create", repo, "name", "url", "path", "description")
	if got := repo["name"]; got != "api" {
		t.Errorf("project repo create -o json repo name = %v, want api", got)
	}

	updated := jsonObject(t, r.run("project", "repo", "update", "acme", "api", "--brief", "the wire API", "-o", "json"))
	assertKeySet(t, "project repo update", updated, projectKeys...)

	repoEntries := jsonObjects(t, r.run("project", "repo", "list", "acme", "-o", "json"))
	for _, entry := range repoEntries {
		assertKeySet(t, "project repo list", entry, "name", "url", "path", "description")
	}

	repoRemoved := jsonObject(t, r.run("project", "repo", "delete", "acme", "api", "-o", "json"))
	assertKeySet(t, "project repo delete", repoRemoved, projectKeys...)

	removed := jsonObject(t, r.run("project", "delete", "acme", "-o", "json"))
	assertKeySet(t, "project delete", removed, projectKeys...)
	if got := removed["id"]; got != "acme" {
		t.Errorf("project delete -o json id = %v, want acme", got)
	}
}

// TestEventListEmitsEventDocSchema pins the snake_case event document, whose
// task_id field is deliberately spelled out rather than inheriting the stored
// document's Go-name key.
func TestEventListEmitsEventDocSchema(t *testing.T) {
	r := newRunner(t)
	r.run("project", "create", "Acme")
	taskID := firstField(t, r.run("task", "create", "-p", "acme", "-t", "First"))

	entries := jsonObjects(t, r.run("event", "list", "-p", "acme", "-o", "json"))
	docKeys := jsonTagNames(reflect.TypeOf(eventDoc{}))
	var sawTask bool
	for _, entry := range entries {
		for key := range entry {
			if !docKeys[key] {
				t.Errorf("event list -o json emitted %q, which is not an eventDoc key", key)
			}
		}
		if entry["task_id"] == taskID {
			sawTask = true
		}
	}
	if !sawTask {
		t.Errorf("event list -o json carried no event with task_id %s:\n%v", taskID, entries)
	}
}

// TestTaskActionVerbsEmitTaskDocSchema folds the remaining action results into
// the one task document: note create, assign, and delete used to print
// unstructured text under -o json.
func TestTaskActionVerbsEmitTaskDocSchema(t *testing.T) {
	r := newRunner(t)
	r.run("project", "create", "Acme")
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("task", "create", "-p", "acme", "-t", "First"))

	docKeys := jsonTagNames(reflect.TypeOf(taskDoc{}))

	noted := jsonObject(t, r.run("task", "note", "create", taskID, "-b", "a decision", "-o", "json"))
	assertTaskDocKeys(t, "task note create", noted, docKeys)
	if notes, ok := noted["notes"].([]any); !ok || len(notes) != 1 {
		t.Errorf("task note create -o json notes = %v, want the new note", noted["notes"])
	}

	assigned := jsonObject(t, r.run("task", "assign", taskID, "--actor", "claude", "-o", "json"))
	assertTaskDocKeys(t, "task assign", assigned, docKeys)
	if got := assigned["assignee"]; got != "claude" {
		t.Errorf("task assign -o json assignee = %v, want claude", got)
	}

	deleted := jsonObject(t, r.run("task", "delete", taskID, "-o", "json"))
	assertTaskDocKeys(t, "task delete", deleted, docKeys)
	if got := deleted["id"]; got != taskID {
		t.Errorf("task delete -o json id = %v, want %s", got, taskID)
	}
}

// assertTaskDocKeys requires every key to belong to the task document, at any
// depth.
func assertTaskDocKeys(t *testing.T, verb string, doc map[string]any, docKeys map[string]bool) {
	t.Helper()
	if len(doc) == 0 {
		t.Fatalf("%s -o json emitted no document", verb)
	}
	assertSnakeCaseKeys(t, verb, doc)
	for key := range doc {
		if !docKeys[key] {
			t.Errorf("%s -o json emitted %q, which is not a taskDoc key", verb, key)
		}
	}
}

// assertKeySet fails when a document's key set differs from want, so a leaked
// Go field name and a missing snake_case field both fail loudly.
func assertKeySet(t *testing.T, verb string, doc map[string]any, want ...string) {
	t.Helper()
	assertSnakeCaseKeys(t, verb, doc)
	got := make([]string, 0, len(doc))
	for key := range doc {
		got = append(got, key)
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("%s -o json keys = %v, want %v", verb, got, want)
	}
}

// jsonObjects decodes one JSON array of objects.
func jsonObjects(t *testing.T, out string) []map[string]any {
	t.Helper()
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("decode json array: %v\n%s", err, out)
	}
	if len(entries) == 0 {
		t.Fatalf("expected at least one entry:\n%s", out)
	}
	return entries
}
