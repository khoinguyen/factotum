package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/check"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

// stubCheck is a programmable, call-counting check.
type stubCheck struct {
	name    string
	version string
	result  check.Result
	err     error
	calls   int
}

func (s *stubCheck) Name() string    { return s.name }
func (s *stubCheck) Version() string { return s.version }

func (s *stubCheck) Run(_ context.Context, spec check.Spec) (check.Result, error) {
	s.calls++
	if s.err != nil {
		return check.Result{}, s.err
	}
	result := s.result
	result.Check = s.name
	result.CheckVersion = s.version
	result.ContentHash = spec.Hash()
	if result.Verdict == "" {
		result.Verdict = check.Ready
	}
	return result, nil
}

type checkFixture struct {
	backend store.Backend
	svc     *CheckService
	tasks   *TaskService
	actors  *ActorService
	stub    *stubCheck
	task    *core.Task
}

func newCheckFixture(t *testing.T, stub *stubCheck) *checkFixture {
	t.Helper()
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &seqIDs{}
	projects := NewProjectService(backend, clock, ids)
	tasks := NewTaskService(backend, clock, ids)
	actors := NewActorService(backend, clock, ids)
	project, err := projects.Create(context.Background(), "Acme", "demo", nil)
	if err != nil {
		t.Fatalf("project create: %v", err)
	}
	task, err := tasks.Add(context.Background(), TaskInput{ProjectID: project.ID, Title: "work", Description: "a body"})
	if err != nil {
		t.Fatalf("task add: %v", err)
	}
	reg := registry.New[check.Check]()
	if err := reg.Register(stub.Name(), stub); err != nil {
		t.Fatalf("register: %v", err)
	}
	return &checkFixture{
		backend: backend,
		svc:     NewCheckService(backend, clock, ids, reg),
		tasks:   tasks,
		actors:  actors,
		stub:    stub,
		task:    task,
	}
}

func (f *checkFixture) human(t *testing.T) *core.Actor {
	t.Helper()
	if actor, err := f.actors.Resolve(context.Background(), "Khoi"); err == nil {
		return actor
	}
	actor, err := f.actors.Add(context.Background(), core.ActorHuman, "Khoi")
	if err != nil {
		t.Fatalf("actor add: %v", err)
	}
	return actor
}

