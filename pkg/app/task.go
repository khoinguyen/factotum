package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/store"
)

type TicketService struct {
	backend store.Backend
	clock   Clock
	ids     IDGen
}

func NewTicketService(backend store.Backend, clock Clock, ids IDGen) *TicketService {
	return &TicketService{backend: backend, clock: clock, ids: ids}
}

type TicketInput struct {
	ID                 *core.TicketID
	ProjectID          core.ProjectID
	Repo               string
	Kind               core.TicketKind
	Title              string
	Description        string
	Priority           int
	Labels             []string
	AssigneeID         *core.ActorID
	WaitingOn          []core.ActorID
	Milestone          *core.MilestoneMeta
	Groomed            bool
	AcceptanceCriteria []string
}

func (s *TicketService) Add(ctx context.Context, in TicketInput) (*core.Ticket, error) {
	project, err := s.backend.Projects().Get(ctx, in.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("task project: %w", err)
	}
	if err := CheckRepo(project, in.Repo); err != nil {
		return nil, err
	}
	kind := in.Kind
	if kind == "" {
		kind = core.KindTask
	}
	id := core.TicketID(s.ids.NewID("t"))
	if in.ID != nil {
		id = *in.ID
	}
	now := s.clock.Now()
	task := &core.Ticket{
		ID:                 id,
		ProjectID:          in.ProjectID,
		Repo:               in.Repo,
		Kind:               kind,
		Title:              in.Title,
		Description:        in.Description,
		Status:             core.StatusTodo,
		AssigneeID:         in.AssigneeID,
		WaitingOn:          in.WaitingOn,
		Labels:             in.Labels,
		Priority:           in.Priority,
		Milestone:          in.Milestone,
		Groomed:            in.Groomed,
		AcceptanceCriteria: in.AcceptanceCriteria,
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Tickets().Create(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskCreated,
		Summary:   fmt.Sprintf("added %s %s: %s", task.Kind, task.ID, task.Title),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TicketService) Get(ctx context.Context, id core.TicketID) (*core.Ticket, error) {
	return s.backend.Tickets().Get(ctx, id)
}

func (s *TicketService) List(ctx context.Context, filter store.TicketFilter) ([]*core.Ticket, error) {
	return s.backend.Tickets().List(ctx, filter)
}

// Search returns the tasks in filter scope whose title, description, or notes
// match the query, in the store's relevance order. An empty query matches
// everything in scope.
func (s *TicketService) Search(ctx context.Context, filter store.TicketFilter, query string) ([]*core.Ticket, error) {
	hits, err := s.backend.Tickets().Search(ctx, filter, query)
	if err != nil {
		return nil, err
	}
	tasks := make([]*core.Ticket, 0, len(hits))
	for _, hit := range hits {
		tasks = append(tasks, hit.Ticket)
	}
	return tasks, nil
}

// PromoteInput is the grooming data a promotion needs: the acceptance criteria
// that define done and the agent the produced task is assigned to. A capture is
// groomed as it is promoted, so both are required - promotion never creates an
// unassigned, ungroomed task.
type PromoteInput struct {
	AssigneeID         *core.ActorID
	AcceptanceCriteria []string
}

// Promote turns a captured ticket (an idea or a bug) into agent-ready work: it
// creates a new task carrying the capture's content, groomed with the supplied
// acceptance criteria and assigned to the supplied agent, records the capture as
// the task's origin edge (a dependency, which never blocks because captures are
// resolved), and leaves the capture untouched as history with a note naming the
// promoted task. Grooming precedes promotion, so a capture without the required
// grooming data is rejected rather than promoted into an unassigned todo.
func (s *TicketService) Promote(ctx context.Context, captureID core.TicketID, in PromoteInput) (*core.Ticket, error) {
	capture, err := s.backend.Tickets().Get(ctx, captureID)
	if err != nil {
		return nil, err
	}
	if !capture.Kind.CapturedByHuman() {
		return nil, fmt.Errorf("%w: %s is not a capture", core.ErrInvalid, captureID)
	}
	if len(in.AcceptanceCriteria) == 0 {
		return nil, fmt.Errorf("%w: promote %s: at least one acceptance criterion is required; groom the %s first (--acceptance)",
			core.ErrInvalid, captureID, capture.Kind)
	}
	if in.AssigneeID == nil {
		return nil, fmt.Errorf("%w: promote %s: an agent assignee is required; groom the %s first (--actor)",
			core.ErrInvalid, captureID, capture.Kind)
	}
	assignee, err := s.backend.Actors().Get(ctx, *in.AssigneeID)
	if err != nil {
		return nil, fmt.Errorf("promote assignee: %w", err)
	}
	if assignee.Kind != core.ActorAgent {
		return nil, fmt.Errorf("%w: promote %s: assignee %s is a %s, not an agent; groom the %s first",
			core.ErrInvalid, captureID, assignee.ID, assignee.Kind, capture.Kind)
	}
	task, err := s.Add(ctx, TicketInput{
		ProjectID:          capture.ProjectID,
		Repo:               capture.Repo,
		Kind:               core.KindTask,
		Title:              capture.Title,
		Description:        capture.Description,
		Priority:           capture.Priority,
		Labels:             append([]string(nil), capture.Labels...),
		AssigneeID:         in.AssigneeID,
		Groomed:            true,
		AcceptanceCriteria: in.AcceptanceCriteria,
	})
	if err != nil {
		return nil, err
	}
	task, err = s.AddDep(ctx, task.ID, captureID)
	if err != nil {
		return nil, err
	}
	if _, err := s.AddNote(ctx, captureID, NoteInput{
		Body:   fmt.Sprintf("Promoted to %s.", task.ID),
		System: true,
	}); err != nil {
		return nil, err
	}
	return task, nil
}

// Origin returns the immutable capture (idea or bug) this ticket was refined
// from, or nil when it has none. Promotion writes the capture as the ticket's
// first dependency; the first capture dependency wins.
func (s *TicketService) Origin(ctx context.Context, task *core.Ticket) (*core.Ticket, error) {
	return originTicket(ctx, s.backend, task)
}

// originTicket resolves the capture a ticket was refined from: the first
// dependency that is a human capture. A dangling dependency is history, not an
// origin, so it is skipped rather than failing the read.
func originTicket(ctx context.Context, backend store.Backend, task *core.Ticket) (*core.Ticket, error) {
	for _, dep := range task.Deps {
		depTask, err := backend.Tickets().Get(ctx, dep)
		if errors.Is(err, core.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if depTask.Kind.CapturedByHuman() {
			return depTask, nil
		}
	}
	return nil, nil
}

type TicketUpdate struct {
	Kind               *core.TicketKind
	Repo               *string
	Title              *string
	Description        *string
	Priority           *int
	Labels             []string
	Groomed            *bool
	AcceptanceCriteria []string
}

func (s *TicketService) Update(ctx context.Context, id core.TicketID, patch TicketUpdate) (*core.Ticket, error) {
	return s.Set(ctx, id, TicketSet{
		Kind:               patch.Kind,
		Repo:               patch.Repo,
		Title:              patch.Title,
		Description:        patch.Description,
		Priority:           patch.Priority,
		Labels:             patch.Labels,
		Groomed:            patch.Groomed,
		AcceptanceCriteria: patch.AcceptanceCriteria,
	})
}

// TicketSet is a partial update to a task. A nil field is left unchanged; a
// non-nil Labels slice replaces the existing labels (an empty slice clears
// them). When Expect is set, the write is a compare-and-swap against the
// task's UpdatedAt and fails with ErrConflict if the task changed since it was
// read.
type TicketSet struct {
	Kind               *core.TicketKind
	Repo               *string
	Title              *string
	Description        *string
	Priority           *int
	Status             *core.TicketStatus
	Labels             []string
	Groomed            *bool
	AcceptanceCriteria []string
	NotBefore          *time.Time
	ClearNotBefore     bool
	Expect             *time.Time
}

func (set TicketSet) hasNonStatus() bool {
	return set.Kind != nil || set.Repo != nil || set.Title != nil ||
		set.Description != nil || set.Priority != nil || set.Labels != nil ||
		set.Groomed != nil || set.AcceptanceCriteria != nil ||
		set.NotBefore != nil || set.ClearNotBefore
}

// Empty reports whether the set carries no changes.
func (set TicketSet) Empty() bool {
	return !set.hasNonStatus() && set.Status == nil
}

// Set applies a partial update. Setting the status emits a status-changed
// event; any other field emits an updated event. It is the single code path
// behind `task set`, `task update`, and the status transition commands.
func (s *TicketService) Set(ctx context.Context, id core.TicketID, set TicketSet) (*core.Ticket, error) {
	if set.Status != nil && !set.Status.Valid() {
		return nil, fmt.Errorf("%w: unknown status %q", core.ErrInvalid, *set.Status)
	}
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	from := task.Status
	if set.Kind != nil {
		if !set.Kind.Valid() {
			return nil, fmt.Errorf("%w: unknown task kind %q", core.ErrInvalid, *set.Kind)
		}
		// Crossing between a capture and an executable kind is never an
		// in-place edit: promoting is an explicit, linked, history-preserving
		// operation, and demoting executable work would silently drop it from
		// the graph.
		if *set.Kind != task.Kind && (task.Kind.CapturedByHuman() || set.Kind.CapturedByHuman()) {
			return nil, fmt.Errorf("%w: cannot change kind to or from a capture in place; use `ft task promote` to turn a capture into a task", core.ErrInvalid)
		}
		task.Kind = *set.Kind
	}
	if set.Repo != nil {
		project, err := s.backend.Projects().Get(ctx, task.ProjectID)
		if err != nil {
			return nil, err
		}
		if err := CheckRepo(project, *set.Repo); err != nil {
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
	if set.Groomed != nil {
		task.Groomed = *set.Groomed
	}
	if set.AcceptanceCriteria != nil {
		task.AcceptanceCriteria = set.AcceptanceCriteria
	}
	if set.ClearNotBefore {
		task.NotBefore = nil
	} else if set.NotBefore != nil {
		notBefore := *set.NotBefore
		task.NotBefore = &notBefore
	}
	if set.Status != nil {
		task.Status = *set.Status
	}
	task.UpdatedAt = s.clock.Now()
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if set.Expect != nil {
		if err := s.backend.Tickets().UpdateExpected(ctx, task, *set.Expect); err != nil {
			return nil, err
		}
	} else if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	if set.Status != nil {
		if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
			ProjectID: task.ProjectID,
			TicketID:  &task.ID,
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
			TicketID:  &task.ID,
			Kind:      core.EventTaskUpdated,
			Summary:   fmt.Sprintf("updated %s", task.ID),
		}); err != nil {
			return nil, err
		}
	}
	return task, nil
}

func (s *TicketService) SetStatus(ctx context.Context, id core.TicketID, status core.TicketStatus) (*core.Ticket, error) {
	if !status.Valid() {
		return nil, fmt.Errorf("%w: unknown status %q", core.ErrInvalid, status)
	}
	return s.Set(ctx, id, TicketSet{Status: &status})
}

func (s *TicketService) Assign(ctx context.Context, id core.TicketID, actorID *core.ActorID) (*core.Ticket, error) {
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if actorID != nil && task.Kind.CapturedByHuman() {
		return nil, fmt.Errorf("%w: %s %s is not assignable", core.ErrInvalid, task.Kind, id)
	}
	if actorID != nil {
		if _, err := s.backend.Actors().Get(ctx, *actorID); err != nil {
			return nil, fmt.Errorf("assignee: %w", err)
		}
	}
	task.AssigneeID = actorID
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	summary := fmt.Sprintf("unassigned %s", task.ID)
	if actorID != nil {
		summary = fmt.Sprintf("assigned %s to %s", task.ID, *actorID)
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskAssigned,
		Summary:   summary,
	}); err != nil {
		return nil, err
	}
	return task, nil
}

// Claim assigns the task to actorID only if it is still unassigned (or already
// theirs), using a compare-and-swap on UpdatedAt so concurrent claimants do not
// both win. It returns ErrConflict when someone else holds the task. When start
// is true it also moves the task to in_progress in the same CAS, so there is no
// window where the task is claimed but not started.
func (s *TicketService) Claim(ctx context.Context, id core.TicketID, actorID core.ActorID, start bool) (*core.Ticket, error) {
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Kind.CapturedByHuman() {
		return nil, fmt.Errorf("%w: %s %s is not assignable", core.ErrInvalid, task.Kind, id)
	}
	if _, err := s.backend.Actors().Get(ctx, actorID); err != nil {
		return nil, fmt.Errorf("assignee: %w", err)
	}
	if task.AssigneeID != nil && *task.AssigneeID != actorID {
		return nil, fmt.Errorf("%w: task %s is already assigned to %s", core.ErrConflict, id, *task.AssigneeID)
	}
	from := task.Status
	expected := task.UpdatedAt
	task.AssigneeID = &actorID
	if start {
		task.Status = core.StatusInProgress
	}
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tickets().UpdateExpected(ctx, task, expected); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskAssigned,
		Summary:   fmt.Sprintf("claimed %s by %s", task.ID, actorID),
	}); err != nil {
		return nil, err
	}
	if start {
		if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
			ProjectID: task.ProjectID,
			TicketID:  &task.ID,
			Kind:      core.EventTaskStatusChanged,
			Summary:   fmt.Sprintf("%s %s -> %s", task.ID, from, task.Status),
			Data:      map[string]any{"from": string(from), "to": string(task.Status)},
		}); err != nil {
			return nil, err
		}
	}
	return task, nil
}

