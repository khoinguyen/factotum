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
	backend  store.Backend
	clock    Clock
	ids      IDGen
	reminder CaptureReminder
}

// CaptureReminder is notified when an executable task's status transition
// resolves it, so a capture whose every task just finished can be handed to QA.
// The QA service implements it.
type CaptureReminder interface {
	RemindResolvedCapture(ctx context.Context, task *core.Ticket) (*core.Message, error)
}

func NewTicketService(backend store.Backend, clock Clock, ids IDGen) *TicketService {
	return &TicketService{backend: backend, clock: clock, ids: ids}
}

// SetCaptureReminder wires the notification sent when a task's resolution
// completes its origin capture. Without one no reminder is sent, so a caller
// that does not run the chief loop stays quiet; the CLI wires the QA service so
// the actor that finished a capture is prompted to dispatch QA.
func (s *TicketService) SetCaptureReminder(reminder CaptureReminder) {
	s.reminder = reminder
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
	// OriginArtifact is the prompt document a capture is groomed from, when its
	// provenance is a document rather than another capture.
	OriginArtifact *core.ArtifactID
}

func (s *TicketService) Add(ctx context.Context, in TicketInput) (*core.Ticket, error) {
	task, err := s.buildTicket(ctx, in)
	if err != nil {
		return nil, err
	}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Tickets().Create(ctx, task); err != nil {
		return nil, err
	}
	if err := s.recordTaskCreated(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

// buildTicket validates the input against its project and builds the ticket Add
// stores. It does not write, so a caller can add store-level data (the origin
// edge of a promotion) before validating and creating it.
func (s *TicketService) buildTicket(ctx context.Context, in TicketInput) (*core.Ticket, error) {
	project, err := s.backend.Projects().Get(ctx, in.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("task project: %w", err)
	}
	if err := CheckRepo(project, in.Repo); err != nil {
		return nil, err
	}
	if in.OriginArtifact != nil {
		if err := checkOriginArtifact(ctx, s.backend, project.ID, *in.OriginArtifact); err != nil {
			return nil, err
		}
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
	return &core.Ticket{
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
		OriginArtifact:     in.OriginArtifact,
		CreatedAt:          now,
		UpdatedAt:          now,
	}, nil
}

// recordTaskCreated appends the event a newly stored task emits.
func (s *TicketService) recordTaskCreated(ctx context.Context, task *core.Ticket) error {
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: task.ProjectID,
		TicketID:  &task.ID,
		Kind:      core.EventTaskCreated,
		Summary:   fmt.Sprintf("added %s %s: %s", task.Kind, task.ID, task.Title),
	})
}

func (s *TicketService) Get(ctx context.Context, id core.TicketID) (*core.Ticket, error) {
	task, err := s.backend.Tickets().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	return repairNoteLinks(task), nil
}

func (s *TicketService) List(ctx context.Context, filter store.TicketFilter) ([]*core.Ticket, error) {
	tasks, err := s.backend.Tickets().List(ctx, filter)
	if err != nil {
		return nil, err
	}
	for _, task := range tasks {
		repairNoteLinks(task)
	}
	return tasks, nil
}

// repairNoteLinks applies core.RepairLinks to a ticket's note links. A note
// written before the url and rune rules existed can still carry a bad link, and
// a task update revalidates every note, so without the repair one legacy note
// would wedge the whole task. Get and List repair what they hand out; an update
// path repairs before validating, so the write persists the repair.
func repairNoteLinks(task *core.Ticket) *core.Ticket {
	for i := range task.Notes {
		task.Notes[i].Links = core.RepairLinks(task.Notes[i].Links)
	}
	return task
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
		tasks = append(tasks, repairNoteLinks(hit.Ticket))
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
//
// Promotion is idempotent and safe under concurrency: a capture owns at most one
// promoted task, and the store guards that with an atomic create
// (TicketRepo.CreatePromoted) rather than a caller's check-then-write, so N
// parallel promotes in one process all return the one task and only the winner
// records the created event and the note. A failed promote deletes the task it
// created, so it leaves no half-promoted capture or orphan task.
func (s *TicketService) Promote(ctx context.Context, captureID core.TicketID, in PromoteInput) (*core.Ticket, error) {
	capture, err := s.backend.Tickets().Get(ctx, captureID)
	if err != nil {
		return nil, err
	}
	if !capture.Kind.CapturedByHuman() {
		return nil, fmt.Errorf("%w: %s is not a capture", core.ErrInvalid, captureID)
	}
	// A re-run returns the promoted task before requiring the grooming flags, so
	// re-running a promotion never demands them again. This read only shapes that
	// error path: the invariant is the store guard below, which decides the race.
	if existing, err := s.backend.Tickets().Promoted(ctx, capture.ID); err != nil {
		return nil, err
	} else if existing != nil {
		return existing, nil
	}
	if len(in.AcceptanceCriteria) == 0 {
		return nil, fmt.Errorf("%w: promote %s: at least one acceptance criterion is required; groom the %s first (--acceptance)",
			core.ErrInvalid, captureID, capture.Kind)
	}
	if in.AssigneeID == nil {
		return nil, fmt.Errorf("%w: promote %s: an agent assignee is required; groom the %s first (--actor)",
			core.ErrInvalid, captureID, capture.Kind)
	}
	// Identity accepts a loop role as an agent, so grooming can hand a capture
	// to a role mailbox with no Actor record, as claim and assign can.
	assignee, err := actorIdentity(ctx, s.backend.Actors(), *in.AssigneeID)
	if err != nil {
		return nil, fmt.Errorf("promote assignee: %w", err)
	}
	if assignee.Kind != core.ActorAgent {
		return nil, fmt.Errorf("%w: promote %s: assignee %s is a %s, not an agent; groom the %s first",
			core.ErrInvalid, captureID, assignee.ID, assignee.Kind, capture.Kind)
	}
	task, err := s.buildTicket(ctx, TicketInput{
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
	// The origin edge is written with the task, in the store's atomic create; a
	// fresh task whose only edge points at an existing capture cannot close a
	// cycle, so the graph needs no check.
	task.Deps = []core.TicketID{capture.ID}
	if err := task.Validate(); err != nil {
		return nil, err
	}
	promoted, created, err := s.backend.Tickets().CreatePromoted(ctx, capture.ID, task)
	if err != nil {
		return nil, err
	}
	if !created {
		return promoted, nil
	}
	if err := s.recordTaskCreated(ctx, promoted); err != nil {
		return nil, s.rollbackPromotion(ctx, promoted.ID, err)
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: promoted.ProjectID,
		TicketID:  &promoted.ID,
		Kind:      core.EventTaskDepAdded,
		Summary:   fmt.Sprintf("%s now depends on %s", promoted.ID, capture.ID),
		Data:      map[string]any{"dep": string(capture.ID)},
	}); err != nil {
		return nil, s.rollbackPromotion(ctx, promoted.ID, err)
	}
	if _, err := s.AddNote(ctx, capture.ID, NoteInput{
		Body:   fmt.Sprintf("Promoted to %s.", promoted.ID),
		System: true,
	}); err != nil {
		return nil, s.rollbackPromotion(ctx, promoted.ID, err)
	}
	return promoted, nil
}

// rollbackPromotion deletes the task a failed promotion created, so the capture
// and its dependents return to their prior state.
func (s *TicketService) rollbackPromotion(ctx context.Context, taskID core.TicketID, cause error) error {
	if err := s.Delete(ctx, taskID); err != nil {
		return errors.Join(cause, fmt.Errorf("promote: roll back %s: %w", taskID, err))
	}
	return cause
}

// Dependencies splits a ticket's dependency edges into blocking dependencies
// and non-blocking capture edges, preserving the stored order within each. An
// executable target (a task or milestone) gates the ticket, so it is a
// dependency; a capture target (an idea or bug) is provenance, the edge that
// records the capture a task came from or is grouped under. A dangling target
// is unknown, and the graph treats a missing edge as unresolved, so it is a
// dependency.
func (s *TicketService) Dependencies(ctx context.Context, task *core.Ticket) (deps, captures []core.Ticket, err error) {
	for _, dep := range task.Deps {
		depTask, err := s.backend.Tickets().Get(ctx, dep)
		switch {
		case errors.Is(err, core.ErrNotFound):
			deps = append(deps, core.Ticket{ID: dep})
		case err != nil:
			return nil, nil, err
		case depTask.Kind.CapturedByHuman():
			captures = append(captures, *depTask)
		default:
			deps = append(deps, *depTask)
		}
	}
	return deps, captures, nil
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

// checkOriginArtifact validates the prompt document a capture is groomed from:
// it must exist and belong to the same project, so provenance never dangles or
// crosses a project boundary.
func checkOriginArtifact(ctx context.Context, backend store.Backend, projectID core.ProjectID, artifactID core.ArtifactID) error {
	artifact, err := backend.Artifacts().Get(ctx, artifactID)
	if err != nil {
		return fmt.Errorf("origin artifact: %w", err)
	}
	if artifact.ProjectID != projectID {
		return fmt.Errorf("%w: origin artifact %s belongs to project %s, not %s",
			core.ErrInvalid, artifactID, artifact.ProjectID, projectID)
	}
	return nil
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
	repairNoteLinks(task)
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
	if set.Status != nil && *set.Status == core.StatusDone && from != core.StatusDone && task.Kind.CapturedByHuman() {
		if err := captureAcceptGate(ctx, s.backend, task); err != nil {
			return nil, err
		}
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
	if s.reminder != nil && set.Status != nil && resolvedTaskStatus(*set.Status) && !resolvedTaskStatus(from) {
		if _, err := s.reminder.RemindResolvedCapture(ctx, task); err != nil {
			return nil, err
		}
	}
	return task, nil
}

// resolvedTaskStatus reports whether a status resolves a task under the feature
// resolution policy, so reaching it can complete the capture it was promoted
// from.
func resolvedTaskStatus(status core.TicketStatus) bool {
	return featurePolicy().Resolves(core.KindTask, status)
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
	// Identity accepts a loop role as well as a record, matching claim: a role
	// is a mailbox a session registers as a run, so it needs no Actor record to
	// hold work.
	if actorID != nil {
		if _, err := actorIdentity(ctx, s.backend.Actors(), *actorID); err != nil {
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
	// Identity accepts a loop role as well as a record: a role is a mailbox a
	// session registers as a run, so it needs no Actor record to hold work.
	if _, err := actorIdentity(ctx, s.backend.Actors(), actorID); err != nil {
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
	repairNoteLinks(task)
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
		if _, err := actorIdentity(ctx, s.backend.Actors(), actorID); err != nil {
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
	repairNoteLinks(task)
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

// GeneratedNoteRef identifies a note a generated-note backfill marked, or would
// mark in a dry run. Body carries the note text so the command can quote it: with
// the author guard inert on old data, the body is what a reviewer checks.
type GeneratedNoteRef struct {
	TaskID core.TicketID
	NoteID string
	Body   string
}

// GeneratedNoteBackfill is the outcome of one backfill pass: the tickets
// inspected and the notes that now carry the system marker (or, in a dry run,
// the notes that would).
type GeneratedNoteBackfill struct {
	Scanned int
	Marked  []GeneratedNoteRef
}

// BackfillGeneratedNotes marks historical machine-written notes with the System
// provenance flag, so task search stops indexing them. A note is a candidate
// only when core.IsGeneratedNote reports it: authorless, opening with a known
// generated-note family, and not already marked. Only tickets whose notes changed
// are written, so a second pass writes nothing.
func (s *TicketService) BackfillGeneratedNotes(ctx context.Context, filter store.TicketFilter, dryRun bool) (GeneratedNoteBackfill, error) {
	tasks, err := s.backend.Tickets().List(ctx, filter)
	if err != nil {
		return GeneratedNoteBackfill{}, err
	}
	result := GeneratedNoteBackfill{Scanned: len(tasks)}
	for _, task := range tasks {
		changed := false
		for i := range task.Notes {
			if !core.IsGeneratedNote(task.Notes[i]) {
				continue
			}
			result.Marked = append(result.Marked, GeneratedNoteRef{TaskID: task.ID, NoteID: task.Notes[i].ID, Body: task.Notes[i].Body})
			task.Notes[i].System = true
			changed = true
		}
		if !changed || dryRun {
			continue
		}
		task.UpdatedAt = s.clock.Now()
		if err := s.backend.Tickets().Update(ctx, task); err != nil {
			return GeneratedNoteBackfill{}, err
		}
	}
	return result, nil
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
