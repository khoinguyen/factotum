package cli

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/core"
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

// TestEventListEmitsEventDocSchema pins the exact snake_case event document for
// both a minimal event (no link, actor, session, data, or tally) and a populated
// one, so a dropped field or a wrong omitempty fails.
func TestEventListEmitsEventDocSchema(t *testing.T) {
	r := newRunner(t)
	caller := ""
	r.getenv = func(key string) string {
		switch key {
		case "FACTOTUM_CALLER":
			return caller
		case "FACTOTUM_SESSION":
			if caller != "" {
				return "sess-1"
			}
		}
		return ""
	}
	r.run("project", "create", "Acme")
	r.run("actor", "create", "claude", "-k", "agent")

	minimal := jsonObjects(t, r.run("event", "list", "-p", "acme", "-n", "1", "-o", "json"))
	assertKeySet(t, "event list (minimal)", minimal[0], "id", "project_id", "kind", "summary", "created_at")

	caller = "claude"
	blocker := firstField(t, r.run("task", "create", "-p", "acme", "-t", "Blocker"))
	dependent := firstField(t, r.run("task", "create", "-p", "acme", "-t", "Dependent"))
	r.run("task", "dep", "create", dependent, blocker)

	populated := jsonObjects(t, r.run("event", "list", "-p", "acme", "-n", "1", "-o", "json"))
	event := populated[0]
	assertKeySet(t, "event list (populated)", event,
		"id", "project_id", "task_id", "kind", "by", "session", "summary", "data", "created_at")
	if got := event["by"]; got != "claude" {
		t.Errorf("event list -o json by = %v, want claude", got)
	}
	if got := event["session"]; got != "sess-1" {
		t.Errorf("event list -o json session = %v, want sess-1", got)
	}
	if data, ok := event["data"].(map[string]any); !ok || data["dep"] != blocker {
		t.Errorf("event list -o json data = %v, want dep %s", event["data"], blocker)
	}
}

// TestEventDocPinsTallyKeys pins the nested tally document; no command produces
// one today, so the constructor is exercised directly.
func TestEventDocPinsTallyKeys(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	doc := eventDocFrom(&core.Event{
		ID:        "ev-1",
		ProjectID: "acme",
		Kind:      core.EventTaskCreated,
		Summary:   "a tally",
		Tally:     &core.Tally{Scope: 3, Done: 1, ReadyAgent: 1, Blocked: 1, Waves: 2},
		CreatedAt: at,
	})
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal event doc: %v", err)
	}
	object := jsonObject(t, string(encoded))
	assertKeySet(t, "eventDoc", object, "id", "project_id", "kind", "summary", "tally", "created_at")
	tally, ok := object["tally"].(map[string]any)
	if !ok {
		t.Fatalf("eventDoc tally = %v, want an object", object["tally"])
	}
	assertKeySet(t, "eventDoc tally", tally, "scope", "done", "ready_agent", "ready_human", "blocked", "cycles", "waves")
}

// TestTaskActionVerbsEmitTaskDocSchema folds the remaining action results into
// the one task document: note create, assign, and delete used to print
// unstructured text under -o json. Each is compared against the exact key set
// `task get` reports, so a field dropped from the document fails here.
func TestTaskActionVerbsEmitTaskDocSchema(t *testing.T) {
	r := newRunner(t)
	r.run("project", "create", "Acme")
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("task", "create", "-p", "acme", "-t", "First"))

	docKeys := jsonTagNames(reflect.TypeOf(taskDoc{}))

	noted := jsonObject(t, r.run("task", "note", "create", taskID, "-b", "a decision", "-o", "json"))
	assertTaskDocKeys(t, "task note create", noted, docKeys)
	assertKeySet(t, "task note create", noted, taskObjectKeys(t, r, taskID)...)
	if notes, ok := noted["notes"].([]any); !ok || len(notes) != 1 {
		t.Errorf("task note create -o json notes = %v, want the new note", noted["notes"])
	}

	assigned := jsonObject(t, r.run("task", "assign", taskID, "--actor", "claude", "-o", "json"))
	assertTaskDocKeys(t, "task assign", assigned, docKeys)
	assertKeySet(t, "task assign", assigned, taskObjectKeys(t, r, taskID)...)
	if got := assigned["assignee"]; got != "claude" {
		t.Errorf("task assign -o json assignee = %v, want claude", got)
	}

	want := taskObjectKeys(t, r, taskID)
	deleted := jsonObject(t, r.run("task", "delete", taskID, "-o", "json"))
	assertTaskDocKeys(t, "task delete", deleted, docKeys)
	assertKeySet(t, "task delete", deleted, want...)
	if got := deleted["id"]; got != taskID {
		t.Errorf("task delete -o json id = %v, want %s", got, taskID)
	}
}

