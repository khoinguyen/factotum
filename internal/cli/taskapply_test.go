package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoc(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
	return path
}

func mutateJSON(t *testing.T, doc string, mutate func(map[string]any)) string {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal([]byte(doc), &fields); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	mutate(fields)
	data, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	return string(data) + "\n"
}

func writeEditorScript(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "editor.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("WriteFile(editor) error = %v", err)
	}
	return path
}

func newDocTask(t *testing.T, r *runner) string {
	t.Helper()
	projectID := firstField(t, r.run("project", "create", "Acme"))
	return firstField(t, r.run("task", "create", "-p", projectID, "-t", "doc", "-b", "body text"))
}

func TestTaskGetJSONDocumentRoundTripsUnchanged(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)

	doc := r.run("task", "get", taskID, "-o", "json")
	if !strings.Contains(doc, `"title": "doc"`) {
		t.Fatalf("json document missing title:\n%s", doc)
	}
	path := writeDoc(t, "task.json", doc)

	out := r.run("task", "apply", "-f", path)
	if !strings.Contains(out, "updated: false") {
		t.Fatalf("applying an unchanged document should be a no-op, got:\n%s", out)
	}
}

func TestTaskApplyJSONUpdatesTitle(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)

	doc := r.run("task", "get", taskID, "-o", "json")
	edited := mutateJSON(t, doc, func(fields map[string]any) { fields["title"] = "edited" })
	path := writeDoc(t, "task.json", edited)

	r.run("task", "apply", "-f", path)
	if got := r.run("task", "get", taskID); !strings.Contains(got, "edited") {
		t.Fatalf("apply did not update the title:\n%s", got)
	}
}

func TestTaskGetYAMLDocumentRoundTrips(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)

	doc := r.run("task", "get", taskID, "-o", "yaml")
	if !strings.Contains(doc, "title: doc") || !strings.Contains(doc, "status: todo") {
		t.Fatalf("yaml document missing fields:\n%s", doc)
	}
	path := writeDoc(t, "task.yaml", strings.Replace(doc, "title: doc", "title: edited", 1))

	r.run("task", "apply", "-f", path)
	if got := r.run("task", "get", taskID); !strings.Contains(got, "edited") {
		t.Fatalf("apply -f *.yaml did not update the title:\n%s", got)
	}
}

func TestTaskApplyRejectsInvalidDocuments(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)
	doc := r.run("task", "get", taskID, "-o", "json")

	cases := map[string]string{
		"immutable project": mutateJSON(t, doc, func(fields map[string]any) { fields["project_id"] = "other" }),
		"managed assignee":  mutateJSON(t, doc, func(fields map[string]any) { fields["assignee"] = "someone" }),
		"bad status":        mutateJSON(t, doc, func(fields map[string]any) { fields["status"] = "bogus" }),
		"unknown field":     `{"id": "` + taskID + `", "bogus": 1}`,
		"missing id":        `{"title": "x"}`,
	}
	for name, content := range cases {
		path := writeDoc(t, "task.json", content)
		if err := r.runErr("task", "apply", "-f", path); err == nil {
			t.Errorf("%s: expected an error, got nil", name)
		}
	}
}

func TestTaskApplyRejectsStaleDocument(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)
	doc := r.run("task", "get", taskID, "-o", "json")

	// A concurrent change after the document was produced invalidates it.
	r.run("task", "set", taskID, "title=other")
	edited := mutateJSON(t, doc, func(fields map[string]any) { fields["title"] = "mine" })
	path := writeDoc(t, "task.json", edited)

	if err := r.runErr("task", "apply", "-f", path); err == nil {
		t.Fatal("expected a conflict applying a stale document, got nil")
	}
	if got := r.run("task", "get", taskID); !strings.Contains(got, "other") {
		t.Fatalf("stale apply must not overwrite the concurrent change:\n%s", got)
	}
}

func TestTaskEditUsesEditor(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)
	t.Setenv("EDITOR", writeEditorScript(t, "printf 'title: edited\\n' > \"$1\"\n"))

	r.run("task", "edit", taskID)
	if got := r.run("task", "get", taskID); !strings.Contains(got, "edited") {
		t.Fatalf("task edit did not apply the edited document:\n%s", got)
	}
}

func TestTaskEditNoChanges(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)
	t.Setenv("EDITOR", writeEditorScript(t, "exit 0\n"))

	if out := r.run("task", "edit", taskID); !strings.Contains(out, "updated: false") {
		t.Fatalf("edit with no changes = %q, want updated: false", out)
	}
}

func TestTaskGetRejectsUnknownOutputFormat(t *testing.T) {
	r := newRunner(t)
	taskID := newDocTask(t, r)

	if err := r.runErr("task", "get", taskID, "-o", "xml"); err == nil {
		t.Fatal("expected an error for -o xml, got nil")
	}
}
