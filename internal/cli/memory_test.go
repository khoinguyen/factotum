package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMemoryContextListsBriefs(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))
	r.run("memory", "create", "-p", projectID, "-t", "Deploy notes", "--brief", "load before deploys", "-b", "SECRET BODY", "--task", taskID)
	r.run("memory", "create", "-p", projectID, "-t", "Other", "--brief", "when relevant")

	out := r.run("memory", "context", "-p", projectID)
	for _, want := range []string{"Deploy notes", "load before deploys", "Other", "when relevant", taskID} {
		if !strings.Contains(out, want) {
			t.Fatalf("memory context missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "SECRET BODY") {
		t.Fatalf("memory context should not inline bodies:\n%s", out)
	}
}

func TestMemoryContextJSON(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))
	memoryID := firstField(t, r.run("memory", "create", "-p", projectID, "-t", "Deploy notes", "--brief", "load before deploys", "--task", taskID))

	out := r.run("memory", "context", "-p", projectID, "-o", "json")
	var entries []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Brief string `json:"brief"`
		Task  string `json:"task_id"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("memory context json: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0].ID != memoryID || entries[0].Title != "Deploy notes" || entries[0].Brief != "load before deploys" || entries[0].Task != taskID {
		t.Fatalf("memory context json = %+v", entries)
	}
}

func TestMemoryContextScopesProject(t *testing.T) {
	r := newRunner(t)
	first := firstField(t, r.run("project", "create", "First"))
	second := firstField(t, r.run("project", "create", "Second"))
	r.run("memory", "create", "-p", first, "-t", "in-first")
	r.run("memory", "create", "-p", second, "-t", "in-second")

	out := r.run("memory", "context", "-p", first)
	if !strings.Contains(out, "in-first") || strings.Contains(out, "in-second") {
		t.Fatalf("memory context did not scope to the project:\n%s", out)
	}
}

func TestMemoryContextEmpty(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	out := r.run("memory", "context", "-p", projectID)
	if strings.Contains(out, "art-") {
		t.Fatalf("empty project should list no memory:\n%s", out)
	}
}

func TestMemoryBriefRoundTrip(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "recall", "--brief", "when to load me", "--body", "remember this"))

	get := r.run("memory", "get", memoryID)
	if !strings.Contains(get, "brief: when to load me") {
		t.Fatalf("memory get missing brief:\n%s", get)
	}

	out := r.run("memory", "get", memoryID, "-o", "json")
	var doc struct {
		Brief string `json:"brief"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("memory get json: %v\n%s", err, out)
	}
	if doc.Brief != "when to load me" || doc.Body != "remember this" {
		t.Fatalf("memory get json = %+v", doc)
	}

	listOut := r.run("memory", "list", "--project", projectID, "-o", "json")
	var entries []struct {
		Brief string `json:"brief"`
	}
	if err := json.Unmarshal([]byte(listOut), &entries); err != nil {
		t.Fatalf("memory list json: %v\n%s", err, listOut)
	}
	if len(entries) != 1 || entries[0].Brief != "when to load me" {
		t.Fatalf("memory list json = %+v, want the brief", entries)
	}
}

