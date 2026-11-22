package cli

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// TestTaskVerbsEmitOneTaskDocSchema pins one machine schema for task objects:
// every verb that reports a task as a document emits the snake_case taskDoc
// keys, so a parser written against `task get` also reads `task create`,
// `task promote`, `task update`, and the other mutations. A key outside taskDoc
// means a verb leaked the core ticket's Go field names.
func TestTaskVerbsEmitOneTaskDocSchema(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "api")
	r.run("actor", "create", "claude", "-k", "agent")
	writeFeedbackConfig(t, r, map[string]string{"factotum": filepath.Join(t.TempDir(), "factotum.json")})

	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "First", "-r", "api"))
	otherID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Second"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "An idea"))
	bugID := firstField(t, r.run("bug", "create", "-p", projectID, "-t", "A bug"))

	docKeys := jsonTagNames(reflect.TypeOf(taskDoc{}))

	// `task get` is the schema authority: its document is taskDoc itself, so
	// every other verb's keys must be taskDoc keys. Check the read verb first.
	readDoc := jsonObject(t, r.run("task", "get", taskID, "-o", "json"))
	for key := range readDoc {
		if !docKeys[key] {
			t.Fatalf("task get -o json emitted %q, which is not a taskDoc key", key)
		}
	}
	assertSnakeCaseKeys(t, "task get", readDoc)

	verbs := []struct {
		name string
		args []string
	}{
		{name: "task create", args: []string{"task", "create", "-p", projectID, "-t", "Third"}},
		{name: "task get", args: []string{"task", "get", taskID}},
		{name: "task update", args: []string{"task", "update", taskID, "--priority", "3"}},
		{name: "task set", args: []string{"task", "set", taskID, "priority=2"}},
		{name: "task dep create", args: []string{"task", "dep", "create", otherID, taskID}},
		{name: "task dep delete", args: []string{"task", "dep", "delete", otherID, taskID}},
		{name: "task wait", args: []string{"task", "wait", taskID, "--on", "claude"}},
		{name: "task snooze", args: []string{"task", "snooze", taskID, "--until", "2030-01-01T00:00:00Z"}},
		{name: "task start", args: []string{"task", "start", otherID}},
		{name: "task review", args: []string{"task", "review", otherID}},
		{name: "task done", args: []string{"task", "done", otherID}},
		{name: "task reopen", args: []string{"task", "reopen", otherID}},
		{name: "task block", args: []string{"task", "block", otherID}},
		{name: "task cancel", args: []string{"task", "cancel", otherID}},
		{name: "idea create", args: []string{"idea", "create", "-p", projectID, "-t", "Another idea"}},
		{name: "bug create", args: []string{"bug", "create", "-p", projectID, "-t", "Another bug"}},
		{name: "milestone create", args: []string{"milestone", "create", "-p", projectID, "-t", "v1"}},
		{name: "task promote", args: []string{"idea", "promote", ideaID, "-a", "claude", "--acceptance", "it works"}},
		{name: "bug triage", args: []string{"bug", "triage", bugID, "-a", "claude", "--acceptance", "it works"}},
		{name: "feedback create", args: []string{"feedback", "create", "-b", "a bug"}},
	}
	for _, verb := range verbs {
		out := r.run(append(append([]string{}, verb.args...), "-o", "json")...)
		doc := jsonObject(t, out)
		if len(doc) == 0 {
			t.Fatalf("%s -o json emitted no document:\n%s", verb.name, out)
		}
		assertSnakeCaseKeys(t, verb.name, doc)
		for key := range doc {
			if !docKeys[key] {
				t.Errorf("%s -o json emitted %q, which is not a taskDoc key:\n%s", verb.name, key, out)
			}
		}
		switch verb.name {
		case "task create", "task get", "task promote":
			// The verbs a script chains must agree on the task's identity keys,
			// not merely avoid Go field names.
			for _, key := range []string{
				"id", "project_id", "repo", "kind", "title", "description",
				"status", "priority", "labels", "groomed", "acceptance_criteria",
				"deps", "waiting_on", "created_at", "updated_at",
			} {
				if _, ok := doc[key]; !ok {
					t.Errorf("%s -o json is missing %q, which the task document carries:\n%s", verb.name, key, out)
				}
			}
		}
	}
}

// TestTaskCreateYAMLUsesTheTaskDocSchema pins `-o yaml` to the same document as
// `-o json`: before the fix, yaml spelled the Go field names (`projectid`,
// `acceptancecriteria`) because it serialized the core ticket, so a yaml reader
// and a json reader disagreed.
func TestTaskCreateYAMLUsesTheTaskDocSchema(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	jsonDoc := jsonObject(t, r.run("task", "create", "-p", projectID, "-t", "First", "-o", "json"))
	var yamlDoc map[string]any
	if err := yaml.Unmarshal([]byte(r.run("task", "create", "-p", projectID, "-t", "First", "-o", "yaml")), &yamlDoc); err != nil {
		t.Fatalf("decode yaml: %v", err)
	}
	if len(yamlDoc) == 0 {
		t.Fatal("task create -o yaml emitted no document")
	}
	assertSnakeCaseKeys(t, "task create", yamlDoc)
	for key := range yamlDoc {
		if _, ok := jsonDoc[key]; !ok {
			t.Errorf("task create -o yaml emitted %q, which task create -o json does not", key)
		}
	}
	if len(yamlDoc) != len(jsonDoc) {
		t.Errorf("task create yaml has %d keys, json has %d; the two formats must agree", len(yamlDoc), len(jsonDoc))
	}
}