func TestCheckRunCachesAndCachedNeverRuns(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1", result: check.Result{Verdict: check.NeedsGrooming}}
	f := newCheckFixture(t, stub)

	results, err := f.svc.Run(context.Background(), f.task.ID, nil, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 1 || results[0].Verdict != check.NeedsGrooming || !results[0].Checked {
		t.Fatalf("Run() = %+v, want one checked needs_grooming result", results)
	}
	if stub.calls != 1 {
		t.Fatalf("check calls = %d, want 1", stub.calls)
	}

	cached, err := f.svc.Cached(context.Background(), f.task.ID, nil)
	if err != nil {
		t.Fatalf("Cached() error = %v", err)
	}
	if len(cached) != 1 || !cached[0].Checked || cached[0].Verdict != check.NeedsGrooming || cached[0].Stale {
		t.Fatalf("Cached() = %+v, want the fresh cached result", cached)
	}
	if stub.calls != 1 {
		t.Fatalf("Cached() ran the check: calls = %d", stub.calls)
	}

	rerun, err := f.svc.Run(context.Background(), f.task.ID, nil, false)
	if err != nil {
		t.Fatalf("Run() again error = %v", err)
	}
	if stub.calls != 1 || rerun[0].Verdict != check.NeedsGrooming {
		t.Fatalf("unchanged content re-ran the check: calls = %d, result = %+v", stub.calls, rerun)
	}
}

func TestCheckCachedAbsentIsNotChecked(t *testing.T) {
	f := newCheckFixture(t, &stubCheck{name: "stub", version: "1"})
	cached, err := f.svc.Cached(context.Background(), f.task.ID, nil)
	if err != nil {
		t.Fatalf("Cached() error = %v", err)
	}
	if len(cached) != 1 || cached[0].Checked {
		t.Fatalf("Cached() = %+v, want a not-checked result", cached)
	}
}

func TestCheckEditingBodyInvalidatesButNoteAndLabelDoNot(t *testing.T) {
	f := newCheckFixture(t, &stubCheck{name: "stub", version: "1"})
	ctx := context.Background()
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if _, err := f.tasks.AddNote(ctx, f.task.ID, NoteInput{Body: "a comment"}); err != nil {
		t.Fatalf("AddNote() error = %v", err)
	}
	labels := []string{"human-decided"}
	if _, err := f.tasks.Set(ctx, f.task.ID, TaskSet{Labels: labels}); err != nil {
		t.Fatalf("Set(labels) error = %v", err)
	}
	cached, _ := f.svc.Cached(ctx, f.task.ID, nil)
	if cached[0].Stale {
		t.Fatalf("a note or label invalidated the verdict: %+v", cached[0])
	}

	body := "a changed body"
	if _, err := f.tasks.Set(ctx, f.task.ID, TaskSet{Description: &body}); err != nil {
		t.Fatalf("Set(body) error = %v", err)
	}
	cached, _ = f.svc.Cached(ctx, f.task.ID, nil)
	if !cached[0].Stale {
		t.Fatalf("editing the body did not mark the verdict stale: %+v", cached[0])
	}
}

func TestCheckForceRecomputesButNeverDowngradesReady(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1", result: check.Result{Verdict: check.Ready}}
	f := newCheckFixture(t, stub)
	ctx := context.Background()
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// A later run now reports needs_grooming, but unchanged content with a
	// cached ready verdict must not be downgraded.
	stub.result = check.Result{Verdict: check.NeedsGrooming}
	forced, err := f.svc.Run(ctx, f.task.ID, nil, true)
	if err != nil {
		t.Fatalf("Run(force) error = %v", err)
	}
	if forced[0].Verdict != check.Ready {
		t.Fatalf("force downgraded a ready verdict: %+v", forced[0])
	}
	if stub.calls != 1 {
		t.Fatalf("force re-ran a ready verdict: calls = %d", stub.calls)
	}
}

func TestCheckForceRecomputesANonReadyVerdict(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1", result: check.Result{Verdict: check.NeedsGrooming}}
	f := newCheckFixture(t, stub)
	ctx := context.Background()
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	stub.result = check.Result{Verdict: check.Ready}
	forced, err := f.svc.Run(ctx, f.task.ID, nil, true)
	if err != nil {
		t.Fatalf("Run(force) error = %v", err)
	}
	if forced[0].Verdict != check.Ready || stub.calls != 2 {
		t.Fatalf("force did not recompute: calls = %d, result = %+v", stub.calls, forced[0])
	}
}

func TestCheckHumanDecisionOverridesWithoutRunning(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1", result: check.Result{Verdict: check.NeedsGrooming}}
	f := newCheckFixture(t, stub)
	ctx := context.Background()
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	human := f.human(t)
	if _, err := f.svc.Decide(ctx, f.task.ID, "stub", human, "shipped before", false); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}

	results, err := f.svc.Run(ctx, f.task.ID, nil, false)
	if err != nil {
		t.Fatalf("Run() after decide error = %v", err)
	}
	if !results[0].Override || results[0].Verdict != check.Ready || results[0].DecidedBy != string(human.ID) {
		t.Fatalf("Run() = %+v, want the human override", results[0])
	}
	if stub.calls != 1 {
		t.Fatalf("override ran the judge: calls = %d", stub.calls)
	}

	cached, _ := f.svc.Cached(ctx, f.task.ID, nil)
	if !cached[0].Override || cached[0].Stale {
		t.Fatalf("Cached() = %+v, want a fresh override", cached[0])
	}
}

func TestCheckStaleOverrideReevaluates(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1", result: check.Result{Verdict: check.NeedsGrooming}}
	f := newCheckFixture(t, stub)
	ctx := context.Background()
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := f.svc.Decide(ctx, f.task.ID, "stub", f.human(t), "ok", false); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	body := "new body"
	if _, err := f.tasks.Set(ctx, f.task.ID, TaskSet{Description: &body}); err != nil {
		t.Fatalf("Set(body) error = %v", err)
	}
	cached, _ := f.svc.Cached(ctx, f.task.ID, nil)
	if !cached[0].Override || !cached[0].Stale {
		t.Fatalf("Cached() = %+v, want a stale override", cached[0])
	}
	results, err := f.svc.Run(ctx, f.task.ID, nil, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if results[0].Override || stub.calls != 2 {
		t.Fatalf("stale override did not re-evaluate: calls = %d, result = %+v", stub.calls, results[0])
	}
}

func TestCheckClearRestoresJudgeVerdict(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1", result: check.Result{Verdict: check.NeedsGrooming}}
	f := newCheckFixture(t, stub)
	ctx := context.Background()
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := f.svc.Decide(ctx, f.task.ID, "stub", f.human(t), "ok", false); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if _, err := f.svc.Decide(ctx, f.task.ID, "stub", f.human(t), "", true); err != nil {
		t.Fatalf("Decide(clear) error = %v", err)
	}
	cached, _ := f.svc.Cached(ctx, f.task.ID, nil)
	if cached[0].Override {
		t.Fatalf("Cached() = %+v, want the judge verdict after clear", cached[0])
	}
	if cached[0].Verdict != check.NeedsGrooming {
		t.Fatalf("clear did not restore the judge verdict: %+v", cached[0])
	}
}

func TestCheckDecideRejectsNonHumanActor(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1"}
	f := newCheckFixture(t, stub)
	agent, err := f.actors.Add(context.Background(), core.ActorAgent, "claude")
	if err != nil {
		t.Fatalf("actor add: %v", err)
	}
	_, err = f.svc.Decide(context.Background(), f.task.ID, "stub", agent, "because", false)
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Decide(agent) error = %v, want ErrInvalid", err)
	}
}

