package up

import (
	"context"
	"fmt"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// SessionResult is what one groom session produced: the tasks it created and
// whether it deferred product questions (advisory gate). Detail is a one-line
// summary for the event log.
type SessionResult struct {
	Produced []core.TicketID
	Deferred bool
	Detail   string
}

// SessionFunc runs one groom session for a pipeline scoped to its capture and
// owns every durable output (the artifacts and the session manifest). The
// controller-generated sessionID names the deterministic output paths, exactly
// as `ft groom` names them. A production SessionFunc launches the harness; a
// test supplies a stub that writes the manifest.
type SessionFunc func(ctx context.Context, p *core.Pipeline, sessionID string) (SessionResult, error)

// GroomRunner is the groom stage runner. It resolves the session id (reusing a
// session already recorded for the capture, so a restart is idempotent),
// delegates the agent session to Session, and maps the outcome to a StageResult.
type GroomRunner struct {
	// DataDir is where sessions live; production passes the project data dir.
	DataDir string
	// IDs mints the session id. Required when Session is set.
	IDs app.IDGen
	// Session runs the agent session. Required.
	Session SessionFunc
}

// Run implements StageRunner.
func (g *GroomRunner) Run(ctx context.Context, p *core.Pipeline) (StageResult, error) {
	if g.Session == nil {
		return StageResult{}, fmt.Errorf("up: groom runner has no session function")
	}
	// A session already recorded for this capture means a prior run finished but
	// the pipeline did not advance (a crash between the manifest write and the
	// pipeline save). Reuse it instead of grooming twice.
	if rec, ok, err := g.existingCaptureSession(p.CaptureID); err != nil {
		return StageResult{}, err
	} else if ok {
		return StageResult{
			Done:     true,
			Session:  rec.ID,
			Produced: ticketIDs(rec.Produced),
			Detail:   fmt.Sprintf("reused groom session %s", rec.ID),
		}, nil
	}

	sessionID := g.IDs.NewID("groom")
	outcome, err := g.Session(ctx, p, sessionID)
	if err != nil {
		return StageResult{}, err
	}
	gate := core.GateNone
	if outcome.Deferred {
		gate = core.GateGroomQuestions
	}
	return StageResult{
		Done:     true,
		Session:  sessionID,
		Produced: outcome.Produced,
		Gate:     gate,
		Detail:   outcome.Detail,
	}, nil
}

// existingCaptureSession returns the recorded session whose scope names the
// capture, if any. It is the idempotency check for a resume.
func (g *GroomRunner) existingCaptureSession(capture core.TicketID) (groom.SessionRecord, bool, error) {
	sessions, err := groom.ListSessions(g.DataDir)
	if err != nil {
		return groom.SessionRecord{}, false, err
	}
	for _, rec := range sessions {
		for _, item := range rec.Scope {
			if item.ID == string(capture) {
				return rec, true, nil
			}
		}
	}
	return groom.SessionRecord{}, false, nil
}

// ticketIDs converts a session record's produced ids into core ticket ids.
func ticketIDs(ids []string) []core.TicketID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]core.TicketID, 0, len(ids))
	for _, id := range ids {
		out = append(out, core.TicketID(id))
	}
	return out
}
