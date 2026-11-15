package cli

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
)

// runSplitErr executes a command against the runner's store and returns stdout,
// stderr, and the error so a test can assert the exit code a caller branches on
// without the helper swallowing the error.
func (r *runner) runSplitErr(args ...string) (string, string, error) {
	r.t.Helper()
	var stdout, stderr bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, r.getenv)
	base := r.setup(deps)

	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	err := root.Execute()
	return stdout.String(), stderr.String(), err
}

// TestTaskDepGroupRejectsMalformedInvocation pins the group's behaviour when it
// is invoked without a subcommand: `ft task dep <task> <dep>` used to print the
// help text and exit 0, so a script believed the dependency was recorded. Every
// malformed invocation must be a usage error (exit 2) with the help still on
// stderr, and nothing on stdout.
func TestTaskDepGroupRejectsMalformedInvocation(t *testing.T) {
	tests := []struct {
		name string
		args []string
		// reason is the phrase the usage error must name.
		reason string
	}{
		{name: "no arguments", args: []string{"task", "dep"}, reason: "expected a subcommand"},
		{name: "one argument", args: []string{"task", "dep", "t-abc"}, reason: `unknown subcommand "t-abc"`},
		{name: "shorthand create", args: []string{"task", "dep", "t-abc", "t-def"}, reason: `unknown subcommand "t-abc"`},
		{name: "three arguments", args: []string{"task", "dep", "t-abc", "t-def", "t-ghi"}, reason: `unknown subcommand "t-abc"`},
		{name: "unknown subcommand", args: []string{"task", "dep", "bogus"}, reason: `unknown subcommand "bogus"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			stdout, stderr, err := r.runSplitErr(tt.args...)

			if got := ExitCode(err); got != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2\nstderr:\n%s", err, got, stderr)
			}
			if !strings.Contains(stderr, "ft: "+tt.reason) {
				t.Fatalf("stderr must name the missing subcommand %q:\n%s", tt.reason, stderr)
			}
			if !strings.Contains(stderr, "Usage:") || !strings.Contains(stderr, "ft task dep") {
				t.Fatalf("help must stay on stderr:\n%s", stderr)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("help must not go to stdout, stdout =\n%s", stdout)
			}
		})
	}
}

// TestTaskDepCreateRejectsWrongArgCount keeps the canonical subcommand strict:
// it needs exactly the two operands it names.
func TestTaskDepCreateRejectsWrongArgCount(t *testing.T) {
	for _, n := range []int{0, 1, 3} {
		args := []string{"task", "dep", "create"}
		for i := 0; i < n; i++ {
			args = append(args, "t-abc")
		}
		r := newRunner(t)
		stdout, stderr, err := r.runSplitErr(args...)

		if got := ExitCode(err); got != 2 {
			t.Fatalf("create with %d args: ExitCode(%v) = %d, want 2\nstderr:\n%s", n, err, got, stderr)
		}
		if want := "expected 2 argument(s), got " + strconv.Itoa(n); !strings.Contains(stderr, want) {
			t.Fatalf("create with %d args: stderr must name the missing operands (%q):\n%s", n, want, stderr)
		}
		if strings.TrimSpace(stdout) != "" {
			t.Fatalf("create with %d args: help must not go to stdout, stdout =\n%s", n, stdout)
		}
	}
}

// TestTaskDepHelpIsNotAUsageError keeps -h/--help as the exit-0 path that
// prints help on stdout.
func TestTaskDepHelpIsNotAUsageError(t *testing.T) {
	for _, flag := range []string{"-h", "--help"} {
		r := newRunner(t)
		stdout, stderr, err := r.runSplitErr("task", "dep", flag)

		if err != nil {
			t.Fatalf("%s: Execute() error = %v, want nil", flag, err)
		}
		if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "ft task dep") {
			t.Fatalf("%s: help must go to stdout:\n%s", flag, stdout)
		}
		if strings.TrimSpace(stderr) != "" {
			t.Fatalf("%s: stderr must stay empty, stderr =\n%s", flag, stderr)
		}
	}
}

// TestTaskDepCreateStillRecordsDependency guards the canonical invocation: the
// malformed-shorthand fix must not change it, nor the -o json/--no-hints
// surfaces callers parse.
func TestTaskDepCreateStillRecordsDependency(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "dependent"))
	depID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "blocker"))

	out, errOut, err := r.runSplitErr("task", "dep", "create", taskID, depID)
	if err != nil {
		t.Fatalf("create error = %v\nstderr:\n%s", err, errOut)
	}
	if !strings.Contains(out, "added: true") {
		t.Fatalf("create must report the dependency was added:\n%s", out)
	}
	if !strings.Contains(r.run("task", "get", taskID), depID) {
		t.Fatalf("task get must list the new dependency %s:\n%s", depID, r.run("task", "get", taskID))
	}

	jsonOut, jsonErr, err := r.runSplitErr("task", "dep", "delete", taskID, depID, "-o", "json")
	if err != nil {
		t.Fatalf("delete -o json error = %v\nstderr:\n%s", err, jsonErr)
	}
	if !strings.Contains(jsonOut, `"ID": "`+taskID+`"`) || !strings.Contains(jsonOut, `"Deps": []`) {
		t.Fatalf("delete -o json must emit the task document without the dependency:\n%s", jsonOut)
	}

	quietOut, quietErr, err := r.runSplitErr("task", "dep", "create", taskID, depID, "--no-hints")
	if err != nil {
		t.Fatalf("create --no-hints error = %v\nstderr:\n%s", err, quietErr)
	}
	if strings.Contains(quietOut, "Next:") {
		t.Fatalf("--no-hints must suppress suggestions:\n%s", quietOut)
	}
}
