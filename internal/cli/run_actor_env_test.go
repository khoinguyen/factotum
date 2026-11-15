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

// runAs executes one command through the runner's fakes with the given
// environment getter, so a test can drive an invocation whose environment is the
// one a launcher would export (for example a run's own env).
func (r *runner) runAs(getenv func(string) string, args ...string) string {
	r.t.Helper()
	prev := r.getenv
	r.getenv = getenv
	defer func() { r.getenv = prev }()
	return r.run(args...)
}

// runEnv launches one `ft run` against a fake backend and returns the
// environment the harness received.
func runEnv(t *testing.T, r *runner, taskID string, getenv func(string) string, extra ...string) map[string]string {
	t.Helper()
	args := append([]string{"run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir()}, extra...)
	return runEnvArgs(t, r, getenv, args...)
}

// runEnvArgs runs one `ft run` invocation with the given args against a fake
// backend and returns the environment the harness received, so a test can drive
// the task-less prompt path as well as a task run.
func runEnvArgs(t *testing.T, r *runner, getenv func(string) string, args ...string) map[string]string {
	t.Helper()
	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode2")

	if getenv == nil {
		r.run(args...)
	} else {
		r.runAs(getenv, args...)
	}
	specs := backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	return specs[0].Env
}

// TestRunExportsActorIdentityForABareSend pins the parity the loop needs: a run
// that names a loop role exports both halves of the identity the chief's wrapper
// exports - FACTOTUM_ACTOR for the receiver to register with, and
// FACTOTUM_DEFAULT_ACTOR so a bare `ft msg send` in the launched session is
// attributed to the role instead of stored as a system message. A role needs no
// Actor record: the loop registers a role as a run, not as a store actor.
func TestRunExportsActorIdentityForABareSend(t *testing.T) {
	for _, role := range []string{"builder-t-1", "reviewer-t-1", "qa-t-1", "chief"} {
		t.Run(role, func(t *testing.T) {
			r := newRunner(t)
			_, taskID := runContext(t, r)

			env := runEnv(t, r, taskID, nil, "--actor", role)

			if got := env[harnesspkg.EnvActor]; got != role {
				t.Errorf("env %s = %q, want %q", harnesspkg.EnvActor, got, role)
			}
			if got := env[harnesspkg.EnvDefaultActor]; got != role {
				t.Errorf("env %s = %q, want %q", harnesspkg.EnvDefaultActor, got, role)
			}
		})
	}
}

// TestTasklessRunExportsActorIdentity covers the other run shape: a prompt-file
// run has no task, but it launches the same session, so it must export the same
// identity pair or the task-less agent's bare send is anonymous.
func TestTasklessRunExportsActorIdentity(t *testing.T) {
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

	env := runEnvArgs(t, r, nil, "run", "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir(), "--actor", "builder-t-1")

	if got := env[harnesspkg.EnvActor]; got != "builder-t-1" {
		t.Errorf("env %s = %q, want %q", harnesspkg.EnvActor, got, "builder-t-1")
	}
	if got := env[harnesspkg.EnvDefaultActor]; got != "builder-t-1" {
		t.Errorf("env %s = %q, want %q", harnesspkg.EnvDefaultActor, got, "builder-t-1")
	}
}

// TestRunInheritsTheDefaultActorForItsChild is the other launch path: a session
// the wrapper started carries FACTOTUM_DEFAULT_ACTOR, and a run it launches with
// no --actor must hand its own child the same identity, so the child's bare send
// is attributed rather than anonymous one level down. The configured-default
// grammar is wider than --actor's, so grooming-agent - which is only ever a
// default - must inherit too.
func TestRunInheritsTheDefaultActorForItsChild(t *testing.T) {
	for _, role := range []string{"builder-t-1", "grooming-agent-t-1"} {
		t.Run(role, func(t *testing.T) {
			r := newRunner(t)
			_, taskID := runContext(t, r)

			inherited := func(key string) string {
				if key == "FACTOTUM_DEFAULT_ACTOR" {
					return role
				}
				return ""
			}
			env := runEnv(t, r, taskID, inherited)

			if got := env[harnesspkg.EnvActor]; got != role {
				t.Errorf("env %s = %q, want the inherited role %q", harnesspkg.EnvActor, got, role)
			}
			if got := env[harnesspkg.EnvDefaultActor]; got != role {
				t.Errorf("env %s = %q, want the inherited role %q", harnesspkg.EnvDefaultActor, got, role)
			}
		})
	}
}

// TestRunExportsRegisteredActorIdentity proves a registered actor keeps its
// record id under both keys, so a run names the same identity a direct ft call
// under that actor would.
func TestRunExportsRegisteredActorIdentity(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	actorID := firstField(t, r.run("actor", "create", "claude", "--kind", "agent"))

	env := runEnv(t, r, taskID, nil, "--actor", "claude")

	if got := env[harnesspkg.EnvActor]; got != actorID {
		t.Errorf("env %s = %q, want the record id %q", harnesspkg.EnvActor, got, actorID)
	}
	if got := env[harnesspkg.EnvDefaultActor]; got != actorID {
		t.Errorf("env %s = %q, want the record id %q", harnesspkg.EnvDefaultActor, got, actorID)
	}
}

// TestRunWithUnregisteredActorExportsNoIdentity keeps the drop rule: an --actor
// that is neither a record nor a loop role is already dropped (with a warning)
// by the caller binding, so the run must not hand the child an identity either.
func TestRunWithUnregisteredActorExportsNoIdentity(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	env := runEnv(t, r, taskID, nil, "--actor", "ghost")

	if got := env[harnesspkg.EnvActor]; got != "" {
		t.Errorf("env %s = %q, want none for a dropped actor", harnesspkg.EnvActor, got)
	}
	if got := env[harnesspkg.EnvDefaultActor]; got != "" {
		t.Errorf("env %s = %q, want none for a dropped actor", harnesspkg.EnvDefaultActor, got)
	}
}

// TestBareSendInsideARunIsAttributed is the end-to-end pin for the whole
// contract: take the environment `ft run` exports, run `ft msg send` under it
// with no --from, and the message is stored with the run's actor. It exercises
// the real send path against the same store, so a key the child ft actually
// reads (not merely one the run writes) is what makes the assertion pass.
func TestBareSendInsideARunIsAttributed(t *testing.T) {
	r := newRunner(t)
	projectID, taskID := runContext(t, r)

	env := runEnv(t, r, taskID, nil, "--actor", "builder-t-1")

	childEnv := func(key string) string { return env[key] }
	sent := r.runAs(childEnv, "msg", "send", "actor:chief", "-b", "status")
	if !strings.Contains(sent, "project: "+projectID) {
		t.Fatalf("send resolved the wrong project from the run env:\n%s", sent)
	}
	got := r.run("msg", "get", firstField(t, sent))
	if !strings.Contains(got, "from: builder-t-1") {
		t.Fatalf("a bare send inside a run is unattributed:\n%s\nwant from: builder-t-1", got)
	}
}
