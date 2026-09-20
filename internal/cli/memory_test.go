package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

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
