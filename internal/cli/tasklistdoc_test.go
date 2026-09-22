package cli

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

// jsonTagNames collects the json key of every field of a struct type.
func jsonTagNames(t reflect.Type) map[string]bool {
	keys := make(map[string]bool, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// TestTaskListEntryGolden pins the exact structured shape of a task list entry,
// so a field rename or an accidental Go-name leak fails loudly.
func TestTaskListEntryGolden(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	assignee := core.ActorID("alice")
	task := &core.Task{
		ID: "t-1", ProjectID: "prj-1", Repo: "api", Kind: core.KindTask,
		Title: "Do it", Status: core.StatusTodo, Priority: 3,
		Labels: []string{"a", "b"}, AssigneeID: &assignee,
		CreatedAt: at, UpdatedAt: at,
	}
	got, err := json.Marshal(taskListEntryFrom(task))
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	want := `{"id":"t-1","project_id":"prj-1","repo":"api","kind":"task","title":"Do it","status":"todo","priority":3,"labels":["a","b"],"assignee":"alice","created_at":"2026-01-02T03:04:05Z","updated_at":"2026-01-02T03:04:05Z"}`
	if string(got) != want {
		t.Fatalf("taskListEntry golden mismatch:\n got %s\nwant %s", got, want)
	}
}

// TestTaskListEntryKeysSubsetOfTaskDoc is the compatibility contract: every key
// a list entry emits is also a key of the single-result task document, so one
// parser handles both task get and task list.
func TestTaskListEntryKeysSubsetOfTaskDoc(t *testing.T) {
	docKeys := jsonTagNames(reflect.TypeOf(taskDoc{}))
	for key := range jsonTagNames(reflect.TypeOf(taskListEntry{})) {
		if !docKeys[key] {
			t.Errorf("taskListEntry key %q is not a key of taskDoc; one parser cannot handle both", key)
		}
	}
}

func TestTaskListJSONIsSnakeCase(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "api")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Product spec", "-r", "api", "--label", "cli"))

	out := r.run("task", "list", "-p", projectID, "-o", "json")
	for _, want := range []string{`"id"`, `"project_id"`, `"repo"`, `"kind"`, `"title"`, `"status"`, `"priority"`, `"labels"`, `"created_at"`, `"updated_at"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("task list json missing %s:\n%s", want, out)
		}
	}
	for _, leak := range []string{`"ID"`, `"ProjectID"`, `"Repo"`, `"Kind"`, `"Title"`, `"Status"`, `"Priority"`, `"Labels"`, `"AssigneeID"`, `"CreatedAt"`, `"UpdatedAt"`} {
		if strings.Contains(out, leak) {
			t.Fatalf("task list json leaked Go field %s:\n%s", leak, out)
		}
	}

	var entries []struct {
		ID        string   `json:"id"`
		ProjectID string   `json:"project_id"`
		Repo      string   `json:"repo"`
		Kind      string   `json:"kind"`
		Title     string   `json:"title"`
		Status    string   `json:"status"`
		Labels    []string `json:"labels"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("task list json: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0].ID != taskID || entries[0].ProjectID != projectID ||
		entries[0].Repo != "api" || entries[0].Kind != "task" || entries[0].Title != "Product spec" ||
		entries[0].Status != "todo" || len(entries[0].Labels) != 1 || entries[0].Labels[0] != "cli" {
		t.Fatalf("task list json = %+v", entries)
	}
}

func TestTaskListYAMLIsSnakeCase(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "create", "-p", projectID, "-t", "Product spec")

	out := r.run("task", "list", "-p", projectID, "-o", "yaml")
	for _, want := range []string{"id:", "project_id:", "created_at:", "updated_at:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("task list yaml missing %s:\n%s", want, out)
		}
	}
	for _, leak := range []string{"ProjectID", "CreatedAt", "UpdatedAt", "AssigneeID"} {
		if strings.Contains(out, leak) {
			t.Fatalf("task list yaml leaked Go field %s:\n%s", leak, out)
		}
	}
}

// TestTaskListTextUnchanged guards the human table against the structured-output
// change: list text keeps its columns and values.
func TestTaskListTextUnchanged(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "create", projectID, "api")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Product spec", "-r", "api"))

	out := r.run("task", "list", "-p", projectID)
	for _, want := range []string{"ID", "KIND", "TITLE", "STATUS", "PROJECT", "REPO", taskID, "Product spec", "todo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("task list text missing %s:\n%s", want, out)
		}
	}
}
