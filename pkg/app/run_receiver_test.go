package app

import (
	"strconv"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

// TestRunCarriesReceiverEnv proves a task run hands the harness the project,
// actor, and task a message receiver registers with.
func TestRunCarriesReceiverEnv(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})

	actor := core.ActorID("act-agent")
	if _, err := f.run(t, RunInput{Actor: &actor}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	prepared := f.backend.Prepared()
	if len(prepared) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(prepared))
	}
	env := prepared[0].Env
	if env[harnesspkg.EnvProject] != string(f.project.ID) {
		t.Errorf("env project = %q, want %q", env[harnesspkg.EnvProject], f.project.ID)
	}
	if env[harnesspkg.EnvActor] != string(actor) {
		t.Errorf("env actor = %q, want %q", env[harnesspkg.EnvActor], actor)
	}
	if env[harnesspkg.EnvTask] != string(f.task.ID) {
		t.Errorf("env task = %q, want %q", env[harnesspkg.EnvTask], f.task.ID)
	}
}

// TestRunWithoutActorHasNoReceiverEnv proves a run that names no actor installs
// no receiver: the plugin is then not staged and the session is unmanaged. The
// run's project is still pinned, so the session's child ft resolves that project
// rather than one the checkout pins.
func TestRunWithoutActorHasNoReceiverEnv(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})

	if _, err := f.run(t, RunInput{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if harnesspkg.MessagingEnabled(env) {
		t.Fatalf("env %v enables messaging, want disabled without an actor", env)
	}
	if env[harnesspkg.EnvProject] != string(f.project.ID) {
		t.Errorf("env %s = %q, want the run's project pinned", harnesspkg.EnvProject, env[harnesspkg.EnvProject])
	}
}

// TestRunCarriesHubReceiverEnv proves a run configured with a messaging hub
// hands the harness the hub URL and token, so a launched remote receiver speaks
// the HTTP transport instead of falling back to the local ft.
func TestRunCarriesHubReceiverEnv(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})

	actor := core.ActorID("act-agent")
	if _, err := f.run(t, RunInput{
		Actor:      &actor,
		MsgURL:     "http://hub:8484",
		ServeToken: "tok",
	}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if env[harnesspkg.EnvMsgURL] != "http://hub:8484" {
		t.Errorf("env %s = %q, want the configured hub URL", harnesspkg.EnvMsgURL, env[harnesspkg.EnvMsgURL])
	}
	if env[harnesspkg.EnvServeToken] != "tok" {
		t.Errorf("env %s = %q, want the configured serve token", harnesspkg.EnvServeToken, env[harnesspkg.EnvServeToken])
	}
}

// TestRunCarriesStoreEnv proves a task run hands the harness the caller's
// resolved store (backend and options) so the session's child ft reads the same
// store, never falling back to a project or user config it finds in the
// checkout. It must coexist with the receiver env a task run already carries.
func TestRunCarriesStoreEnv(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})

	actor := core.ActorID("act-agent")
	storeEnv := map[string]string{
		harnesspkg.EnvStore:     "jsonfile",
		harnesspkg.EnvStoreOpts: "path=/tmp/lab/db.json",
	}
	if _, err := f.run(t, RunInput{Actor: &actor, StoreEnv: storeEnv}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if env[harnesspkg.EnvStore] != "jsonfile" {
		t.Errorf("env %s = %q, want the configured backend", harnesspkg.EnvStore, env[harnesspkg.EnvStore])
	}
	if env[harnesspkg.EnvStoreOpts] != "path=/tmp/lab/db.json" {
		t.Errorf("env %s = %q, want the configured options", harnesspkg.EnvStoreOpts, env[harnesspkg.EnvStoreOpts])
	}
	if env[harnesspkg.EnvProject] != string(f.project.ID) {
		t.Errorf("env %s = %q, want the receiver project retained", harnesspkg.EnvProject, env[harnesspkg.EnvProject])
	}
}

// TestRunWithoutStoreEnvOmitsIt proves a run with no store env configured does
// not set a bogus empty store: the caller only injects what it resolved.
func TestRunWithoutStoreEnvOmitsIt(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})

	if _, err := f.run(t, RunInput{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if _, ok := env[harnesspkg.EnvStore]; ok {
		t.Errorf("env %s = %q, want no store backend when unset", harnesspkg.EnvStore, env[harnesspkg.EnvStore])
	}
	if _, ok := env[harnesspkg.EnvStoreOpts]; ok {
		t.Errorf("env %s = %q, want no store options when unset", harnesspkg.EnvStoreOpts, env[harnesspkg.EnvStoreOpts])
	}
}

// TestRunWithoutHubOmitsHubEnv proves a run with no hub configured falls back
// cleanly: no hub URL or token reaches the harness, so its receiver talks to the
// local ft store. A token with no URL is not a hub.
func TestRunWithoutHubOmitsHubEnv(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})

	actor := core.ActorID("act-agent")
	if _, err := f.run(t, RunInput{Actor: &actor, ServeToken: "tok"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	env := f.backend.Prepared()[0].Env
	if _, ok := env[harnesspkg.EnvMsgURL]; ok {
		t.Errorf("env %s = %q, want no hub URL when unset", harnesspkg.EnvMsgURL, env[harnesspkg.EnvMsgURL])
	}
	if _, ok := env[harnesspkg.EnvServeToken]; ok {
		t.Errorf("env %s = %q, want no serve token without a hub URL", harnesspkg.EnvServeToken, env[harnesspkg.EnvServeToken])
	}
}

// TestRunIncompleteHubOmitsHubEnv proves a hub URL with no usable token is
// treated as no hub. The transport is always token-gated, so injecting the URL
// with a missing or whitespace-only token would make a launched receiver speak
// it unauthenticated and fail-close every message; both halves are required. `ft
// run` warns about the half-configured hub, so the fallback to the local ft
// store is not silent.
func TestRunIncompleteHubOmitsHubEnv(t *testing.T) {
	for _, token := range []string{"", "   ", "\t"} {
		t.Run("token="+strconv.Quote(token), func(t *testing.T) {
			f := newRunFixture(t)
			f.backend.Program(isolation.ExecResult{Stdout: []byte("ok\n"), ExitCode: 0})

			actor := core.ActorID("act-agent")
			if _, err := f.run(t, RunInput{Actor: &actor, MsgURL: "http://hub:8484", ServeToken: token}); err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			env := f.backend.Prepared()[0].Env
			if _, ok := env[harnesspkg.EnvMsgURL]; ok {
				t.Errorf("env %s = %q, want no hub URL without a usable token", harnesspkg.EnvMsgURL, env[harnesspkg.EnvMsgURL])
			}
			if _, ok := env[harnesspkg.EnvServeToken]; ok {
				t.Errorf("env %s = %q, want no serve token without a usable one", harnesspkg.EnvServeToken, env[harnesspkg.EnvServeToken])
			}
		})
	}
}
