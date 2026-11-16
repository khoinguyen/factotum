package app

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/store"
)

// sessionEvents counts the lifecycle events one session carries.
func sessionEvents(t *testing.T, backend store.Backend, sid string) (started, retired int) {
	t.Helper()
	events, err := backend.Events().List(context.Background(), store.EventFilter{})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	for _, event := range events {
		if event.Session != sid {
			continue
		}
		switch event.Kind {
		case core.EventSessionStarted:
			started++
		case core.EventSessionRetired:
			retired++
		}
	}
	return started, retired
}

// TestRunBoundsTheSessionItMints pins option (a): a run that mints a
// run-scoped session (no explicit id, no inherited FACTOTUM_SESSION) brackets
// it with session.started and session.retired, so a finished run's session is
// not alive forever on the sessions page. A session the caller owns - inherited
// from its context or named explicitly - is never retired by the run.
func TestRunBoundsTheSessionItMints(t *testing.T) {
	const inherited = "sid-chief"
	tests := []struct {
		name     string
		project  bool
		inherit  bool
		explicit string
		minted   bool
	}{
		{name: "a task run retires the session it minted", minted: true},
		{name: "a project run retires the session it minted", project: true, minted: true},
		{name: "a task run leaves the inherited session alone", inherit: true},
		{name: "a project run leaves the inherited session alone", project: true, inherit: true},
		{name: "a task run leaves an explicit session alone", explicit: "groom-session"},
		{name: "a project run leaves an explicit session alone", project: true, explicit: "groom-session"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			if tt.inherit {
				ctx = WithCaller(ctx, "builder-t-1", inherited)
			}
			var sid string
			var backend store.Backend
			if tt.project {
				f := newProjectRunFixture(t)
				f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
				outcome, err := f.runCtx(t, ctx, ProjectRunInput{Session: tt.explicit})
				if err != nil {
					t.Fatalf("RunProject() error = %v", err)
				}
				sid, backend = outcome.Session, f.svc.backend
			} else {
				f := newRunFixture(t)
				f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
				outcome, err := f.runCtx(t, ctx, RunInput{Session: tt.explicit})
				if err != nil {
					t.Fatalf("Run() error = %v", err)
				}
				sid, backend = outcome.Session, f.svc.backend
			}

			started, retired := sessionEvents(t, backend, sid)
			if !tt.minted {
				if started != 0 || retired != 0 {
					t.Fatalf("session %q started %d retired %d, want the caller's session untouched", sid, started, retired)
				}
				return
			}
			if started != 1 || retired != 1 {
				t.Fatalf("minted session %q started %d retired %d, want exactly one of each", sid, started, retired)
			}
		})
	}
}

// TestRunRetiresTheSessionOfAFailedHarness pins that a harness failure does not
// leak the session the run minted: the retire is deferred, so the error path
// retires it exactly as the success path does, and a run that failed on its way
// out still leaves no permanently alive session.
func TestRunRetiresTheSessionOfAFailedHarness(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("boom\n"), Stderr: []byte("no widget\n"), ExitCode: 1})

	outcome, err := f.run(t, RunInput{})
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("Run() error = %v, want ErrRunFailed", err)
	}
	started, retired := sessionEvents(t, f.svc.backend, outcome.Session)
	if started != 1 || retired != 1 {
		t.Fatalf("failed run's session %q started %d retired %d, want exactly one of each", outcome.Session, started, retired)
	}
}

// TestRunBracketsItsMintedSession proves the minted session's lifetime spans the
// run: session.started precedes the run-started event and session.retired
// follows the run-finished one, so the recorded lifetime is the run's.
func TestRunBracketsItsMintedSession(t *testing.T) {
	f := newRunFixture(t)
	f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	outcome, err := f.run(t, RunInput{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	events, err := f.svc.backend.Events().List(context.Background(), store.EventFilter{})
	if err != nil {
		t.Fatalf("list events: %v", err)
	}
	index := map[core.EventKind]int{}
	for i, event := range events {
		if event.Session == outcome.Session {
			// The store lists newest first; record the chronological position.
			index[event.Kind] = len(events) - 1 - i
		}
	}
	started, ok := index[core.EventSessionStarted]
	if !ok {
		t.Fatalf("minted session %q recorded no session.started", outcome.Session)
	}
	runStarted, ok := index[core.EventTaskRunStarted]
	if !ok {
		t.Fatalf("run recorded no task.run_started")
	}
	runFinished, ok := index[core.EventTaskRunFinished]
	if !ok {
		t.Fatalf("run recorded no task.run_finished")
	}
	retired, ok := index[core.EventSessionRetired]
	if !ok {
		t.Fatalf("minted session %q recorded no session.retired", outcome.Session)
	}
	if started >= runStarted || runStarted >= runFinished || runFinished >= retired {
		t.Fatalf("event order started=%d runStarted=%d runFinished=%d retired=%d, want the session to bracket the run",
			started, runStarted, runFinished, retired)
	}
}

// TestRunResolvesTheRunSession pins how a run picks the session it and the agent
// it launches are attributed to: an explicit id wins (a groom session judging
// its own work names one), else the caller's session is inherited (a run started
// from inside a loop session rolls up to it, exactly as loop-agent.sh's
// FACTOTUM_SESSION does), else a fresh run-scoped id is minted, so a run started
// from a bare shell still lands on a session timeline instead of escaping it.
func TestRunResolvesTheRunSession(t *testing.T) {
	const inherited = "sid-chief"
	tests := []struct {
		name     string
		explicit string
		inherit  bool
		want     string
		minted   bool
	}{
		{name: "an explicit id wins over the inherited session", explicit: "groom-session", inherit: true, want: "groom-session"},
		{name: "the caller's session is inherited", inherit: true, want: inherited},
		{name: "a bare run mints a run-scoped id", minted: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newRunFixture(t)
			f.backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			ctx := context.Background()
			if tt.inherit {
				ctx = WithCaller(ctx, "builder-t-1", inherited)
			}

			outcome, err := f.runCtx(t, ctx, RunInput{Session: tt.explicit})
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			sid := f.backend.Prepared()[0].Env[harnesspkg.EnvSession]
			if tt.minted {
				if sid == "" {
					t.Fatal("run exported no FACTOTUM_SESSION; want a minted run-scoped id")
				}
			} else if sid != tt.want {
				t.Fatalf("env %s = %q, want %q", harnesspkg.EnvSession, sid, tt.want)
			}
			if outcome.Session != sid {
				t.Errorf("outcome session = %q, want the exported %q", outcome.Session, sid)
			}
			// The run's own events sit on the same session, so the timeline holds
			// the run as well as the work its agent does.
			events, err := f.svc.backend.Events().List(context.Background(), store.EventFilter{})
			if err != nil {
				t.Fatalf("list events: %v", err)
			}
			runs := 0
			for _, event := range events {
				if event.Kind != core.EventTaskRunStarted && event.Kind != core.EventTaskRunFinished {
					continue
				}
				runs++
				if event.Session != sid {
					t.Errorf("event %s session = %q, want %q", event.Kind, event.Session, sid)
				}
			}
			if runs != 2 {
				t.Errorf("recorded %d run events, want 2", runs)
			}
		})
	}
}
