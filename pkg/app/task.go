package app

import (
	"context"
	"fmt"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/store"
)

type TaskService struct {
	backend store.Backend
	clock   Clock
	ids     IDGen
}

func NewTaskService(backend store.Backend, clock Clock, ids IDGen) *TaskService {
	return &TaskService{backend: backend, clock: clock, ids: ids}
}

type TaskInput struct {
	ID          *core.TaskID
	ProjectID   core.ProjectID
	Repo        string
	Kind        core.TaskKind
	Title       string
	Description string
	Priority    int
	Labels      []string
	AssigneeID  *core.ActorID
	WaitingOn   []core.ActorID
	Milestone   *core.MilestoneMeta
}

func (s *TaskService) Add(ctx context.Context, in TaskInput) (*core.Task, error) {
	project, err := s.backend.Projects().Get(ctx, in.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("task project: %w", err)
	}
	if err := checkRepo(project, in.Repo); err != nil {
		return nil, err
	}
	kind := in.Kind
	if kind == "" {
		kind = core.KindTask
	}
	id := core.TaskID(s.ids.NewID("t"))
	if in.ID != nil {
		id = *in.ID
	}
	now := s.clock.Now()
	task := &core.Task{
		ID:          id,
		ProjectID:   in.ProjectID,
		Repo:        in.Repo,
		Kind:        kind,
		Title:       in.Title,
		Description: in.Description,
		Status:      core.StatusTodo,
		AssigneeID:  in.AssigneeID,
		WaitingOn:   in.WaitingOn,
		Labels:      in.Labels,
		Priority:    in.Priority,
		Milestone:   in.Milestone,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Tasks().Create(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		TaskID:    &task.ID,
		Kind:      core.EventTaskCreated,
		Summary:   fmt.Sprintf("added %s %s: %s", task.Kind, task.ID, task.Title),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) Get(ctx context.Context, id core.TaskID) (*core.Task, error) {
	return s.backend.Tasks().Get(ctx, id)
}

func (s *TaskService) List(ctx context.Context, filter store.TaskFilter) ([]*core.Task, error) {
	return s.backend.Tasks().List(ctx, filter)
}

type TaskUpdate struct {
	Kind        *core.TaskKind
	Repo        *string
	Title       *string
	Description *string
	Priority    *int
	Labels      []string
}

func (s *TaskService) Update(ctx context.Context, id core.TaskID, patch TaskUpdate) (*core.Task, error) {
	return s.Set(ctx, id, TaskSet{
		Kind:        patch.Kind,
		Repo:        patch.Repo,
		Title:       patch.Title,
		Description: patch.Description,
		Priority:    patch.Priority,
		Labels:      patch.Labels,
	})
}

// TaskSet is a partial update to a task. A nil field is left unchanged; a
// non-nil Labels slice replaces the existing labels (an empty slice clears
// them). When Expect is set, the write is a compare-and-swap against the
// task's UpdatedAt and fails with ErrConflict if the task changed since it was
// read.
type TaskSet struct {
	Kind        *core.TaskKind
	Repo        *string
	Title       *string
	Description *string
	Priority    *int
	Status      *core.TaskStatus
	Labels      []string
	Expect      *time.Time
}

func (set TaskSet) hasNonStatus() bool {
	return set.Kind != nil || set.Repo != nil || set.Title != nil ||
		set.Description != nil || set.Priority != nil || set.Labels != nil
}

// Empty reports whether the set carries no changes.
func (set TaskSet) Empty() bool {
	return !set.hasNonStatus() && set.Status == nil
}

// Set applies a partial update. Setting the status emits a status-changed
// event; any other field emits an updated event. It is the single code path
// behind `task set`, `task update`, and the status transition commands.
func (s *TaskService) Set(ctx context.Context, id core.TaskID, set TaskSet) (*core.Task, error) {
	if set.Status != nil && !set.Status.Valid() {
		return nil, fmt.Errorf("%w: unknown status %q", core.ErrInvalid, *set.Status)
	}
	task, err := s.backend.Tasks().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	from := task.Status
	if set.Kind != nil {
		if !set.Kind.Valid() {
			return nil, fmt.Errorf("%w: unknown task kind %q", core.ErrInvalid, *set.Kind)
		}
		task.Kind = *set.Kind
	}
	if set.Repo != nil {
		project, err := s.backend.Projects().Get(ctx, task.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := checkRepo(project, *set.Repo); err != nil {
			return nil, err
		}
		task.Repo = *set.Repo
	}
	if set.Title != nil {
		task.Title = *set.Title
	}
	if set.Description != nil {
		task.Description = *set.Description
	}
	if set.Priority != nil {
		task.Priority = *set.Priority
	}
	if set.Labels != nil {
		task.Labels = set.Labels
	}
	if set.Status != nil {
		task.Status = *set.Status
	}
	task.UpdatedAt = s.clock.Now()
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if set.Expect != nil {
		if err := s.backend.Tasks().UpdateExpected(ctx, task, *set.Expect); err != nil {
			return nil, err
		}
	} else if err := s.backend.Tasks().Update(ctx, task); err != nil {
		return nil, err
	}
	if set.Status != nil {
		if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
			ProjectID: task.ProjectID,
			TaskID:    &task.ID,
			Kind:      core.EventTaskStatusChanged,
			Summary:   fmt.Sprintf("%s %s -> %s", task.ID, from, *set.Status),
			Data:      map[string]any{"from": string(from), "to": string(*set.Status)},
		}); err != nil {
			return nil, err
		}
	}
	if set.hasNonStatus() {
		if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
			ProjectID: task.ProjectID,
			TaskID:    &task.ID,
			Kind:      core.EventTaskUpdated,
			Summary:   fmt.Sprintf("updated %s", task.ID),
		}); err != nil {
			return nil, err
		}
	}
	return task, nil
}