// TestTaskListVerbsEmitOneTaskDocSchema pins the same schema for the list
// verbs: every entry carries only taskDoc keys, at every depth.
func TestTaskListVerbsEmitOneTaskDocSchema(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "-p", projectID, "-t", "A task")
	r.run("idea", "create", "-p", projectID, "-t", "An idea")
	r.run("bug", "create", "-p", projectID, "-t", "A bug")
	r.run("milestone", "create", "-p", projectID, "-t", "v1")

	docKeys := jsonTagNames(reflect.TypeOf(taskDoc{}))
	lists := map[string][]string{
		"task list":      {"task", "list", "-p", projectID},
		"task search":    {"task", "search", "task", "-p", projectID},
		"milestone list": {"milestone", "list", "-p", projectID},
		"idea list":      {"idea", "list", "-p", projectID},
		"bug list":       {"bug", "list", "-p", projectID},
	}
	for name, args := range lists {
		out := r.run(append(args, "-o", "json")...)
		var entries []map[string]any
		if err := json.Unmarshal([]byte(out), &entries); err != nil {
			t.Fatalf("%s -o json: %v\n%s", name, err, out)
		}
		if len(entries) == 0 {
			t.Fatalf("%s -o json emitted no entries:\n%s", name, out)
		}
		for _, entry := range entries {
			assertSnakeCaseKeys(t, name, entry)
			for key := range entry {
				if !docKeys[key] {
					t.Errorf("%s -o json emitted %q, which is not a taskDoc key", name, key)
				}
			}
		}
	}
}

// TestCaptureCreateKeepsOriginArtifact keeps the provenance the raw ticket used
// to expose: a capture created from a prompt document still names it.
func TestCaptureCreateKeepsOriginArtifact(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	docID := firstField(t, r.run("doc", "create", "-p", projectID, "-t", "Add dark mode", "-b", "please add a dark mode toggle"))

	out := r.run("idea", "create", "-p", projectID, "-t", "An idea", "--origin-artifact", docID, "-o", "json")
	if got := jsonObject(t, out)["origin_artifact"]; got != docID {
		t.Fatalf("idea create -o json origin_artifact = %v, want %s:\n%s", got, docID, out)
	}
}

// TestNoCommandEmitsRawStruct stops the divergence at the source: a command must
// render a domain value through its document constructor, never hand a raw core
// domain value to emit, which would serialize Go field names. The guard covers
// every domain whose verbs emit a machine document: tasks, actors, projects, and
// events.
func TestNoCommandEmitsRawStruct(t *testing.T) {
	raw := map[string]string{
		"task": "taskDocFrom", "tasks": "taskListEntries", "capture": "taskDocFrom", "captures": "taskListEntries",
		"updated": "taskDocFrom", "original": "taskDocFrom", "milestone": "taskDocFrom", "milestones": "taskListEntries",
		"actor": "actorDocFrom", "actors": "actorDocsFrom",
		"project": "projectDocFrom", "projects": "projectDocsFrom",
		"event": "eventDocFrom", "events": "eventDocsFrom",
		"repo": "repoDocFrom", "repos": "repoDocsFrom",
	}
	// A raw core slice reached through a field would leak too; name the fields
	// that carry one so a re-introduced `emit(project.Repos)` fails.
	rawFields := map[string]string{"Repos": "repoDocsFrom(project.Repos)"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "emit" {
				return true
			}
			switch arg := call.Args[0].(type) {
			case *ast.Ident:
				if doc, ok := raw[arg.Name]; ok {
					t.Errorf("%s: emit(%s) would serialize a raw core value as Go field names; render it with %s", name, arg.Name, doc)
				}
			case *ast.SelectorExpr:
				if doc, ok := rawFields[arg.Sel.Name]; ok {
					t.Errorf("%s: emit(%s) would serialize raw core values as Go field names; render them with %s", name, arg.Sel.Name, doc)
				}
			}
			return true
		})
	}
}

// jsonObject decodes one JSON object.
func jsonObject(t *testing.T, out string) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	return doc
}

// assertSnakeCaseKeys fails on every key that is not snake_case, at any depth,
// so a Go field name cannot hide inside notes or another sub-document.
func assertSnakeCaseKeys(t *testing.T, verb string, value any) {
	t.Helper()
	for key := range walkJSONKeys(value) {
		if key != strings.ToLower(key) {
			t.Errorf("%s -o json leaked Go field name %q; machine documents are snake_case", verb, key)
		}
	}
}

func walkJSONKeys(value any) map[string]bool {
	keys := map[string]bool{}
	var walk func(any)
	walk = func(node any) {
		switch typed := node.(type) {
		case map[string]any:
			for key, child := range typed {
				keys[key] = true
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	return keys
}