// Snooze parks a task out of ranking until the given condition passes. A date
// or task condition clears itself once met; indefinite lasts until Unsnooze.
func (s *TicketService) Snooze(ctx context.Context, id core.TicketID, snooze core.Snooze) (*core.Ticket, error) {
	if err := snooze.Validate(); err != nil {
		return nil, err
	}
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if snooze.UntilTask != nil {
		if *snooze.UntilTask == id {
			return nil, fmt.Errorf("%w: a task cannot snooze until itself", core.ErrInvalid)
		}
		if _, err := s.backend.Tickets().Get(ctx, *snooze.UntilTask); err != nil {
			return nil, fmt.Errorf("snooze until task: %w", err)
		}
		// Reject a logical deadlock: if the until-task depends on this task
		// (directly or transitively), neither can ever resolve. UntilTask is not
		// a dependency edge, so the cycle detector does not see it.
		dependents, err := s.transitiveDependents(ctx, task.ProjectID, id)
		if err != nil {
			return nil, err
		}
		for _, dependent := range dependents {
			if dependent == *snooze.UntilTask {
				return nil, fmt.Errorf("%w: %s cannot snooze until %s because %s depends on it", core.ErrInvalid, id, *snooze.UntilTask, *snooze.UntilTask)
			}
		}
		if err := s.rejectSnoozeCycle(ctx, task, *snooze.UntilTask); err != nil {
			return nil, err
		}
	}
	stored := snooze
	if snooze.Until != nil {
		until := *snooze.Until
		stored.Until = &until
	}
	if snooze.UntilTask != nil {
		untilTask := *snooze.UntilTask
		stored.UntilTask = &untilTask
	}
	task.Snooze = &stored
	task.UpdatedAt = s.clock.Now()
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskSnoozed,
		Summary:   fmt.Sprintf("snoozed %s %s", task.ID, SnoozeDescription(stored)),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

// transitiveDependents returns the tasks that transitively depend on id in the
// project's graph.
func (s *TicketService) transitiveDependents(ctx context.Context, projectID core.ProjectID, id core.TicketID) ([]core.TicketID, error) {
	tasks, err := s.backend.Tickets().List(ctx, store.TicketFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	copied := make([]core.Ticket, 0, len(tasks))
	for _, task := range tasks {
		copied = append(copied, *task)
	}
	built, err := graph.New(copied, core.DefaultResolutionPolicy())
	if err != nil {
		return nil, err
	}
	return built.TransitiveDependents(id), nil
}

// rejectSnoozeCycle rejects a snooze whose until-task is itself (transitively)
// snoozed until the snoozed task. That is a waits-for cycle with no dependency
// edges, so the graph cycle detector and the dependency-based check miss it.
func (s *TicketService) rejectSnoozeCycle(ctx context.Context, task *core.Ticket, untilTask core.TicketID) error {
	seen := make(map[core.TicketID]bool)
	for current := untilTask; current != "" && !seen[current]; {
		if current == task.ID {
			return fmt.Errorf("%w: %s cannot snooze until %s: it would create a snooze cycle", core.ErrInvalid, task.ID, untilTask)
		}
		seen[current] = true
		next, err := s.backend.Tickets().Get(ctx, current)
		if errors.Is(err, core.ErrNotFound) {
			return nil // a dangling UntilTask ends the chain
		}
		if err != nil {
			return err
		}
		if next.Snooze == nil || next.Snooze.UntilTask == nil {
			return nil
		}
		current = *next.Snooze.UntilTask
	}
	return nil
}

// Unsnooze removes a task's snooze.
func (s *TicketService) Unsnooze(ctx context.Context, id core.TicketID) (*core.Ticket, error) {
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task.Snooze == nil {
		return nil, fmt.Errorf("%w: task %s is not snoozed", core.ErrInvalid, id)
	}
	task.Snooze = nil
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskUnsnoozed,
		Summary:   fmt.Sprintf("unsnoozed %s", task.ID),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

// SnoozeDescription renders a snooze condition for humans.
func SnoozeDescription(snooze core.Snooze) string {
	return snooze.Describe()
}

func (s *TicketService) SetWaitingOn(ctx context.Context, id core.TicketID, actorIDs []core.ActorID) (*core.Ticket, error) {
	task, err := s.backend.Tickets().Get(ctx, id)
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
	if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskWaitingOnChanged,
		Summary:   fmt.Sprintf("updated waiting-on for %s", task.ID),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TicketService) AddDep(ctx context.Context, id, depID core.TicketID) (*core.Ticket, error) {
	if id == depID {
		return nil, fmt.Errorf("%w: task cannot depend on itself", core.ErrInvalid)
	}
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if _, err := s.backend.Tickets().Get(ctx, depID); err != nil {
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
	if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskDepAdded,
		Summary:   fmt.Sprintf("%s now depends on %s", task.ID, depID),
		Data:      map[string]any{"dep": string(depID)},
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TicketService) RemoveDep(ctx context.Context, id, depID core.TicketID) (*core.Ticket, error) {
	task, err := s.backend.Tickets().Get(ctx, id)
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
	if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
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
	System bool
}

func (s *TicketService) AddNote(ctx context.Context, id core.TicketID, in NoteInput) (*core.Ticket, error) {
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	note := core.Note{
		ID:        s.ids.NewID("note"),
		Body:      in.Body,
		Links:     in.Links,
		CreatedAt: s.clock.Now(),
		System:    in.System,
	}
	if in.Author != nil {
		note.Author = *in.Author
	}
	if err := note.Validate(); err != nil {
		return nil, err
	}
	task.Notes = append(task.Notes, note)
	task.UpdatedAt = s.clock.Now()
	if err := s.backend.Tickets().Update(ctx, task); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskNoteAdded,
		Summary:   fmt.Sprintf("noted on %s", task.ID),
	}); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *TicketService) Delete(ctx context.Context, id core.TicketID) error {
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.backend.Tickets().Delete(ctx, id); err != nil {
		return err
	}
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &id,
		Kind:      core.EventTaskDeleted,
		Summary:   fmt.Sprintf("deleted %s", id),
	})
}

// CheckRepo reports whether repo belongs to project. An empty repo is always valid.
func CheckRepo(project *core.Project, repo string) error {
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

func (s *TicketService) wouldCycle(ctx context.Context, projectID core.ProjectID, taskID, depID core.TicketID) error {
	project, err := s.backend.Projects().Get(ctx, projectID)
	if err != nil {
		return err
	}
	tasks, err := s.backend.Tickets().List(ctx, store.TicketFilter{ProjectID: projectID})
	if err != nil {
		return err
	}
	copies := make([]core.Ticket, 0, len(tasks))
	for _, task := range tasks {
		copied := *task
		if copied.ID == taskID {
			copied.Deps = append(append([]core.TicketID(nil), copied.Deps...), depID)
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