// taskObjectKeys is the key set an action verb must emit: `task get` minus the
// read-only extras only the read verb derives (the base revision, graph facts,
// and provenance).
func taskObjectKeys(t *testing.T, r *runner, taskID string) []string {
	t.Helper()
	doc := jsonObject(t, r.run("task", "get", taskID, "-o", "json"))
	for _, readOnly := range []string{
		"base", "not_ready", "dependents", "origin", "origin_title",
		"origin_artifact", "origin_artifact_title", "grouped_under", "checks",
	} {
		delete(doc, readOnly)
	}
	keys := make([]string, 0, len(doc))
	for key := range doc {
		keys = append(keys, key)
	}
	return keys
}

// TestDomainDocsYAMLUsesTheSameKeysAsJSON mirrors the task-create yaml guard for
// the new domain documents: a wrong yaml tag must not let a yaml reader see a
// different schema than a json reader.
func TestDomainDocsYAMLUsesTheSameKeysAsJSON(t *testing.T) {
	cases := []struct {
		name string
		args func(taskID string) []string
	}{
		{"actor create", func(string) []string {
			return []string{"actor", "create", "bob", "-k", "human"}
		}},
		{"project get", func(string) []string { return []string{"project", "get", "acme"} }},
		{"event list", func(string) []string { return []string{"event", "list", "-p", "acme"} }},
		{"task note create", func(id string) []string {
			return []string{"task", "note", "create", id, "-b", "a decision"}
		}},
		{"task assign", func(id string) []string {
			return []string{"task", "assign", id, "--actor", "claude"}
		}},
		{"task delete", func(id string) []string { return []string{"task", "delete", id} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			jsonKeys := fixtureKeys(t, "json", tc.args)
			yamlKeys := fixtureKeys(t, "yaml", tc.args)
			if strings.Join(sortedKeyNames(jsonKeys), ",") != strings.Join(sortedKeyNames(yamlKeys), ",") {
				t.Errorf("%s yaml keys %v != json keys %v", tc.name, sortedKeyNames(yamlKeys), sortedKeyNames(jsonKeys))
			}
		})
	}
}

// fixtureKeys runs one verb against fresh fixtures in format and returns every
// key the output carries, at any depth.
func fixtureKeys(t *testing.T, format string, args func(taskID string) []string) map[string]bool {
	t.Helper()
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "api")
	r.run("actor", "create", "claude", "-k", "agent")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "First"))
	out := r.run(append(args(taskID), "-o", format)...)
	switch format {
	case "json":
		return walkJSONKeys(jsonValue(t, out))
	case "yaml":
		return walkJSONKeys(yamlValue(t, out))
	default:
		t.Fatalf("unknown format %q", format)
		return nil
	}
}

func jsonValue(t *testing.T, out string) any {
	t.Helper()
	var value any
	if err := json.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	return value
}

func yamlValue(t *testing.T, out string) any {
	t.Helper()
	var value any
	if err := yaml.Unmarshal([]byte(out), &value); err != nil {
		t.Fatalf("decode yaml: %v\n%s", err, out)
	}
	return value
}

func sortedKeyNames(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
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