func (s *TaskService) SetStatus(ctx context.Context, id core.TaskID, status core.TaskStatus) (*core.Task, error) {
	if !status.Valid() {
		return nil, fmt.Errorf("%w: unknown status %q", core.ErrInvalid, status)
	}
	return s.Set(ctx, id, TaskSet{Status: &status})
}

func (s *TaskService) Assign(ctx context.Context, id core.TaskID, actorID *core.ActorID) (*core.Task, error) {
	task, err := s.backend.Tasks().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if actorID != nil {
		if _, err := s.backend.Actors().Get(ctx, *actorID); err != nil {
			return nil, fmt.Errorf("assignee: %w", err)
		}
	}
	task.AssigneeID = actorID
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tasks().Update(ctx, task); err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("unassigned %s", task.ID)
	if actorID != nil {
		summary = fmt.Sprintf("assigned %s to %s", task.ID, *actorID)
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TaskID:    &task.ID,
		Kind:      core.EventTaskAssigned,
		Summary:   summary,
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) SetWaitingOn(ctx context.Context, id core.TaskID, actorIDs []core.ActorID) (*core.Task, error) {
	task, err := s.backend.Tasks().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, actorID := range actorIDs {
		if _, err := s.backend.Actors().Get(ctx, actorID); err != nil {
			return nil, fmt.Errorf("waiting on: %w", err)
		}
	}
	task.WaitingOn = actorIDs
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tasks().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TaskID:    &task.ID,
		Kind:      core.EventTaskWaitingOnChanged,
		Summary:   fmt.Sprintf("updated waiting-on for %s", task.ID),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) AddDep(ctx context.Context, id, depID core.TaskID) (*core.Task, error) {
	if id == depID {
		return nil, fmt.Errorf("%w: task cannot depend on itself", core.ErrInvalid)
	}
	task, err := s.backend.Tasks().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.backend.Tasks().Get(ctx, depID); err != nil {
		return nil, fmt.Errorf("dependency: %w", err)
	}
	for _, dep := range task.Deps {
		if dep == depID {
			return nil, fmt.Errorf("%w: dependency %s already exists", core.ErrConflict, depID)
		}
	}
	if err := s.wouldCycle(ctx, task.ProjectID, id, depID); err != nil {
		return nil, err
	}

	task.Deps = append(task.Deps, depID)
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tasks().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TaskID:    &task.ID,
		Kind:      core.EventTaskDepAdded,
		Summary:   fmt.Sprintf("%s now depends on %s", task.ID, depID),
		Data:      map[string]any{"dep": string(depID)},
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) RemoveDep(ctx context.Context, id, depID core.TaskID) (*core.Task, error) {
	task, err := s.backend.Tasks().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	index := -1
	for i, dep := range task.Deps {
		if dep == depID {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("%w: dependency %s", core.ErrNotFound, depID)
	}
	task.Deps = append(task.Deps[:index], task.Deps[index+1:]...)
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tasks().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TaskID:    &task.ID,
		Kind:      core.EventTaskDepRemoved,
		Summary:   fmt.Sprintf("%s no longer depends on %s", task.ID, depID),
		Data:      map[string]any{"dep": string(depID)},
	}); err != nil {
		return nil, err
	}
	return task, nil
}

type NoteInput struct {
	Body   string
	Links  []core.Link
	Author *core.ActorID
}

func (s *TaskService) AddNote(ctx context.Context, id core.TaskID, in NoteInput) (*core.Task, error) {
	task, err := s.backend.Tasks().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	note := core.Note{
		ID:        s.ids.NewID("note"),
		Body:      in.Body,
		Links:     in.Links,
		CreatedAt: s.clock.Now(),
	}
	if in.Author != nil {
		note.Author = *in.Author
	}
	if err := note.Validate(); err != nil {
		return nil, err
	}
	task.Notes = append(task.Notes, note)
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tasks().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TaskID:    &task.ID,
		Kind:      core.EventTaskNoteAdded,
		Summary:   fmt.Sprintf("noted on %s", task.ID),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TaskService) Delete(ctx context.Context, id core.TaskID) error {
	task, err := s.backend.Tasks().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.backend.Tasks().Delete(ctx, id); err != nil {
		return err
	}
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TaskID:    &id,
		Kind:      core.EventTaskDeleted,
		Summary:   fmt.Sprintf("deleted %s", id),
	})
}

func checkRepo(project *core.Project, repo string) error {
	if repo == "" {
		return nil
	}
	for _, candidate := range project.Repos {
		if candidate.Name == repo {
			return nil
		}
	}
	return fmt.Errorf("%w: repository %q is not part of project %s", core.ErrInvalid, repo, project.ID)
}

func (s *TaskService) wouldCycle(ctx context.Context, projectID core.ProjectID, taskID, depID core.TaskID) error {
	project, err := s.backend.Projects().Get(ctx, projectID)
	if err != nil {
		return err
	}
	tasks, err := s.backend.Tasks().List(ctx, store.TaskFilter{ProjectID: projectID})
	if err != nil {
		return err
	}
	copies := make([]core.Task, 0, len(tasks))
	for _, task := range tasks {
		copied := *task
		if copied.ID == taskID {
			copied.Deps = append(append([]core.TaskID(nil), copied.Deps...), depID)
		}
		copies = append(copies, copied)
	}
	built, err := graph.New(copies, project.Policy)
	if err != nil {
		return err
	}
	if built.HasCycle() {
		return fmt.Errorf("%w: adding dependency %s -> %s would create a cycle", core.ErrCycle, taskID, depID)
	}
	return nil
}
