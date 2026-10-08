package app

import (
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
// no receiver: the plugin is then not staged and the session is unmanaged.
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
