// Package jsonfile is a single-document JSON storage backend with atomic
// writes. It is intended for local, single-process use.
package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/internal/clone"
)

const defaultPath = ".factotum/factotum.json"

type state struct {
	Projects  []core.Project  `json:"projects,omitempty"`
	Tasks     []core.Task     `json:"tasks,omitempty"`
	Actors    []core.Actor    `json:"actors,omitempty"`
	Artifacts []core.Artifact `json:"artifacts,omitempty"`
	Events    []core.Event    `json:"events,omitempty"`
}

type Backend struct {
	mu    sync.Mutex
	path  string
	state state
}

func Open(_ context.Context, cfg store.Config) (store.Backend, error) {
	path := cfg.Option("path")
	if path == "" {
		path = defaultPath
	}
	loaded, err := load(path)
	if err != nil {
		return nil, err
	}
	return &Backend{path: path, state: loaded}, nil
}

func load(path string) (state, error) {
	var loaded state
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return loaded, nil
	}
	if err != nil {
		return loaded, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return loaded, nil
	}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return loaded, fmt.Errorf("parse %s: %w", path, err)
	}
	return loaded, nil
}

func (b *Backend) persist() error {
	data, err := json.MarshalIndent(b.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".factotum-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, b.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func (b *Backend) Close() error { return nil }

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
	if _, ok := r.backend.projectIndex(project.ID); ok {
		return fmt.Errorf("%w: project %s", core.ErrAlreadyExists, project.ID)
	}
	r.backend.state.Projects = append(r.backend.state.Projects, clone.Project(*project))
	return r.backend.persist()
}

func (r *projectRepo) Get(_ context.Context, id core.ProjectID) (*core.Project, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.projectIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	project := clone.Project(r.backend.state.Projects[index])
	return &project, nil
}

func (r *projectRepo) List(_ context.Context) ([]*core.Project, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Project, 0, len(r.backend.state.Projects))
	for _, project := range r.backend.state.Projects {
		cloned := clone.Project(project)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *projectRepo) Update(_ context.Context, project *core.Project) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.projectIndex(project.ID)
	if !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, project.ID)
	}
	r.backend.state.Projects[index] = clone.Project(*project)
	return r.backend.persist()
}