func TestMemoryUpdateBrief(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "recall"))

	r.run("memory", "update", memoryID, "--brief", "new brief")
	out := r.run("memory", "get", memoryID, "-o", "json")
	var doc struct {
		Brief string `json:"brief"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("memory get json: %v\n%s", err, out)
	}
	if doc.Brief != "new brief" {
		t.Fatalf("memory update --brief = %q, want new brief", doc.Brief)
	}
}

func TestMemoryCreateListGet(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "recall", "--body", "remember this"))

	if !strings.HasPrefix(memoryID, "art-") {
		t.Fatalf("memory id = %q, want art- prefix", memoryID)
	}

	list := r.run("memory", "list", "--project", projectID)
	if !strings.Contains(list, memoryID) || !strings.Contains(list, "recall") {
		t.Fatalf("memory list missing entry:\n%s", list)
	}

	get := r.run("memory", "get", memoryID)
	if !strings.Contains(get, "(memory) "+memoryID+": recall") {
		t.Fatalf("memory get header wrong:\n%s", get)
	}
	if !strings.Contains(get, "remember this") {
		t.Fatalf("memory get missing body:\n%s", get)
	}
	if !strings.Contains(get, "created_at:") || !strings.Contains(get, "updated_at:") {
		t.Fatalf("memory get missing timestamps:\n%s", get)
	}
}

func TestMemoryListExcludesOtherKinds(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	r.run("memory", "create", "--project", projectID, "--title", "kept")
	r.run("doc", "create", "--project", projectID, "--kind", "doc", "--title", "dropped")

	list := r.run("memory", "list", "--project", projectID)
	if strings.Contains(list, "dropped") {
		t.Fatalf("memory list leaked a doc artifact:\n%s", list)
	}
	if !strings.Contains(list, "kept") {
		t.Fatalf("memory list missing memory artifact:\n%s", list)
	}
}

func TestMemoryListFiltersByProject(t *testing.T) {
	r := newRunner(t)
	first := firstField(t, r.run("project", "create", "First"))
	second := firstField(t, r.run("project", "create", "Second"))
	r.run("memory", "create", "--project", first, "--title", "in-first")
	r.run("memory", "create", "--project", second, "--title", "in-second")

	list := r.run("memory", "list", "--project", first)
	if !strings.Contains(list, "in-first") || strings.Contains(list, "in-second") {
		t.Fatalf("memory list did not filter by project:\n%s", list)
	}
}

func TestMemoryListJSONIsStable(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "--project", projectID, "--title", "recall", "--body", "remember this")

	out := r.run("memory", "list", "--project", projectID, "-o", "json")
	var entries []struct {
		ID      string `json:"id"`
		Title   string `json:"title"`
		Project string `json:"project"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("memory list json: %v\n%s", err, out)
	}
	if len(entries) != 1 || entries[0].Title != "recall" || entries[0].Project != projectID {
		t.Fatalf("memory list json = %+v, want one entry for recall", entries)
	}
}

func TestMemorySearch(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "--project", projectID, "--title", "Terraform notes", "--body", "apply in devops")
	r.run("memory", "create", "--project", projectID, "--title", "Kubernetes notes", "--body", "cluster upgrade")

	out := r.run("memory", "search", "terraform", "--project", projectID)
	if !strings.Contains(out, "Terraform notes") || strings.Contains(out, "Kubernetes notes") {
		t.Fatalf("memory search did not filter:\n%s", out)
	}

	jsonOut := r.run("memory", "search", "notes", "--project", projectID, "-o", "json")
	var entries []struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &entries); err != nil {
		t.Fatalf("memory search json: %v\n%s", err, jsonOut)
	}
	if len(entries) != 2 || entries[0].Title != "Kubernetes notes" || entries[1].Title != "Terraform notes" {
		t.Fatalf("memory search json order = %+v, want Kubernetes notes then Terraform notes", entries)
	}
}

func TestMemorySearchRanksTitleBeforeBody(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "--project", projectID, "--title", "run scripts", "--body", "terraform apply")
	r.run("memory", "create", "--project", projectID, "--title", "Terraform notes", "--body", "unrelated")

	out := r.run("memory", "search", "terraform", "--project", projectID, "-o", "json")
	var entries []struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("memory search json: %v\n%s", err, out)
	}
	if len(entries) != 2 || entries[0].Title != "Terraform notes" || entries[1].Title != "run scripts" {
		t.Fatalf("memory search order = %+v, want title match first", entries)
	}
}

func TestMemorySearchExcludesOtherKinds(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("memory", "create", "--project", projectID, "--title", "terraform memory")
	r.run("doc", "create", "--project", projectID, "--kind", "doc", "--title", "terraform doc")

	out := r.run("memory", "search", "terraform", "--project", projectID)
	if !strings.Contains(out, "terraform memory") || strings.Contains(out, "terraform doc") {
		t.Fatalf("memory search leaked a doc artifact:\n%s", out)
	}
}

