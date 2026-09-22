package app

import (
	"context"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

func factsBackend(t *testing.T) (store.Backend, core.ProjectID) {
	t.Helper()
	ctx := context.Background()
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	project := &core.Project{ID: "prj", Name: "Acme", Policy: core.DefaultResolutionPolicy()}
	if err := backend.Projects().Create(ctx, project); err != nil {
		t.Fatalf("Create(project) error = %v", err)
	}
	return backend, project.ID
}

func createTask(t *testing.T, backend store.Backend, task *core.Task) {
	t.Helper()
	if err := backend.Tasks().Create(context.Background(), task); err != nil {
		t.Fatalf("Create(%s) error = %v", task.ID, err)
	}
}

func TestTaskGraphFactsReportsDependentsAndReason(t *testing.T) {
	ctx := context.Background()
	backend, projectID := factsBackend(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	createTask(t, backend, &core.Task{ID: "first", ProjectID: projectID, Kind: core.KindTask, Title: "first", Status: core.StatusTodo})
	createTask(t, backend, &core.Task{ID: "second", ProjectID: projectID, Kind: core.KindTask, Title: "second", Status: core.StatusTodo, Deps: []core.TaskID{"first"}})
	createTask(t, backend, &core.Task{ID: "third", ProjectID: projectID, Kind: core.KindTask, Title: "third", Status: core.StatusTodo, Deps: []core.TaskID{"second"}})

	first, err := backend.Tasks().Get(ctx, "first")
	if err != nil {
		t.Fatalf("Get(first) error = %v", err)
	}
	dependents, reason, err := TaskGraphFacts(ctx, backend, first, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(first) error = %v", err)
	}
	if !equalIDs(dependents, []core.TaskID{"second"}) {
		t.Fatalf("first dependents = %v, want [second]", dependents)
	}
	if reason != nil {
		t.Fatalf("first should be ready, reason = %+v", reason)
	}

	second, err := backend.Tasks().Get(ctx, "second")
	if err != nil {
		t.Fatalf("Get(second) error = %v", err)
	}
	dependents, reason, err = TaskGraphFacts(ctx, backend, second, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(second) error = %v", err)
	}
	if !equalIDs(dependents, []core.TaskID{"third"}) {
		t.Fatalf("second dependents = %v, want [third]", dependents)
	}
	if reason == nil || reason.Code != graph.ReasonDepUnresolved || reason.Detail != "first" {
		t.Fatalf("second reason = %+v, want dep_unresolved/first", reason)
	}

	third, err := backend.Tasks().Get(ctx, "third")
	if err != nil {
		t.Fatalf("Get(third) error = %v", err)
	}
	dependents, reason, err = TaskGraphFacts(ctx, backend, third, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(third) error = %v", err)
	}
	if len(dependents) != 0 {
		t.Fatalf("third dependents = %v, want none", dependents)
	}
	if reason == nil || reason.Code != graph.ReasonDepUnresolved || reason.Detail != "second" {
		t.Fatalf("third reason = %+v, want dep_unresolved/second", reason)
	}
}

// A task snoozed until another task must reflect that task's resolution, so the
// neighborhood read has to include the snooze until-task even though it is not a
// dependency edge.
func TestTaskGraphFactsSnoozeUntilTask(t *testing.T) {
	ctx := context.Background()
	backend, projectID := factsBackend(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	gate := core.TaskID("gate")

	createTask(t, backend, &core.Task{ID: "gate", ProjectID: projectID, Kind: core.KindTask, Title: "gate", Status: core.StatusTodo})
	createTask(t, backend, &core.Task{ID: "parked", ProjectID: projectID, Kind: core.KindTask, Title: "parked", Status: core.StatusTodo, Snooze: &core.Snooze{UntilTask: &gate}})

	parked, err := backend.Tasks().Get(ctx, "parked")
	if err != nil {
		t.Fatalf("Get(parked) error = %v", err)
	}
	_, reason, err := TaskGraphFacts(ctx, backend, parked, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(parked) error = %v", err)
	}
	if reason == nil || reason.Code != graph.ReasonSnoozed {
		t.Fatalf("parked reason = %+v, want snoozed", reason)
	}

	if err := backend.Tasks().Update(ctx, &core.Task{ID: "gate", ProjectID: projectID, Kind: core.KindTask, Title: "gate", Status: core.StatusDone}); err != nil {
		t.Fatalf("Update(gate) error = %v", err)
	}
	_, reason, err = TaskGraphFacts(ctx, backend, parked, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(parked) after gate error = %v", err)
	}
	if reason != nil {
		t.Fatalf("parked reason after gate resolved = %+v, want ready", reason)
	}
}

func TestTaskGraphFactsNotBefore(t *testing.T) {
	ctx := context.Background()
	backend, projectID := factsBackend(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	notBefore := now.Add(24 * time.Hour)

	createTask(t, backend, &core.Task{ID: "later", ProjectID: projectID, Kind: core.KindTask, Title: "later", Status: core.StatusTodo, NotBefore: &notBefore})
	later, err := backend.Tasks().Get(ctx, "later")
	if err != nil {
		t.Fatalf("Get(later) error = %v", err)
	}
	_, reason, err := TaskGraphFacts(ctx, backend, later, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(later) error = %v", err)
	}
	if reason == nil || reason.Code != graph.ReasonNotBefore {
		t.Fatalf("later reason = %+v, want not_before", reason)
	}
}

// A dependency in another project is out of the graph's scope, so it stays
// unresolved: this matches the full-snapshot view, which only loads the task's
// own project.
func TestTaskGraphFactsIgnoresCrossProjectDeps(t *testing.T) {
	ctx := context.Background()
	backend, projectID := factsBackend(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := backend.Projects().Create(ctx, &core.Project{ID: "other", Name: "Other", Policy: core.DefaultResolutionPolicy()}); err != nil {
		t.Fatalf("Create(other project) error = %v", err)
	}
	createTask(t, backend, &core.Task{ID: "foreign", ProjectID: "other", Kind: core.KindTask, Title: "foreign", Status: core.StatusDone})
	createTask(t, backend, &core.Task{ID: "local", ProjectID: projectID, Kind: core.KindTask, Title: "local", Status: core.StatusTodo, Deps: []core.TaskID{"foreign"}})

	local, err := backend.Tasks().Get(ctx, "local")
	if err != nil {
		t.Fatalf("Get(local) error = %v", err)
	}
	_, reason, err := TaskGraphFacts(ctx, backend, local, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(local) error = %v", err)
	}
	if reason == nil || reason.Code != graph.ReasonDepUnresolved || reason.Detail != "foreign" {
		t.Fatalf("local reason = %+v, want dep_unresolved/foreign", reason)
	}
}

// Dependents are scoped to the task's project: a task in another project that
// names the id is not a dependent.
func TestTaskGraphFactsDependentsAreProjectScoped(t *testing.T) {
	ctx := context.Background()
	backend, projectID := factsBackend(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	createTask(t, backend, &core.Task{ID: "mine", ProjectID: projectID, Kind: core.KindTask, Title: "mine", Status: core.StatusTodo})
	createTask(t, backend, &core.Task{ID: "local", ProjectID: projectID, Kind: core.KindTask, Title: "local", Status: core.StatusTodo, Deps: []core.TaskID{"mine"}})
	createTask(t, backend, &core.Task{ID: "remote", ProjectID: "other", Kind: core.KindTask, Title: "remote", Status: core.StatusTodo, Deps: []core.TaskID{"mine"}})

	mine, err := backend.Tasks().Get(ctx, "mine")
	if err != nil {
		t.Fatalf("Get(mine) error = %v", err)
	}
	dependents, _, err := TaskGraphFacts(ctx, backend, mine, now)
	if err != nil {
		t.Fatalf("TaskGraphFacts(mine) error = %v", err)
	}
	if !equalIDs(dependents, []core.TaskID{"local"}) {
		t.Fatalf("mine dependents = %v, want [local]", dependents)
	}
}
