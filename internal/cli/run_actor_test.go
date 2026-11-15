package cli

import (
	"strings"
	"testing"

	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestRunUnregisteredActorWarnsNoReceiver pins the failure mode the run had no
// signal for: an --actor that names no Actor record and no loop role leaves the
// run with no receiver identity, so the launched session can never be reached
// over ft msg. The run still launches - the work is worth doing - but it says the
// receiver is missing and names the fix. A role-shaped name (qa-ghost) is the
// silent case, because bindCaller's role grammar trusts it and warns nothing; a
// plain typo (ghost) is dropped by bindCaller and warned about there, and the run
// adds the receiver consequence.
func TestRunUnregisteredActorWarnsNoReceiver(t *testing.T) {
	for _, name := range []string{"qa-ghost", "ghost"} {
		t.Run(name, func(t *testing.T) {
			r := newRunner(t)
			_, taskID := runContext(t, r)

			backend := isofake.New("sandbox")
			backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			out, stderr := r.runSplit("run", taskID, "--actor", name,
				"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

			if !strings.Contains(stderr, `no message receiver will register for actor "`+name+`"`) {
				t.Fatalf("run stderr missing the no-receiver warning:\n%s", stderr)
			}
			if !strings.Contains(stderr, `ft actor create "`+name+`"`) {
				t.Fatalf("run stderr missing the fix hint:\n%s", stderr)
			}
			if !strings.Contains(out, "run: finished") {
				t.Fatalf("the run did not launch; it should warn, not fail:\n%s", out)
			}
			prepared := backend.Prepared()
			if len(prepared) != 1 {
				t.Fatalf("Prepare called %d times, want 1", len(prepared))
			}
			if harnesspkg.MessagingEnabled(prepared[0].Env) {
				t.Fatalf("harness env enables messaging for an unresolvable actor: %v", prepared[0].Env)
			}
		})
	}
}

// TestRunLoopRoleInstallsReceiver pins the exemption from the other side: a loop
// role is a mailbox, not an Actor record, so it resolves without registration.
// The run registers its receiver under the role id and warns about nothing, so a
// builder/reviewer/qa loop keeps working exactly as before.
func TestRunLoopRoleInstallsReceiver(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("run", taskID, "--actor", "builder-t-abc",
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	if strings.Contains(stderr, "no message receiver will register") {
		t.Fatalf("a loop role should install a receiver without a warning:\n%s", stderr)
	}
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	if got := prepared[0].Env[harnesspkg.EnvActor]; got != "builder-t-abc" {
		t.Fatalf("harness env %s = %q, want builder-t-abc", harnesspkg.EnvActor, got)
	}
	if !harnesspkg.MessagingEnabled(prepared[0].Env) {
		t.Fatalf("harness env does not enable messaging for a loop role: %v", prepared[0].Env)
	}
}
