// Package memory is the in-memory storage backend. It is the reference
// implementation of the store ports and passes the conformance suite.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/internal/clone"
)

type Backend struct {
	mu        sync.RWMutex
	projects  map[core.ProjectID]core.Project
	tasks     map[core.TaskID]core.Task
	actors    map[core.ActorID]core.Actor
	artifacts map[core.ArtifactID]core.Artifact
	events    []core.Event
}

func New() *Backend {
	return &Backend{
		projects:  make(map[core.ProjectID]core.Project),
		tasks:     make(map[core.TaskID]core.Task),
		actors:    make(map[core.ActorID]core.Actor),
		artifacts: make(map[core.ArtifactID]core.Artifact),
	}
}

func Open(_ context.Context, _ store.Config) (store.Backend, error) {
	return New(), nil
}

func (b *Backend) Close() error                { return nil }
func (b *Backend) Projects() store.ProjectRepo { return &projectRepo{backend: b} }
func (b *Backend) Tasks() store.TaskRepo       { return &taskRepo{backend: b} }
func (b *Backend) Actors() store.ActorRepo     { return &actorRepo{backend: b} }
func (b *Backend) Artifacts() store.ArtifactRepo {
	return &artifactRepo{backend: b}
}
func (b *Backend) Events() store.EventRepo { return &eventRepo{backend: b} }

type projectRepo struct{ backend *Backend }

func (r *projectRepo) Create(_ context.Context, project *core.Project) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.projects[project.ID]; ok {
		return fmt.Errorf("%w: project %s", core.ErrAlreadyExists, project.ID)
	}
	r.backend.projects[project.ID] = clone.Project(*project)
	return nil
}

func (r *projectRepo) Get(_ context.Context, id core.ProjectID) (*core.Project, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	project, ok := r.backend.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	cloned := clone.Project(project)
	return &cloned, nil
}

func (r *projectRepo) List(_ context.Context) ([]*core.Project, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.ProjectID, 0, len(r.backend.projects))
	for id := range r.backend.projects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Project, 0, len(ids))
	for _, id := range ids {
		cloned := clone.Project(r.backend.projects[id])
		out = append(out, &cloned)
	}
	return out, nil
}

func (r *projectRepo) Update(_ context.Context, project *core.Project) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.projects[project.ID]; !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, project.ID)
	}
	r.backend.projects[project.ID] = clone.Project(*project)
	return nil
}

func (r *projectRepo) Delete(_ context.Context, id core.ProjectID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.projects[id]; !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	delete(r.backend.projects, id)
	return nil
}

type taskRepo struct{ backend *Backend }

func (r *taskRepo) Create(_ context.Context, task *core.Task) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.tasks[task.ID]; ok {
		return fmt.Errorf("%w: task %s", core.ErrAlreadyExists, task.ID)
	}
	r.backend.tasks[task.ID] = clone.Task(*task)
	return nil
}

func (r *taskRepo) Get(_ context.Context, id core.TaskID) (*core.Task, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	task, ok := r.backend.tasks[id]
	if !ok {
		return nil, fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	cloned := clone.Task(task)
	return &cloned, nil
}

func (r *taskRepo) List(_ context.Context, filter store.TaskFilter) ([]*core.Task, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.TaskID, 0, len(r.backend.tasks))
	for id, task := range r.backend.tasks {
		if !matchesTask(task, filter) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Task, 0, len(ids))
	for _, id := range ids {
		cloned := clone.Task(r.backend.tasks[id])
		out = append(out, &cloned)
	}
	return out, nil
}

func matchesTask(task core.Task, filter store.TaskFilter) bool {
	if filter.ProjectID != "" && task.ProjectID != filter.ProjectID {
		return false
	}
	if filter.Repo != nil && task.Repo != *filter.Repo {
		return false
	}
	if len(filter.Statuses) > 0 {
		found := false
		for _, status := range filter.Statuses {
			if task.Status == status {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Kind != nil && task.Kind != *filter.Kind {
		return false
	}
	return store.MatchLabels(task, filter.Labels)
}

func (r *taskRepo) Search(_ context.Context, filter store.TaskFilter, query string) ([]store.TaskSearchHit, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.TaskSearchHit, 0)
	for _, task := range r.backend.tasks {
		if !matchesTask(task, filter) {
			continue
		}
		score, matched := store.LexicalTaskScore(&task, terms)
		if !matched {
			continue
		}
		cloned := clone.Task(task)
		hits = append(hits, store.TaskSearchHit{Task: &cloned, Score: score})
	}
	store.SortTaskSearchHits(hits)
	return hits, nil
}

func (r *taskRepo) Update(_ context.Context, task *core.Task) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.tasks[task.ID]; !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	r.backend.tasks[task.ID] = clone.Task(*task)
	return nil
}

func (r *taskRepo) UpdateExpected(_ context.Context, task *core.Task, expected time.Time) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	current, ok := r.backend.tasks[task.ID]
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	if !current.UpdatedAt.Equal(expected) {
		return fmt.Errorf("%w: task %s was modified", core.ErrConflict, task.ID)
	}
	r.backend.tasks[task.ID] = clone.Task(*task)
	return nil
}