func TestMemoryGetJSONIncludesBodyAndTimestamps(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "recall", "--body", "remember this"))

	out := r.run("memory", "get", memoryID, "-o", "json")
	var doc struct {
		ID        string `json:"id"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
		UpdatedAt string `json:"updated_at"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("memory get json: %v\n%s", err, out)
	}
	if doc.ID != memoryID || doc.Title != "recall" || doc.Body != "remember this" || doc.CreatedAt == "" || doc.UpdatedAt == "" {
		t.Fatalf("memory get json = %+v", doc)
	}
}

func TestMemoryUpdate(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "old", "--body", "before"))

	out := r.run("memory", "update", memoryID, "--title", "new", "--body", "after")
	if !strings.Contains(out, "updated: true") || !strings.Contains(out, "title: new") {
		t.Fatalf("memory update output:\n%s", out)
	}

	get := r.run("memory", "get", memoryID)
	if !strings.Contains(get, "(memory) "+memoryID+": new") || !strings.Contains(get, "after") {
		t.Fatalf("memory update not persisted:\n%s", get)
	}
}

func TestMemoryUpdateJSON(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "old", "--body", "before"))

	out := r.run("memory", "update", memoryID, "--title", "renamed", "-o", "json")
	var doc struct {
		ID    string `json:"id"`
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("memory update json: %v\n%s", err, out)
	}
	if doc.ID != memoryID || doc.Title != "renamed" || doc.Body != "before" {
		t.Fatalf("memory update json = %+v", doc)
	}
}

func TestMemoryUpdateFromFile(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "old"))

	path := filepath.Join(t.TempDir(), "body.txt")
	if err := os.WriteFile(path, []byte("from a file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r.run("memory", "update", memoryID, "--file", path)

	get := r.run("memory", "get", memoryID)
	if !strings.Contains(get, "from a file") {
		t.Fatalf("memory update --file not persisted:\n%s", get)
	}
}

func TestMemoryUpdateTaskLink(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--title", "work"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "note"))

	r.run("memory", "update", memoryID, "--task", taskID)
	if get := r.run("memory", "get", memoryID); !strings.Contains(get, "task: "+taskID) {
		t.Fatalf("memory update --task did not attach:\n%s", get)
	}

	r.run("memory", "update", memoryID, "--task", "")
	if get := r.run("memory", "get", memoryID); strings.Contains(get, "task: ") {
		t.Fatalf("memory update --task \"\" did not detach:\n%s", get)
	}
}

func TestMemoryUpdateRequiresChange(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "note"))

	if err := r.runErr("memory", "update", memoryID); !errors.Is(err, ErrUsage) {
		t.Fatalf("memory update with no flags error = %v, want ErrUsage", err)
	}
}

func TestMemoryUpdateRejectsOtherKinds(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	docID := firstField(t, r.run("doc", "create", "--project", projectID, "--kind", "doc", "--title", "a doc"))

	if err := r.runErr("memory", "update", docID, "--title", "renamed"); err == nil {
		t.Fatal("memory update on a doc artifact error = nil, want rejection")
	}
}

func TestMemoryDelete(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	memoryID := firstField(t, r.run("memory", "create", "--project", projectID, "--title", "note"))

	out := r.run("memory", "delete", memoryID)
	if !strings.Contains(out, "deleted: true") {
		t.Fatalf("memory delete output:\n%s", out)
	}
	if err := r.runErr("memory", "get", memoryID); err == nil {
		t.Fatal("memory get after delete error = nil, want not found")
	}
}

func TestMemoryDeleteRejectsOtherKinds(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	docID := firstField(t, r.run("doc", "create", "--project", projectID, "--kind", "doc", "--title", "a doc"))

	if err := r.runErr("memory", "delete", docID); err == nil {
		t.Fatal("memory delete on a doc artifact error = nil, want rejection")
	}
	if out := r.run("doc", "list", "--project", projectID); !strings.Contains(out, docID) {
		t.Fatalf("memory delete removed a non-memory artifact:\n%s", out)
	}
}
