package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/rank"
)

// fakeLoopRunner stands in for the single-task RunService: it records the order
// tasks were run and, unless programmed to fail a task, resolves it the way a
// real run does (moves it to ready_for_review).
type fakeLoopRunner struct {
	tasks *TicketService
	order []core.TicketID
	fail  map[core.TicketID]error
}

func (f *fakeLoopRunner) run(ctx context.Context, id core.TicketID) (*RunOutcome, error) {
	f.order = append(f.order, id)
	if err := f.fail[id]; err != nil {
		return &RunOutcome{TicketID: id, ExitCode: 1}, err
	}
	updated, err := f.tasks.SetStatus(ctx, id, core.StatusReadyForReview)
	if err != nil {
		return nil, err
	}
	return &RunOutcome{TicketID: id, Status: updated.Status, Output: "ok", Complete: true}, nil
}

func newLoopService(t *testing.T, h *harness) *RunLoopService {
	t.Helper()
	ranker, err := rank.Default()
	if err != nil {
		t.Fatalf("rank.Default() error = %v", err)
	}
	return NewRunLoopService(h.backend, ranker, fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)})
}

// loopAgent returns the agent the loop's fixture tasks are assigned to,
// creating it once per harness. The loop only runs agent-ready work, so every
// task addLoopTask builds is assigned and groomed.
func loopAgent(t *testing.T, h *harness) *core.Actor {
	t.Helper()
	ctx := context.Background()
	agent, err := h.actors.Add(ctx, core.ActorAgent, "claude")
	if errors.Is(err, core.ErrAlreadyExists) {
		if agent, err = h.actors.Resolve(ctx, "claude"); err == nil {
			return agent
		}
	}
	if err != nil {
		t.Fatalf("ensure loop agent: %v", err)
	}
	return agent
}

func addLoopTask(t *testing.T, h *harness, project *core.Project, title string, priority int) *core.Ticket {
	t.Helper()
	agent := loopAgent(t, h)
	task, err := h.tasks.Add(context.Background(), TicketInput{
		ProjectID:          project.ID,
		Title:              title,
		Priority:           priority,
		AssigneeID:         &agent.ID,
		Groomed:            true,
		AcceptanceCriteria: []string{"done"},
	})
	if err != nil {
		t.Fatalf("Add(%s) error = %v", title, err)
	}
	return task
}

func mustDep(t *testing.T, h *harness, task, dep *core.Ticket) {
	t.Helper()
	if _, err := h.tasks.AddDep(context.Background(), task.ID, dep.ID); err != nil {
		t.Fatalf("AddDep(%s, %s) error = %v", task.ID, dep.ID, err)
	}
}

func TestRunLoopDrivesChainToTaskGoal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, first)

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopGoalReached {
		t.Fatalf("Stop = %q, want goal_reached", outcome.Stop)
	}
	if outcome.GoalKind != core.KindTask {
		t.Fatalf("GoalKind = %q, want task", outcome.GoalKind)
	}
	if outcome.GoalStatus != core.StatusReadyForReview {
		t.Fatalf("GoalStatus = %q, want ready_for_review", outcome.GoalStatus)
	}
	if !equalIDs(runner.order, []core.TicketID{first.ID, goal.ID}) {
		t.Fatalf("run order = %v, want [%s %s]", runner.order, first.ID, goal.ID)
	}
	if len(outcome.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(outcome.Steps))
	}
	if outcome.Steps[0].Iteration != 1 || outcome.Steps[1].Iteration != 2 {
		t.Fatalf("iteration numbers = %d, %d, want 1, 2", outcome.Steps[0].Iteration, outcome.Steps[1].Iteration)
	}
	if outcome.Steps[0].Remaining != 1 || outcome.Steps[1].Remaining != 0 {
		t.Fatalf("remaining = %d, %d, want 1, 0", outcome.Steps[0].Remaining, outcome.Steps[1].Remaining)
	}
	if outcome.Remaining != 0 {
		t.Fatalf("outcome.Remaining = %d, want 0", outcome.Remaining)
	}
}

