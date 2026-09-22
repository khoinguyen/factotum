package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDocListJSONIsSnakeCase(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	docID := firstField(t, r.run("doc", "create", "-p", projectID, "-t", "Product spec", "-k", "spec", "-b", "ship it"))

	out := r.run("doc", "list", "-p", projectID, "-o", "json")
	for _, want := range []string{`"id"`, `"project_id"`, `"kind"`, `"title"`, `"created_at"`, `"updated_at"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("doc list json missing %s:\n%s", want, out)
		}
	}
	for _, leak := range []string{`"ID"`, `"ProjectID"`, `"CreatedAt"`, `"UpdatedAt"`, `"TaskID"`} {
		if strings.Contains(out, leak) {
			t.Fatalf("doc list json leaked Go field %s:\n%s", leak, out)
		}
	}

	var docs []struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
		Kind      string `json:"kind"`
		Title     string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &docs); err != nil {
		t.Fatalf("doc list json: %v\n%s", err, out)
	}
	if len(docs) != 1 || docs[0].ID != docID || docs[0].ProjectID != projectID || docs[0].Kind != "spec" || docs[0].Title != "Product spec" {
		t.Fatalf("doc list json = %+v", docs)
	}
}

func TestDocGetJSON(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "work"))
	docID := firstField(t, r.run("doc", "create", "-p", projectID, "-t", "Product spec", "-k", "spec", "-b", "ship it", "--task", taskID))

	out := r.run("doc", "get", docID, "-o", "json")
	var doc struct {
		ID        string `json:"id"`
		ProjectID string `json:"project_id"`
		TaskID    string `json:"task_id"`
		Kind      string `json:"kind"`
		Title     string `json:"title"`
		Body      string `json:"body"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("doc get json: %v\n%s", err, out)
	}
	if doc.ID != docID || doc.ProjectID != projectID || doc.TaskID != taskID || doc.Kind != "spec" || doc.Title != "Product spec" || doc.Body != "ship it" || doc.CreatedAt == "" {
		t.Fatalf("doc get json = %+v", doc)
	}
}

func TestDocGetText(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	docID := firstField(t, r.run("doc", "create", "-p", projectID, "-t", "Product spec", "-k", "spec", "-b", "ship it"))

	out := r.run("doc", "get", docID)
	if !strings.Contains(out, "(spec) "+docID+": Product spec") || !strings.Contains(out, "project: "+projectID) || !strings.Contains(out, "ship it") {
		t.Fatalf("doc get text:\n%s", out)
	}
}

func TestDocGetNotFound(t *testing.T) {
	r := newRunner(t)
	if err := r.runErr("doc", "get", "doc-nope"); err == nil {
		t.Fatal("doc get unknown id error = nil, want not found")
	}
}
