package cli

import (
	"strings"
	"testing"

	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// runActorRun drives one `ft run <task>` over the fake backend and harness and
// returns what the operator saw plus the harness env the run prepared, so a test
// can assert both the warning and whether a receiver identity was exported.
func runActorRun(t *testing.T, r *runner, taskID string, args ...string) (string, string, map[string]string) {
	t.Helper()
	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	base := []string{"run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir()}
	out, stderr := r.runSplit(append(base, args...)...)
	prepared := backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	return out, stderr, prepared[0].Env
}

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

			out, stderr, env := runActorRun(t, r, taskID, "--actor", name)

			if !strings.Contains(stderr, `no message receiver will register for actor "`+name+`"`) {
				t.Fatalf("run stderr missing the no-receiver warning:\n%s", stderr)
			}
			if !strings.Contains(stderr, `ft actor create "`+name+`"`) {
				t.Fatalf("run stderr missing the fix hint:\n%s", stderr)
			}
			if !strings.Contains(out, "run: finished") {
				t.Fatalf("the run did not launch; it should warn, not fail:\n%s", out)
			}
			if harnesspkg.MessagingEnabled(env) {
				t.Fatalf("harness env enables messaging for an unresolvable actor: %v", env)
			}
		})
	}
}

// TestRunLoopRoleInstallsReceiver pins the exemption from the other side: a loop
// role is a mailbox, not an Actor record, so it resolves without registration.
// The run registers its receiver under the role id and warns about nothing.
func TestRunLoopRoleInstallsReceiver(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	_, stderr, env := runActorRun(t, r, taskID, "--actor", "builder-t-abc")

	if strings.Contains(stderr, "no message receiver will register") {
		t.Fatalf("a loop role should install a receiver without a warning:\n%s", stderr)
	}
	if got := env[harnesspkg.EnvActor]; got != "builder-t-abc" {
		t.Fatalf("harness env %s = %q, want builder-t-abc", harnesspkg.EnvActor, got)
	}
	if !harnesspkg.MessagingEnabled(env) {
		t.Fatalf("harness env does not enable messaging for a loop role: %v", env)
	}
}

// TestRunUnregisteredCallerWarnsNoReceiver covers the other source of a receiver
// identity: a detected caller that resolves becomes the run's actor
// (bindCaller writes ActorRef), so an unregistered FACTOTUM_CALLER is the same
// silently unreachable run one env var over and takes the same warning.
func TestRunUnregisteredCallerWarnsNoReceiver(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.getenv = func(key string) string {
		if key == "FACTOTUM_CALLER" {
			return "ghost"
		}
		return ""
	}

	out, stderr, env := runActorRun(t, r, taskID)

	if !strings.Contains(stderr, `caller "ghost" is not a registered actor`) {
		t.Fatalf("run stderr missing the unregistered-caller warning:\n%s", stderr)
	}
	if !strings.Contains(stderr, `no message receiver will register for actor "ghost"`) {
		t.Fatalf("run stderr missing the no-receiver warning:\n%s", stderr)
	}
	if !strings.Contains(out, "run: finished") {
		t.Fatalf("the run did not launch; it should warn, not fail:\n%s", out)
	}
	if harnesspkg.MessagingEnabled(env) {
		t.Fatalf("harness env enables messaging for an unregistered caller: %v", env)
	}
}

// TestRunRegisteredCallerInstallsReceiver pins the mirror image: a caller that
// names a registered actor is a usable receiver identity, so the run installs the
// receiver without a no-receiver warning.
func TestRunRegisteredCallerInstallsReceiver(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.run("actor", "create", "--kind", "agent", "claude")
	r.getenv = func(key string) string {
		if key == "FACTOTUM_CALLER" {
			return "claude"
		}
		return ""
	}

	_, stderr, env := runActorRun(t, r, taskID)

	if strings.Contains(stderr, "no message receiver will register") {
		t.Fatalf("a registered caller should install a receiver without a warning:\n%s", stderr)
	}
	if got := env[harnesspkg.EnvActor]; got != "claude" {
		t.Fatalf("harness env %s = %q, want claude", harnesspkg.EnvActor, got)
	}
	if !harnesspkg.MessagingEnabled(env) {
		t.Fatalf("harness env does not enable messaging for a registered caller: %v", env)
	}
}

// TestRunAttributionOnlyRoleGetsNoReceiver keeps the run on bindCaller's decision:
// grooming-agent-<artifact> is a role only a configured default may carry, so an
// explicit --actor naming it is dropped for attribution. The run must not then
// install a receiver for the identity it just refused to attribute - an
// "unattributed" warning next to a live mailbox is contradictory. It warns that
// the receiver is missing instead.
func TestRunAttributionOnlyRoleGetsNoReceiver(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	_, stderr, env := runActorRun(t, r, taskID, "--actor", "grooming-agent-groom-abc")

	if !strings.Contains(stderr, `actor "grooming-agent-groom-abc" is not a registered actor`) {
		t.Fatalf("run stderr missing the attribution warning:\n%s", stderr)
	}
	if !strings.Contains(stderr, `no message receiver will register for actor "grooming-agent-groom-abc"`) {
		t.Fatalf("run stderr missing the no-receiver warning:\n%s", stderr)
	}
	if harnesspkg.MessagingEnabled(env) {
		t.Fatalf("harness env enables messaging for a dropped identity: %v", env)
	}
}

// TestRunActorNameInstallsReceiverUnderItsSluggedID pins that the fix hint is
// actionable for a name whose id differs: `ft actor create "Qa Ghost"` mints id
// qa-ghost, and `--actor "Qa Ghost"` afterwards resolves by name to that id, so
// the run is silent and registers under the id.
func TestRunActorNameInstallsReceiverUnderItsSluggedID(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	r.run("actor", "create", "--kind", "agent", "Qa Ghost")

	_, stderr, env := runActorRun(t, r, taskID, "--actor", "Qa Ghost")

	if strings.Contains(stderr, "no message receiver will register") {
		t.Fatalf("a registered name should install a receiver without a warning:\n%s", stderr)
	}
	if got := env[harnesspkg.EnvActor]; got != "qa-ghost" {
		t.Fatalf("harness env %s = %q, want qa-ghost", harnesspkg.EnvActor, got)
	}
	if !harnesspkg.MessagingEnabled(env) {
		t.Fatalf("harness env does not enable messaging for a registered name: %v", env)
	}
}
