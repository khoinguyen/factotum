package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestRunInheritsTheCallersSession pins loop parity: a run started from inside a
// session (a builder launching a nested run) hands its agent the session it
// inherited, so the nested work rolls up to the chief's timeline instead of
// starting an unrelated one. The id is reported too, so the same invocation is
// queryable without guessing.
func TestRunInheritsTheCallersSession(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode2")
	inherited := func(key string) string {
		if key == harnesspkg.EnvSession {
			return "sid-chief"
		}
		return hermeticGetenv(key)
	}

	out := r.runAs(inherited, "run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	if got := backend.Prepared()[0].Env[harnesspkg.EnvSession]; got != "sid-chief" {
		t.Fatalf("env %s = %q, want the inherited session", harnesspkg.EnvSession, got)
	}
	if !strings.Contains(out, "session: sid-chief") {
		t.Fatalf("run output does not report its session:\n%s", out)
	}
}

// TestRunMintsARunScopedSession covers a run started from a bare shell: there is
// no session to inherit, so the run mints one and exports it. The run's own
// events and the work its agent does are then both retrievable by that id, which
// is why the id is printed rather than left to be discovered.
func TestRunMintsARunScopedSession(t *testing.T) {
	r := newRunner(t)
	projectID, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode2")

	out := r.runAs(hermeticGetenv, "run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	sid := backend.Prepared()[0].Env[harnesspkg.EnvSession]
	if sid == "" {
		t.Fatalf("run exported no FACTOTUM_SESSION; want a minted run-scoped id:\n%s", out)
	}
	if !strings.Contains(out, "session: "+sid) {
		t.Fatalf("run output does not report the minted session %q:\n%s", sid, out)
	}
	if listed := r.run("event", "list", "--project", projectID, "--session", sid); !strings.Contains(listed, "task.run_started") {
		t.Fatalf("the minted session does not carry the run's own events:\n%s", listed)
	}

	// The agent's side: an ft call under the run's exported env attributes what
	// it creates to the same session.
	child := func(key string) string { return backend.Prepared()[0].Env[key] }
	childTask := firstField(t, r.runAs(child, "task", "create", "--project", projectID, "--title", "child work"))
	if listed := r.run("event", "list", "--project", projectID, "--session", sid); !strings.Contains(listed, childTask) {
		t.Fatalf("the agent's work is not retrievable by session %q:\n%s", sid, listed)
	}
}

// TestTasklessRunCarriesTheRunSession covers the other run shape: a prompt-file
// run is a session of its own too, so it inherits the caller's id when there is
// one and mints one when there is not. The groom path is the explicit-id case
// covered by TestRunProjectInjectsSession.
func TestTasklessRunCarriesTheRunSession(t *testing.T) {
	tests := []struct {
		name      string
		inherited string
		want      string
		minted    bool
	}{
		{name: "inherited from the caller", inherited: "sid-chief", want: "sid-chief"},
		{name: "minted when none is inherited", minted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			r.run("project", "repo", "create", projectID, "web", "--path", t.TempDir())
			if err := os.WriteFile(r.projectPath, []byte("project = \""+projectID+"\"\n"), 0o600); err != nil {
				t.Fatalf("write project config: %v", err)
			}
			promptPath := filepath.Join(t.TempDir(), "prompt.md")
			if err := os.WriteFile(promptPath, []byte("groom\n"), 0o600); err != nil {
				t.Fatalf("write prompt: %v", err)
			}
			getenv := func(key string) string {
				if key == harnesspkg.EnvSession {
					return tt.inherited
				}
				return hermeticGetenv(key)
			}

			env := runEnvArgs(t, r, getenv, "run", "--prompt-file", promptPath,
				"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

			sid := env[harnesspkg.EnvSession]
			if tt.minted {
				if sid == "" {
					t.Fatal("task-less run exported no FACTOTUM_SESSION; want a minted run-scoped id")
				}
			} else if sid != tt.want {
				t.Errorf("env %s = %q, want %q", harnesspkg.EnvSession, sid, tt.want)
			}
		})
	}
}