func (r *taskRepo) Delete(_ context.Context, id core.TaskID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.tasks[id]; !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	delete(r.backend.tasks, id)
	return nil
}

type actorRepo struct{ backend *Backend }

func (r *actorRepo) Create(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actors[actor.ID]; ok {
		return fmt.Errorf("%w: actor %s", core.ErrAlreadyExists, actor.ID)
	}
	r.backend.actors[actor.ID] = *actor
	return nil
}

func (r *actorRepo) Get(_ context.Context, id core.ActorID) (*core.Actor, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	actor, ok := r.backend.actors[id]
	if !ok {
		return nil, fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	return &actor, nil
}

func (r *actorRepo) FindByName(_ context.Context, name string) (*core.Actor, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	for _, actor := range r.backend.actors {
		if actor.Name == name {
			found := actor
			return &found, nil
		}
	}
	return nil, fmt.Errorf("%w: actor named %q", core.ErrNotFound, name)
}

func (r *actorRepo) List(_ context.Context) ([]*core.Actor, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.ActorID, 0, len(r.backend.actors))
	for id := range r.backend.actors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Actor, 0, len(ids))
	for _, id := range ids {
		actor := r.backend.actors[id]
		out = append(out, &actor)
	}
	return out, nil
}

func (r *actorRepo) Update(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actors[actor.ID]; !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, actor.ID)
	}
	r.backend.actors[actor.ID] = *actor
	return nil
}

func (r *actorRepo) Delete(_ context.Context, id core.ActorID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actors[id]; !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	delete(r.backend.actors, id)
	return nil
}

type artifactRepo struct{ backend *Backend }

func (r *artifactRepo) Create(_ context.Context, artifact *core.Artifact) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.artifacts[artifact.ID]; ok {
		return fmt.Errorf("%w: artifact %s", core.ErrAlreadyExists, artifact.ID)
	}
	r.backend.artifacts[artifact.ID] = clone.Artifact(*artifact)
	return nil
}

func (r *artifactRepo) Get(_ context.Context, id core.ArtifactID) (*core.Artifact, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	artifact, ok := r.backend.artifacts[id]
	if !ok {
		return nil, fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	cloned := clone.Artifact(artifact)
	return &cloned, nil
}

func (r *artifactRepo) List(_ context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.ArtifactID, 0, len(r.backend.artifacts))
	for id, artifact := range r.backend.artifacts {
		if !store.MatchesArtifactFilter(&artifact, filter) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Artifact, 0, len(ids))
	for _, id := range ids {
		cloned := clone.Artifact(r.backend.artifacts[id])
		out = append(out, &cloned)
	}
	return out, nil
}

func (r *artifactRepo) Search(_ context.Context, filter store.ArtifactFilter, query string) ([]store.SearchHit, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.SearchHit, 0)
	for _, artifact := range r.backend.artifacts {
		if !store.MatchesArtifactFilter(&artifact, filter) {
			continue
		}
		score, matched := store.LexicalScore(&artifact, terms)
		if !matched {
			continue
		}
		cloned := clone.Artifact(artifact)
		hits = append(hits, store.SearchHit{Artifact: &cloned, Score: score})
	}
	store.SortSearchHits(hits)
	return hits, nil
}

func (r *artifactRepo) Update(_ context.Context, artifact *core.Artifact) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.artifacts[artifact.ID]; !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	r.backend.artifacts[artifact.ID] = clone.Artifact(*artifact)
	return nil
}

func (r *artifactRepo) Delete(_ context.Context, id core.ArtifactID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.artifacts[id]; !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	delete(r.backend.artifacts, id)
	return nil
}

type eventRepo struct{ backend *Backend }

func (r *eventRepo) Append(_ context.Context, event *core.Event) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	r.backend.events = append(r.backend.events, clone.Event(*event))
	return nil
}

func (r *eventRepo) List(_ context.Context, filter store.EventFilter) ([]*core.Event, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	out := make([]*core.Event, 0)
	for i := len(r.backend.events) - 1; i >= 0; i-- {
		event := r.backend.events[i]
		if filter.ProjectID != "" && event.ProjectID != filter.ProjectID {
			continue
		}
		if filter.TaskID != nil && (event.TaskID == nil || *event.TaskID != *filter.TaskID) {
			continue
		}
		if len(filter.Kinds) > 0 && !containsKind(filter.Kinds, event.Kind) {
			continue
		}
		if filter.Since != nil && event.CreatedAt.Before(*filter.Since) {
			continue
		}
		cloned := clone.Event(event)
		out = append(out, &cloned)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func containsKind(kinds []core.EventKind, kind core.EventKind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}
