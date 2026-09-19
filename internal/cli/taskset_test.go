package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/store/builtins"
)

// runErr executes a command against the runner's store and returns the error,
// if any, without failing the test. It is used for cases that must be rejected.
func (r *runner) runErr(args ...string) error {
	r.t.Helper()
	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, nil)
	builtins.RegisterAll(deps.StoreFactories)
	root := NewRoot(deps)
	root.SetArgs(append([]string{
		"--store", "jsonfile", "--store-opt", "path=" + r.path,
		"--config", r.projectPath, "--user-config", r.userPath,
	}, args...))
	root.SetOut(&out)
	root.SetErr(&out)
	return root.Execute()
}

func TestTaskSetUpdatesFields(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "add", "-p", projectID, "-t", "original"))

	r.run("task", "set", taskID, "title=renamed", "status=in_progress", "priority=5")

	out := r.run("task", "get", taskID)
	if !strings.HasPrefix(out, "(in_progress) "+taskID+": renamed") {
		t.Fatalf("task set did not apply status/title:\n%s", out)
	}
}

func TestTaskSetLongBodyFromFile(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "add", "-p", projectID, "-t", "original"))

	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("# Heading\n\nlots of detail\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r.run("task", "set", taskID, "body=@"+bodyFile)

	out := r.run("task", "get", taskID)
	if !strings.Contains(out, "=== Description ===") || !strings.Contains(out, "lots of detail") {
		t.Fatalf("body=@file not applied:\n%s", out)
	}
}

func TestTaskSetStatusMatchesTransitionCommand(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	setID := firstField(t, r.run("task", "add", "-p", projectID, "-t", "via set"))
	doneID := firstField(t, r.run("task", "add", "-p", projectID, "-t", "via done"))

	if got := r.run("task", "set", setID, "status=done"); !strings.Contains(got, setID) || !strings.Contains(got, "done") {
		t.Fatalf("task set status=done output = %q", got)
	}
	r.run("task", "done", doneID)

	if got := r.run("task", "get", setID); !strings.HasPrefix(got, "(done) ") {
		t.Fatalf("task set status=done did not reach done:\n%s", got)
	}
	if got := r.run("task", "get", doneID); !strings.HasPrefix(got, "(done) ") {
		t.Fatalf("task done did not reach done:\n%s", got)
	}
}

func TestTaskSetRejectsInvalidValues(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "add", "-p", projectID, "-t", "original"))

	cases := map[string][]string{
		"unknown field": {"task", "set", taskID, "nope=1"},
		"bad priority":  {"task", "set", taskID, "priority=high"},
		"bad status":    {"task", "set", taskID, "status=bogus"},
		"bad kind":      {"task", "set", taskID, "kind=epic"},
		"malformed":     {"task", "set", taskID, "title"},
		"missing body":  {"task", "set", taskID, "body=@/no/such/file"},
	}
	for name, args := range cases {
		if err := r.runErr(args...); err == nil {
			t.Errorf("%s: expected an error, got nil", name)
		}
	}
}
