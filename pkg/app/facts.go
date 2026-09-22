package app

import (
	"context"
	"errors"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/store"
)

// TaskGraphFacts returns a task's direct dependents and the reason it is
// excluded from the ready set. Unlike LoadSnapshot, it reads only the task and
// its dependency neighborhood — the task itself, its direct dependencies, and
// its snooze until-task — plus an indexed lookup of its dependents, so `task
// get` stays a point read instead of an O(tasks) snapshot load.
func TaskGraphFacts(ctx context.Context, backend store.Backend, task *core.Task, now time.Time) ([]core.TaskID, *graph.NotReadyReason, error) {
	project, err := backend.Projects().Get(ctx, task.ProjectID)
	if err != nil {
		return nil, nil, err
	}

	neighborhood := make([]core.Task, 0, len(task.Deps)+2)
	seen := make(map[core.TaskID]bool, len(task.Deps)+2)
	add := func(t *core.Task) {
		if seen[t.ID] {
			return
		}
		seen[t.ID] = true
		neighborhood = append(neighborhood, *t)
	}
	add(task)
	for _, dep := range task.Deps {
		if err := addNeighbor(ctx, backend, project.ID, dep, add); err != nil {
			return nil, nil, err
		}
	}
	if task.Snooze != nil && task.Snooze.UntilTask != nil {
		if err := addNeighbor(ctx, backend, project.ID, *task.Snooze.UntilTask, add); err != nil {
			return nil, nil, err
		}
	}

	built, err := graph.NewAt(neighborhood, project.Policy, now)
	if err != nil {
		return nil, nil, err
	}
	_, reason := built.Readiness(task.ID)

	dependentTasks, err := backend.Tasks().List(ctx, store.TaskFilter{ProjectID: task.ProjectID, DependsOn: &task.ID})
	if err != nil {
		return nil, nil, err
	}
	dependents := make([]core.TaskID, 0, len(dependentTasks))
	for _, dependent := range dependentTasks {
		dependents = append(dependents, dependent.ID)
	}
	return dependents, reason, nil
}

// addNeighbor fetches a task by id and passes it to add. A missing task, or one
// in another project, is skipped: absent from the neighborhood, graph.Readiness
// treats it as unresolved, exactly as the full-snapshot graph (scoped to one
// project) would.
func addNeighbor(ctx context.Context, backend store.Backend, projectID core.ProjectID, id core.TaskID, add func(*core.Task)) error {
	task, err := backend.Tasks().Get(ctx, id)
	if errors.Is(err, core.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if task.ProjectID != projectID {
		return nil
	}
	add(task)
	return nil
}