func TestCheckDecideSetsAndClearsHumanDecidedLabel(t *testing.T) {
	stub := &stubCheck{name: "stub", version: "1"}
	f := newCheckFixture(t, stub)
	ctx := context.Background()
	if _, err := f.svc.Decide(ctx, f.task.ID, "stub", f.human(t), "ok", false); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	task, _ := f.tasks.Get(ctx, f.task.ID)
	if !hasLabel(task, "human-decided") {
		t.Fatalf("decide did not set the human-decided label: %v", task.Labels)
	}
	if _, err := f.svc.Decide(ctx, f.task.ID, "stub", f.human(t), "", true); err != nil {
		t.Fatalf("Decide(clear) error = %v", err)
	}
	task, _ = f.tasks.Get(ctx, f.task.ID)
	if hasLabel(task, "human-decided") {
		t.Fatalf("clear did not remove the human-decided label: %v", task.Labels)
	}
}

func TestCheckRunsEveryRegisteredCheckIndependently(t *testing.T) {
	first := &stubCheck{name: "alpha", version: "1", result: check.Result{Verdict: check.Ready}}
	second := &stubCheck{name: "beta", version: "1", result: check.Result{Verdict: check.NeedsGrooming}}
	f := newCheckFixture(t, first)
	if err := f.svc.checks.Register(second.Name(), second); err != nil {
		t.Fatalf("register second check: %v", err)
	}
	results, err := f.svc.Run(context.Background(), f.task.ID, nil, false)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(results) != 2 || results[0].Check != "alpha" || results[1].Check != "beta" {
		t.Fatalf("Run() = %+v, want both checks in name order", results)
	}
	if first.calls != 1 || second.calls != 1 {
		t.Fatalf("calls = %d/%d, want 1/1", first.calls, second.calls)
	}
}

func TestCheckOnlyWritesTheDerivedCache(t *testing.T) {
	f := newCheckFixture(t, &stubCheck{name: "stub", version: "1"})
	ctx := context.Background()
	before, err := f.tasks.Get(ctx, f.task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	after, err := f.tasks.Get(ctx, f.task.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) || len(after.Notes) != len(before.Notes) || len(after.Labels) != len(before.Labels) {
		t.Fatalf("check mutated the task: before %+v, after %+v", before, after)
	}
	kind := core.ArtifactTaskCheck
	artifacts, err := f.backend.Artifacts().List(ctx, store.ArtifactFilter{ProjectID: f.task.ProjectID, TaskID: &f.task.ID, Kind: &kind})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(artifacts) != 1 {
		t.Fatalf("cache artifacts = %d, want 1", len(artifacts))
	}
}

func TestCheckTitleAndKindInvalidate(t *testing.T) {
	f := newCheckFixture(t, &stubCheck{name: "stub", version: "1"})
	ctx := context.Background()
	if _, err := f.svc.Run(ctx, f.task.ID, nil, false); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	title := "a new title"
	if _, err := f.tasks.Set(ctx, f.task.ID, TaskSet{Title: &title}); err != nil {
		t.Fatalf("Set(title) error = %v", err)
	}
	cached, _ := f.svc.Cached(ctx, f.task.ID, nil)
	if !cached[0].Stale {
		t.Fatalf("editing the title did not invalidate: %+v", cached[0])
	}
	kind := core.KindMilestone
	if _, err := f.tasks.Set(ctx, f.task.ID, TaskSet{Kind: &kind}); err != nil {
		t.Fatalf("Set(kind) error = %v", err)
	}
	cached, _ = f.svc.Cached(ctx, f.task.ID, nil)
	if !cached[0].Stale {
		t.Fatalf("editing the kind did not invalidate: %+v", cached[0])
	}
}

func TestCheckUnknownNameErrors(t *testing.T) {
	f := newCheckFixture(t, &stubCheck{name: "stub", version: "1"})
	if _, err := f.svc.Run(context.Background(), f.task.ID, []string{"nope"}, false); !errors.Is(err, registry.ErrNotFound) {
		t.Fatalf("Run(unknown) error = %v, want ErrNotFound", err)
	}
}

func TestCheckJudgeUnavailablePropagates(t *testing.T) {
	f := newCheckFixture(t, &stubCheck{name: "stub", version: "1", err: judge.ErrUnavailable})
	if _, err := f.svc.Run(context.Background(), f.task.ID, nil, false); !errors.Is(err, judge.ErrUnavailable) {
		t.Fatalf("Run() error = %v, want the check error", err)
	}
}

func hasLabel(task *core.Task, label string) bool {
	for _, have := range task.Labels {
		if have == label {
			return true
		}
	}
	return false
}
