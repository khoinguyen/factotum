package app

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/store"
)

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
