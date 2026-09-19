package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTaskNextUsesProjectFromConfigFile(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "one"))

	cfgPath := filepath.Join(t.TempDir(), "project.toml")
	if err := os.WriteFile(cfgPath, []byte("project = \""+projectID+"\"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	out := r.run("--config", cfgPath, "task", "next", "-n", "1")
	if !strings.Contains(out, taskID) {
		t.Fatalf("task next should use the configured project:\n%s", out)
	}
}

func TestShorthandFlags(t *testing.T) {
	r := newRunner(t)

	projectID := firstField(t, r.run("project", "create", "Acme", "-r", "backend"))
	actorID := firstField(t, r.run("actor", "create", "-k", "agent", "Bot"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "one", "-b", "body", "-r", "backend"))

	if out := r.run("task", "next", "-p", projectID, "-n", "1"); !strings.Contains(out, taskID) {
		t.Fatalf("task next -p/-n mismatch:\n%s", out)
	}
	if out := r.run("task", "list", "-p", projectID, "-s", "todo", "-k", "task"); !strings.Contains(out, taskID) {
		t.Fatalf("task list -p/-s/-k mismatch:\n%s", out)
	}
	if out := r.run("task", "list", "-p", projectID, "-r", "backend"); !strings.Contains(out, taskID) {
		t.Fatalf("task list -r mismatch:\n%s", out)
	}
	r.run("task", "update", taskID, "-t", "renamed", "-b", "updated")
	if out := r.run("task", "get", taskID); !strings.Contains(out, "renamed") || !strings.Contains(out, "updated") {
		t.Fatalf("task update -t/-b mismatch:\n%s", out)
	}
	r.run("task", "assign", taskID, "-a", actorID)
	if out := r.run("task", "note", "create", taskID, "-b", "hello"); !strings.Contains(out, "notes=1") {
		t.Fatalf("task note add -b mismatch:\n%s", out)
	}
	if milestoneID := firstField(t, r.run("milestone", "create", "-p", projectID, "-t", "v1")); milestoneID == "" {
		t.Fatal("milestone create -p/-t returned no id")
	}
	if out := r.run("doc", "create", "-p", projectID, "-t", "Spec", "-k", "spec", "-b", "content"); !strings.Contains(out, "Spec") {
		t.Fatalf("doc add -p/-t/-k/-b mismatch:\n%s", out)
	}
	if out := r.run("doc", "list", "-p", projectID); !strings.Contains(out, "Spec") {
		t.Fatalf("doc list -p mismatch:\n%s", out)
	}
	if out := r.run("doc", "search", "Spec", "-p", projectID); !strings.Contains(out, "Spec") {
		t.Fatalf("doc search -p mismatch:\n%s", out)
	}
	if out := r.run("graph", "render", "-p", projectID, "-f", "json"); !strings.Contains(out, "{") {
		t.Fatalf("graph render -p/-f mismatch:\n%s", out)
	}
	if out := r.run("event", "list", "-p", projectID, "-n", "5"); strings.TrimSpace(out) == "" {
		t.Fatal("event list -p/-n returned no output")
	}
}