func TestRunLoopStopsOnBudget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	second := addLoopTask(t, h, project, "second", 0)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, second, first)
	mustDep(t, h, goal, second)

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run, MaxTasks: 1})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopBudgetExhausted {
		t.Fatalf("Stop = %q, want budget_exhausted", outcome.Stop)
	}
	if !equalIDs(runner.order, []core.TicketID{first.ID}) {
		t.Fatalf("run order = %v, want [%s]", runner.order, first.ID)
	}
	if outcome.Remaining != 2 {
		t.Fatalf("Remaining = %d, want 2", outcome.Remaining)
	}
}

func TestRunLoopGoalReachedExactlyAtBudget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, first)

	runner := &fakeLoopRunner{tasks: h.tasks}
	// The budget is exactly the work on the path: the loop must report the goal,
	// not a budget stop, once the last task resolves.
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run, MaxTasks: 2})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopGoalReached {
		t.Fatalf("Stop = %q, want goal_reached", outcome.Stop)
	}
	if !equalIDs(runner.order, []core.TicketID{first.ID, goal.ID}) {
		t.Fatalf("run order = %v, want [%s %s]", runner.order, first.ID, goal.ID)
	}
}

func TestRunLoopStallTakesPrecedenceOverBudget(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, first)
	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusInProgress); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}

	runner := &fakeLoopRunner{tasks: h.tasks}
	// No startable work: the loop reports the stall, not the budget it never
	// needed to spend.
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run, MaxTasks: 5})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopNoReadyWork {
		t.Fatalf("Stop = %q, want no_ready_work", outcome.Stop)
	}
	if len(runner.order) != 0 {
		t.Fatalf("run order = %v, want none", runner.order)
	}
}

func TestRunLoopStopsWhenNoReadyWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, first)
	// Another actor owns the only prerequisite, so nothing on the path is
	// startable and the goal cannot be reached.
	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusInProgress); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopNoReadyWork {
		t.Fatalf("Stop = %q, want no_ready_work", outcome.Stop)
	}
	if len(runner.order) != 0 {
		t.Fatalf("run order = %v, want none", runner.order)
	}
	if outcome.Remaining != 2 {
		t.Fatalf("Remaining = %d, want 2", outcome.Remaining)
	}
}

func TestRunLoopStopsBlocked(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, first)
	if _, err := h.tasks.SetStatus(ctx, first.ID, core.StatusBlocked); err != nil {
		t.Fatalf("SetStatus() error = %v", err)
	}

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopBlocked {
		t.Fatalf("Stop = %q, want blocked", outcome.Stop)
	}
}

// A milestone is a human gate: the loop completes its prerequisites and stops
// when the milestone becomes startable, never running the milestone itself.
func TestRunLoopMilestoneGoalStopsWhenGateOpens(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	gate, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Kind: core.KindMilestone, Title: "release"})
	if err != nil {
		t.Fatalf("Add(milestone) error = %v", err)
	}
	mustDep(t, h, gate, first)

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: gate.ID, Runner: runner.run})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopGoalReached {
		t.Fatalf("Stop = %q, want goal_reached", outcome.Stop)
	}
	if outcome.GoalKind != core.KindMilestone {
		t.Fatalf("GoalKind = %q, want milestone", outcome.GoalKind)
	}
	if !equalIDs(runner.order, []core.TicketID{first.ID}) {
		t.Fatalf("run order = %v, want only the prerequisite", runner.order)
	}
	if outcome.Remaining != 0 {
		t.Fatalf("Remaining = %d, want 0", outcome.Remaining)
	}
}

func TestRunLoopRespectsPriority(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	low := addLoopTask(t, h, project, "low", 0)
	high := addLoopTask(t, h, project, "high", 10)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, low)
	mustDep(t, h, goal, high)

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopGoalReached {
		t.Fatalf("Stop = %q, want goal_reached", outcome.Stop)
	}
	if !equalIDs(runner.order, []core.TicketID{high.ID, low.ID, goal.ID}) {
		t.Fatalf("run order = %v, want high, low, goal", runner.order)
	}
}

