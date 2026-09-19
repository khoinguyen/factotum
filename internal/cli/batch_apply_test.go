package cli

import (
	"os"
	"strings"
	"testing"
)

func TestBatchApplyFromFileAndDryRun(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	a := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a"))
	b := firstField(t, r.run("task", "create", "-p", projectID, "-t", "b"))

	plan := "id: " + a + "\nstatus: done\n---\nid: " + b + "\ntitle: renamed b\n"
	path := writeDoc(t, "plan.yaml", plan)

	dryRunOut := r.run("task", "apply", "-f", path, "--dry-run")
	if !strings.Contains(dryRunOut, "dry_run: true") {
		t.Fatalf("dry-run output should be marked:\n%s", dryRunOut)
	}
	if got := r.run("task", "get", a); !strings.Contains(got, "(todo)") {
		t.Fatalf("dry-run must not write:\n%s", got)
	}

	out := r.run("task", "apply", "-f", path)
	if !strings.Contains(out, "task_id: "+a) || !strings.Contains(out, "task_id: "+b) {
		t.Fatalf("batch apply should report every document:\n%s", out)
	}
	if got := r.run("task", "get", a); !strings.Contains(got, "(done)") {
		t.Fatalf("first document not applied:\n%s", got)
	}
	if got := r.run("task", "get", b); !strings.Contains(got, "renamed b") {
		t.Fatalf("second document not applied:\n%s", got)
	}
}

func TestBatchApplyFromStdin(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "a"))

	old := os.Stdin
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe() error = %v", err)
	}
	os.Stdin = pr
	defer func() { os.Stdin = old }()
	go func() {
		_, _ = pw.WriteString(`[{"id": "` + taskID + `", "status": "done"}]`)
		_ = pw.Close()
	}()

	r.run("task", "apply", "-f", "-")
	if got := r.run("task", "get", taskID); !strings.Contains(got, "(done)") {
		t.Fatalf("stdin document not applied:\n%s", got)
	}
}