func (r *projectRepo) Delete(_ context.Context, id core.ProjectID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.projectIndex(id)
	if !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	r.backend.state.Projects = append(r.backend.state.Projects[:index], r.backend.state.Projects[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) projectIndex(id core.ProjectID) (int, bool) {
	for i, project := range b.state.Projects {
		if project.ID == id {
			return i, true
		}
	}
	return 0, false
}

type taskRepo struct{ backend *Backend }

func (r *taskRepo) Create(_ context.Context, task *core.Task) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.taskIndex(task.ID); ok {
		return fmt.Errorf("%w: task %s", core.ErrAlreadyExists, task.ID)
	}
	r.backend.state.Tasks = append(r.backend.state.Tasks, clone.Task(*task))
	return r.backend.persist()
}

func (r *taskRepo) Get(_ context.Context, id core.TaskID) (*core.Task, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.taskIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	task := clone.Task(r.backend.state.Tasks[index])
	return &task, nil
}

func (r *taskRepo) List(_ context.Context, filter store.TaskFilter) ([]*core.Task, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Task, 0, len(r.backend.state.Tasks))
	for _, task := range r.backend.state.Tasks {
		if !matchesTask(task, filter) {
			continue
		}
		cloned := clone.Task(task)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
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
	if filter.DependsOn != nil && !taskDependsOn(task, *filter.DependsOn) {
		return false
	}
	return store.MatchLabels(task, filter.Labels)
}

// taskDependsOn reports whether task lists id among its direct dependencies.
func taskDependsOn(task core.Task, id core.TaskID) bool {
	for _, dep := range task.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

func (r *taskRepo) Search(_ context.Context, filter store.TaskFilter, query string) ([]store.TaskSearchHit, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.TaskSearchHit, 0)
	for _, task := range r.backend.state.Tasks {
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
	index, ok := r.backend.taskIndex(task.ID)
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	r.backend.state.Tasks[index] = clone.Task(*task)
	return r.backend.persist()
}

func (r *taskRepo) UpdateExpected(_ context.Context, task *core.Task, expected time.Time) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.taskIndex(task.ID)
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	if !r.backend.state.Tasks[index].UpdatedAt.Equal(expected) {
		return fmt.Errorf("%w: task %s was modified", core.ErrConflict, task.ID)
	}
	r.backend.state.Tasks[index] = clone.Task(*task)
	return r.backend.persist()
}

func (r *taskRepo) Delete(_ context.Context, id core.TaskID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.taskIndex(id)
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	r.backend.state.Tasks = append(r.backend.state.Tasks[:index], r.backend.state.Tasks[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) taskIndex(id core.TaskID) (int, bool) {
	for i, task := range b.state.Tasks {
		if task.ID == id {
			return i, true
		}
	}
	return 0, false
}

type actorRepo struct{ backend *Backend }

func (r *actorRepo) Create(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actorIndex(actor.ID); ok {
		return fmt.Errorf("%w: actor %s", core.ErrAlreadyExists, actor.ID)
	}
	r.backend.state.Actors = append(r.backend.state.Actors, clone.Actor(*actor))
	return r.backend.persist()
}

func (r *actorRepo) Get(_ context.Context, id core.ActorID) (*core.Actor, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.actorIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	actor := clone.Actor(r.backend.state.Actors[index])
	return &actor, nil
}

func (r *actorRepo) FindByName(_ context.Context, name string) (*core.Actor, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	for _, actor := range r.backend.state.Actors {
		if actor.Name == name {
			found := clone.Actor(actor)
			return &found, nil
		}
	}
	return nil, fmt.Errorf("%w: actor named %q", core.ErrNotFound, name)
}

func (r *actorRepo) List(_ context.Context) ([]*core.Actor, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Actor, 0, len(r.backend.state.Actors))
	for _, actor := range r.backend.state.Actors {
		cloned := clone.Actor(actor)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *actorRepo) Update(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.actorIndex(actor.ID)
	if !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, actor.ID)
	}
	r.backend.state.Actors[index] = clone.Actor(*actor)
	return r.backend.persist()
}

func (r *actorRepo) Delete(_ context.Context, id core.ActorID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.actorIndex(id)
	if !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	r.backend.state.Actors = append(r.backend.state.Actors[:index], r.backend.state.Actors[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) actorIndex(id core.ActorID) (int, bool) {
	for i, actor := range b.state.Actors {
		if actor.ID == id {
			return i, true
		}
	}
	return 0, false
}

type artifactRepo struct{ backend *Backend }

func (r *artifactRepo) Create(_ context.Context, artifact *core.Artifact) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.artifactIndex(artifact.ID); ok {
		return fmt.Errorf("%w: artifact %s", core.ErrAlreadyExists, artifact.ID)
	}
	r.backend.state.Artifacts = append(r.backend.state.Artifacts, clone.Artifact(*artifact))
	return r.backend.persist()
}

func (r *artifactRepo) Get(_ context.Context, id core.ArtifactID) (*core.Artifact, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.artifactIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	artifact := clone.Artifact(r.backend.state.Artifacts[index])
	return &artifact, nil
}

func (r *artifactRepo) List(_ context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Artifact, 0, len(r.backend.state.Artifacts))
	for _, artifact := range r.backend.state.Artifacts {
		if !store.MatchesArtifactFilter(&artifact, filter) {
			continue
		}
		cloned := clone.Artifact(artifact)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *artifactRepo) Search(_ context.Context, filter store.ArtifactFilter, query string) ([]store.SearchHit, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.SearchHit, 0)
	for _, artifact := range r.backend.state.Artifacts {
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
	index, ok := r.backend.artifactIndex(artifact.ID)
	if !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	r.backend.state.Artifacts[index] = clone.Artifact(*artifact)
	return r.backend.persist()
}

func (r *artifactRepo) Delete(_ context.Context, id core.ArtifactID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.artifactIndex(id)
	if !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	r.backend.state.Artifacts = append(r.backend.state.Artifacts[:index], r.backend.state.Artifacts[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) artifactIndex(id core.ArtifactID) (int, bool) {
	for i, artifact := range b.state.Artifacts {
		if artifact.ID == id {
			return i, true
		}
	}
	return 0, false
}

type eventRepo struct{ backend *Backend }

func (r *eventRepo) Append(_ context.Context, event *core.Event) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	r.backend.state.Events = append(r.backend.state.Events, clone.Event(*event))
	return r.backend.persist()
}

func (r *eventRepo) List(_ context.Context, filter store.EventFilter) ([]*core.Event, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Event, 0)
	for i := len(r.backend.state.Events) - 1; i >= 0; i-- {
		event := r.backend.state.Events[i]
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
