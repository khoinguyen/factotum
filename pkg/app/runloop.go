package app

import (
	"context"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/store"
)

// StopReason is the stable, machine-readable reason the DAG loop stopped.
type StopReason string

const (
	// StopGoalReached is the goal resolving under the project policy, or a
	// milestone goal becoming startable (its human gate is now open).
	StopGoalReached StopReason = "goal_reached"
	// StopNoReadyWork is no startable task left on the path to the goal while the
	// goal is still unresolved (for example, waiting on a non-executable or
	// external dependency).
	StopNoReadyWork StopReason = "no_ready_work"
	// StopBlocked is on-path work explicitly parked in the blocked status.
	StopBlocked StopReason = "blocked"
	// StopBudgetExhausted is the configured task budget reached before the goal.
	StopBudgetExhausted StopReason = "budget_exhausted"
	// StopFailed is a task run failing; the loop stops for a human to inspect.
	StopFailed StopReason = "failed"
)

// LoopStep is one iteration's compact progress report.
type LoopStep struct {
	Iteration int
	TaskID    core.TaskID
	Title     string
	Status    core.TaskStatus
	ExitCode  int
	Complete  bool
	Failed    bool
	Output    string
	// Remaining is the number of unresolved tasks still on the path to the goal
	// after this iteration.
	Remaining int
}

// LoopOutcome records how the loop ended and what it did.
type LoopOutcome struct {
	Goal       core.TaskID
	GoalKind   core.TaskKind
	GoalStatus core.TaskStatus
	Stop       StopReason
	Steps      []LoopStep
	Remaining  int
}

// LoopRunner runs one task end-to-end and reflects its outcome into the store.
// It is the single-task RunService in production and a fake in tests, so the
// loop depends on the behavior, not the adapter.
type LoopRunner func(ctx context.Context, taskID core.TaskID) (*RunOutcome, error)

// LoopInput is one DAG-loop run toward a goal.
type LoopInput struct {
	GoalID core.TaskID
	Runner LoopRunner
	// MaxTasks bounds how many tasks the loop runs; 0 means no budget.
	MaxTasks int
	// OnStep, when set, is called after each iteration with its report, so a
	// caller can stream progress.
	OnStep func(LoopStep)
}

// RunLoopService drives the graph toward a goal: it repeatedly selects the
// highest-ranked startable task on the path to the goal, runs it, and re-reads
// the graph until the goal is reached or the loop cannot progress.
type RunLoopService struct {
	backend store.Backend
	ranker  rank.Ranker
	clock   Clock
}

func NewRunLoopService(backend store.Backend, ranker rank.Ranker, clock Clock) *RunLoopService {
	return &RunLoopService{backend: backend, ranker: ranker, clock: clock}
}

// Run drives the loop. It returns the outcome on every terminal state; a task
// run failure is returned as an error alongside the outcome.
func (s *RunLoopService) Run(ctx context.Context, in LoopInput) (*LoopOutcome, error) {
	if in.Runner == nil {
		return nil, fmt.Errorf("%w: no task runner", core.ErrInvalid)
	}
	goal, err := s.backend.Tasks().Get(ctx, in.GoalID)
	if err != nil {
		return nil, err
	}
	// Milestones are executable in the graph, but the loop runs work toward the
	// gate, never the gate itself.
	if !goal.Kind.Executable() {
		return nil, fmt.Errorf("%w: goal %s has kind %q, not task or milestone", core.ErrInvalid, goal.ID, goal.Kind)
	}

	outcome := &LoopOutcome{Goal: goal.ID, GoalKind: goal.Kind}
	var snapshot *Snapshot
	for {
		if snapshot == nil {
			if snapshot, err = LoadSnapshot(ctx, s.backend, goal.ProjectID, s.clock.Now()); err != nil {
				return outcome, err
			}
		}
		current, ok := snapshot.Graph.Task(goal.ID)
		if !ok {
			return outcome, fmt.Errorf("%w: goal %s is no longer in project %s", core.ErrNotFound, goal.ID, goal.ProjectID)
		}
		outcome.GoalStatus = current.Status

		if current.Resolves(snapshot.Project.Policy) {
			return s.reachGoal(outcome, snapshot, 0), nil
		}
		if current.IsMilestone() {
			if ready, _ := snapshot.Graph.Readiness(goal.ID); ready {
				return s.reachGoal(outcome, snapshot, 0), nil
			}
		}

		candidates := readyOnPath(snapshot.Graph, goal.ID)
		if len(candidates) == 0 {
			return s.stall(outcome, snapshot, goal.ID), nil
		}

		scored, rankErr := s.ranker.Rank(ctx, rank.Request{
			Graph:      snapshot.Graph,
			Tasks:      snapshot.Tasks,
			Candidates: candidates,
			Toward:     goal.ID,
		})
		if rankErr != nil {
			return outcome, rankErr
		}
		if len(scored) == 0 {
			return s.stall(outcome, snapshot, goal.ID), nil
		}
		next := scored[0].TaskID
		nextTask, _ := snapshot.Graph.Task(next)

		if in.MaxTasks > 0 && len(outcome.Steps) >= in.MaxTasks {
			outcome.Stop = StopBudgetExhausted
			outcome.Remaining = remainingOnPath(snapshot.Graph, goal.ID, snapshot.Project.Policy)
			return outcome, nil
		}

		runOutcome, runErr := in.Runner(ctx, next)
		step := LoopStep{Iteration: len(outcome.Steps) + 1, TaskID: next, Title: nextTask.Title, Failed: runErr != nil}
		if runOutcome != nil {
			step.Status = runOutcome.Status
			step.ExitCode = runOutcome.ExitCode
			step.Complete = runOutcome.Complete
			step.Output = runOutcome.Output
		}

		if snapshot, err = LoadSnapshot(ctx, s.backend, goal.ProjectID, s.clock.Now()); err != nil {
			return outcome, err
		}
		step.Remaining = remainingOnPath(snapshot.Graph, goal.ID, snapshot.Project.Policy)
		outcome.Steps = append(outcome.Steps, step)
		if in.OnStep != nil {
			in.OnStep(step)
		}
		if runErr != nil {
			outcome.Stop = StopFailed
			outcome.Remaining = step.Remaining
			if updated, ok := snapshot.Graph.Task(goal.ID); ok {
				outcome.GoalStatus = updated.Status
			}
			return outcome, runErr
		}
	}
}

