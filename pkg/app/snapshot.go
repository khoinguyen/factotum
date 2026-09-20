package app

import (
	"context"
	"fmt"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/store"
)

// Snapshot is the fully-loaded state of one project: its tasks, the actor
// registry, the derived graph, and the current ready buckets. Renderers and the
// CLI work from a snapshot.
type Snapshot struct {
	Project *core.Project
	Tasks   []*core.Task
	Actors  map[core.ActorID]core.Actor
	Graph   *graph.Graph
	Ready   graph.ReadyBucket
}

// LoadSnapshot loads a project's state and evaluates readiness at now, so
// not_before constraints in the future are excluded from the ready buckets.
func LoadSnapshot(ctx context.Context, backend store.Backend, projectID core.ProjectID, now time.Time) (*Snapshot, error) {
	project, err := backend.Projects().Get(ctx, projectID)
	if err != nil {
		return nil, err
	}
	tasks, err := backend.Tasks().List(ctx, store.TaskFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	actors, err := backend.Actors().List(ctx)
	if err != nil {
		return nil, err
	}
	actorMap := make(map[core.ActorID]core.Actor, len(actors))
	for _, actor := range actors {
		actorMap[actor.ID] = *actor
	}

	copied := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		copied = append(copied, *task)
	}
	built, err := graph.NewAt(copied, project.Policy, now)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}

	return &Snapshot{
		Project: project,
		Tasks:   tasks,
		Actors:  actorMap,
		Graph:   built,
		Ready:   built.ReadyByActor(actorMap),
	}, nil
}

// LoadAllSnapshot merges every project's tasks into a single graph for
// cross-project queries such as ranking. Snapshot.Project is nil; the default
// resolution policy is used.
func LoadAllSnapshot(ctx context.Context, backend store.Backend, now time.Time) (*Snapshot, error) {
	tasks, err := backend.Tasks().List(ctx, store.TaskFilter{})
	if err != nil {
		return nil, err
	}
	actors, err := backend.Actors().List(ctx)
	if err != nil {
		return nil, err
	}
	actorMap := make(map[core.ActorID]core.Actor, len(actors))
	for _, actor := range actors {
		actorMap[actor.ID] = *actor
	}

	copied := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		copied = append(copied, *task)
	}
	built, err := graph.NewAt(copied, core.DefaultResolutionPolicy(), now)
	if err != nil {
		return nil, fmt.Errorf("build graph: %w", err)
	}

	return &Snapshot{
		Tasks:  tasks,
		Actors: actorMap,
		Graph:  built,
		Ready:  built.ReadyByActor(actorMap),
	}, nil
}