func TestRunLoopStopsOnFailure(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	first := addLoopTask(t, h, project, "first", 0)
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, first)

	runner := &fakeLoopRunner{tasks: h.tasks, fail: map[core.TicketID]error{first.ID: ErrRunFailed}}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run})
	if !errors.Is(err, ErrRunFailed) {
		t.Fatalf("Run() error = %v, want ErrRunFailed", err)
	}
	if outcome.Stop != StopFailed {
		t.Fatalf("Stop = %q, want failed", outcome.Stop)
	}
	if !equalIDs(runner.order, []core.TicketID{first.ID}) {
		t.Fatalf("run order = %v, want only the failed task", runner.order)
	}
	stored, err := h.tasks.Get(ctx, first.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if stored.Status != core.StatusTodo {
		t.Fatalf("failed task status = %q, want todo (unchanged)", stored.Status)
	}
}

func TestRunLoopRejectsMissingOrNonExecutableGoal(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	idea, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Kind: core.KindIdea, Title: "maybe"})
	if err != nil {
		t.Fatalf("Add(idea) error = %v", err)
	}

	runner := &fakeLoopRunner{tasks: h.tasks}
	svc := newLoopService(t, h)
	if _, err := svc.Run(ctx, LoopInput{GoalID: "t-missing", Runner: runner.run}); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("missing goal error = %v, want ErrNotFound", err)
	}
	if _, err := svc.Run(ctx, LoopInput{GoalID: idea.ID, Runner: runner.run}); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("idea goal error = %v, want ErrInvalid", err)
	}
}

// An ungroomed on-path task is not agent-ready: the loop leaves it for a human
// to groom rather than running it, and names it in the outcome as not-run.
func TestRunLoopSkipsUngroomedOnPathTask(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := loopAgent(t, h)
	// Assigned to the agent but ungroomed, so it is still human work.
	prereq, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "ungroomed", AssigneeID: &agent.ID})
	if err != nil {
		t.Fatalf("Add(ungroomed) error = %v", err)
	}
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, prereq)

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopNoReadyWork {
		t.Fatalf("Stop = %q, want no_ready_work", outcome.Stop)
	}
	if len(runner.order) != 0 {
		t.Fatalf("run order = %v, want none", runner.order)
	}
	if !equalIDs(outcome.NotRun, []core.TicketID{prereq.ID}) {
		t.Fatalf("NotRun = %v, want [%s]", outcome.NotRun, prereq.ID)
	}
	if outcome.Remaining != 2 {
		t.Fatalf("Remaining = %d, want 2", outcome.Remaining)
	}
	if stored, err := h.tasks.Get(ctx, prereq.ID); err != nil || stored.Status != core.StatusTodo {
		t.Fatalf("ungroomed task = %v (err %v), want todo and untouched", stored, err)
	}
}

// The loop runs the agent-ready work it can and, when only ungroomed work is
// left startable, stalls and reports the skipped task instead of running it.
func TestRunLoopReportsUngroomedTaskAfterAgentWork(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	project := h.newProject(t)
	agent := loopAgent(t, h)
	ready := addLoopTask(t, h, project, "ready", 0)
	ungroomed, err := h.tasks.Add(ctx, TicketInput{ProjectID: project.ID, Title: "ungroomed", AssigneeID: &agent.ID})
	if err != nil {
		t.Fatalf("Add(ungroomed) error = %v", err)
	}
	goal := addLoopTask(t, h, project, "goal", 0)
	mustDep(t, h, goal, ready)
	mustDep(t, h, goal, ungroomed)

	runner := &fakeLoopRunner{tasks: h.tasks}
	outcome, err := newLoopService(t, h).Run(ctx, LoopInput{GoalID: goal.ID, Runner: runner.run})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if outcome.Stop != StopNoReadyWork {
		t.Fatalf("Stop = %q, want no_ready_work", outcome.Stop)
	}
	if !equalIDs(runner.order, []core.TicketID{ready.ID}) {
		t.Fatalf("run order = %v, want only the agent-ready task", runner.order)
	}
	if !equalIDs(outcome.NotRun, []core.TicketID{ungroomed.ID}) {
		t.Fatalf("NotRun = %v, want [%s]", outcome.NotRun, ungroomed.ID)
	}
}
