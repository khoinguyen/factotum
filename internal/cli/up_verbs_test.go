package cli

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

// TestUpOperatorVerbsControlPipeline exercises the pause/resume/cancel verbs on
// the real CLI path: a pipeline enqueued by `ft up start` is held, resumed, and
// cancelled, each verb reflecting its state on the durable pipeline.
func TestUpOperatorVerbsControlPipeline(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a spark", "-b", "context"))
	pipelineID := firstField(t, r.run("up", "start", ideaID, "-p", projectID))

	if paused := r.run("up", "pause", pipelineID, "-p", projectID); !strings.Contains(paused, "state: paused") {
		t.Fatalf("up pause should park the pipeline:\n%s", paused)
	}
	if resumed := r.run("up", "resume", pipelineID, "-p", projectID); !strings.Contains(resumed, "state: queued") {
		t.Fatalf("up resume should return the pipeline to its held phase:\n%s", resumed)
	}
	if cancelled := r.run("up", "cancel", pipelineID, "-p", projectID); !strings.Contains(cancelled, "state: cancelled") {
		t.Fatalf("up cancel should terminate the pipeline:\n%s", cancelled)
	}
}

func TestUpAnswerRequiresFile(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	_, stderr := r.runSplit("up", "answer", "pl-1", "-p", projectID)
	if !strings.Contains(stderr, "--body-file required") {
		t.Fatalf("up answer without -f should require the file:\n%s", stderr)
	}
}

func TestUpMergeRejectsPipelineNotAtGate(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "a spark"))
	pipelineID := firstField(t, r.run("up", "start", ideaID, "-p", projectID))

	if err := runUpExpectError(r, "up", "merge", pipelineID, "-p", projectID); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("up merge on a pipeline not at merge_approval = %v, want ErrConflict", err)
	}
}

// runUpExpectError runs a command and returns its error, so a test can assert a
// non-usage failure's sentinel (the runner's run() fatals on those).
func runUpExpectError(r *runner, args ...string) error {
	var stdout, stderr bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, r.getenv)
	base := r.setup(deps)
	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	return root.Execute()
}

// TestApproveGroomSessionReRecordsReview proves `ft up approve`'s side effect:
// it re-records an approving verdict on the session and unblocks exactly the
// tasks a prior needs-rework review held, so the resumed review stage advances.
func TestApproveGroomSessionReRecordsReview(t *testing.T) {
	ctx := context.Background()
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	clock := app.SystemClock{}
	ids := app.RandomIDGen{}
	projects := app.NewProjectService(backend, clock, ids)
	tasks := app.NewTicketService(backend, clock, ids)

	project, err := projects.Create(ctx, "Acme", "acme", nil)
	if err != nil {
		t.Fatalf("Create(project) error = %v", err)
	}
	capture, err := tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindIdea, Title: "spark"})
	if err != nil {
		t.Fatalf("Add(capture) error = %v", err)
	}
	task, err := tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: core.KindTask, Title: "produced"})
	if err != nil {
		t.Fatalf("Add(task) error = %v", err)
	}
	if _, err := tasks.AddDep(ctx, task.ID, capture.ID); err != nil {
		t.Fatalf("AddDep() error = %v", err)
	}
	if _, err := tasks.SetStatus(ctx, task.ID, core.StatusBlocked); err != nil {
		t.Fatalf("SetStatus(blocked) error = %v", err)
	}

	dataDir := t.TempDir()
	if err := groom.WriteSession(dataDir, groom.SessionRecord{
		ID:            "groom-1",
		Project:       string(project.ID),
		Produced:      []string{string(task.ID)},
		Blocked:       []string{string(task.ID)},
		ReviewVerdict: string(groom.VerdictNeedsRework),
	}); err != nil {
		t.Fatalf("WriteSession() error = %v", err)
	}

	target := groomTarget{dataDir: dataDir, backend: backend, tasks: tasks}
	if err := approveGroomSession(ctx, target, &core.Pipeline{Session: "groom-1"}); err != nil {
		t.Fatalf("approveGroomSession() error = %v", err)
	}

	unblocked, err := tasks.Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("Get(task) error = %v", err)
	}
	if unblocked.Status != core.StatusTodo {
		t.Fatalf("produced task status = %q, want todo (unblocked)", unblocked.Status)
	}
	record, err := groom.ReadSession(dataDir, "groom-1")
	if err != nil {
		t.Fatalf("ReadSession() error = %v", err)
	}
	if record.ReviewVerdict != string(groom.VerdictApprove) {
		t.Fatalf("session verdict = %q, want approve", record.ReviewVerdict)
	}
	if len(record.Blocked) != 0 {
		t.Fatalf("session blocked = %v, want none", record.Blocked)
	}
}