// reachGoal finishes with the goal reached: for a milestone goal the gate is
// now startable for a human, for a task goal it resolved.
func (s *RunLoopService) reachGoal(outcome *LoopOutcome, snapshot *Snapshot, remaining int) *LoopOutcome {
	outcome.Stop = StopGoalReached
	outcome.Remaining = remaining
	if current, ok := snapshot.Graph.Task(outcome.Goal); ok {
		outcome.GoalStatus = current.Status
	}
	return outcome
}

// stall finishes when no startable work remains toward the goal.
func (s *RunLoopService) stall(outcome *LoopOutcome, snapshot *Snapshot, goal core.TaskID) *LoopOutcome {
	unresolved := unresolvedOnPath(snapshot.Graph, goal, snapshot.Project.Policy)
	outcome.Remaining = len(unresolved)
	outcome.Stop = StopNoReadyWork
	for _, id := range unresolved {
		if task, ok := snapshot.Graph.Task(id); ok && task.Status == core.StatusBlocked {
			outcome.Stop = StopBlocked
			break
		}
	}
	return outcome
}

// readyOnPath returns the startable tasks that lie on a path to the goal.
// Milestones are excluded: a milestone is a human gate, so the loop completes
// its prerequisites and stops rather than running the gate.
func readyOnPath(g *graph.Graph, goal core.TaskID) []core.TaskID {
	onPath := ancestorsToward(g, goal)
	var out []core.TaskID
	for _, id := range g.ReadySet() {
		if !onPath[id] {
			continue
		}
		if task, ok := g.Task(id); ok && task.IsMilestone() {
			continue
		}
		out = append(out, id)
	}
	return out
}

// remainingOnPath counts the unresolved tasks on the path to the goal. A
// milestone goal is excluded from its own count: reaching it is the goal.
func remainingOnPath(g *graph.Graph, goal core.TaskID, policy core.ResolutionPolicy) int {
	return len(unresolvedOnPath(g, goal, policy))
}

func unresolvedOnPath(g *graph.Graph, goal core.TaskID, policy core.ResolutionPolicy) []core.TaskID {
	onPath := ancestorsToward(g, goal)
	var out []core.TaskID
	for _, id := range g.IDs() {
		if !onPath[id] {
			continue
		}
		task, ok := g.Task(id)
		if !ok || task.Resolves(policy) {
			continue
		}
		if id == goal && task.IsMilestone() {
			continue
		}
		out = append(out, id)
	}
	return out
}

// ancestorsToward returns the goal and every task that can reach it by
// following dependency edges (its upstream prerequisites). A task outside this
// set is not on the path to the goal and the loop leaves it alone.
func ancestorsToward(g *graph.Graph, goal core.TaskID) map[core.TaskID]bool {
	onPath := map[core.TaskID]bool{goal: true}
	queue := []core.TaskID{goal}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, dep := range g.Deps(cur) {
			if onPath[dep] {
				continue
			}
			if _, ok := g.Task(dep); !ok {
				continue
			}
			onPath[dep] = true
			queue = append(queue, dep)
		}
	}
	return onPath
}
