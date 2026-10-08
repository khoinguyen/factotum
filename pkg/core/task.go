package core

import (
	"fmt"
	"strings"
	"time"
)

type TicketKind string

const (
	KindTask      TicketKind = "task"
	KindMilestone TicketKind = "milestone"
	// KindIdea is a non-executable capture: an unrefined thought recorded for
	// later grooming. Ideas never appear in readiness or ranking, are not
	// assignable, and are promoted into a task rather than executed in place.
	KindIdea TicketKind = "idea"
)

func (k TicketKind) Valid() bool {
	return k == KindTask || k == KindMilestone || k == KindIdea
}

// KindFamily groups kinds by their role in the workflow: human captures that
// are refined into work, the executable kind an agent runs, and gates that
// hold dependents until a milestone resolves.
type KindFamily string

const (
	// FamilyCapture is a human capture (idea, and later bug): recorded for
	// refinement, never executed in place.
	FamilyCapture KindFamily = "capture"
	// FamilyExecutable is the agent-facing executable kind (task).
	FamilyExecutable KindFamily = "executable"
	// FamilyGate is a milestone that gates its dependents.
	FamilyGate KindFamily = "gate"
)

// Family reports the kind's workflow family, or "" for an unknown kind.
func (k TicketKind) Family() KindFamily {
	switch k {
	case KindTask:
		return FamilyExecutable
	case KindMilestone:
		return FamilyGate
	case KindIdea:
		return FamilyCapture
	default:
		return ""
	}
}

// Executable reports whether a kind participates in execution: readiness,
// ranking, and dependency resolution. Ideas are captures, not work.
func (k TicketKind) Executable() bool {
	return k == KindTask || k == KindMilestone
}

// CapturedByHuman reports whether a kind is a human capture that is refined
// into work rather than executed in place.
func (k TicketKind) CapturedByHuman() bool {
	return k.Family() == FamilyCapture
}

// RefineVerb names the command a human uses to refine a capture into work, or
// "" for kinds that are not refined (they are executed or gate directly).
func (k TicketKind) RefineVerb() string {
	switch k {
	case KindIdea:
		return "groom"
	default:
		return ""
	}
}

// AllowsStatus reports whether a kind may hold a status. Ideas are captures,
// so only todo, done, and cancelled apply; other kinds accept any valid status.
func (k TicketKind) AllowsStatus(s TicketStatus) bool {
	if k == KindIdea {
		return s == StatusTodo || s == StatusDone || s == StatusCancelled
	}
	return s.Valid()
}

type TicketStatus string

const (
	StatusTodo           TicketStatus = "todo"
	StatusInProgress     TicketStatus = "in_progress"
	StatusBlocked        TicketStatus = "blocked"
	StatusReadyForReview TicketStatus = "ready_for_review"
	StatusDone           TicketStatus = "done"
	StatusCancelled      TicketStatus = "cancelled"
)

func (s TicketStatus) Valid() bool {
	switch s {
	case StatusTodo, StatusInProgress, StatusBlocked, StatusReadyForReview, StatusDone, StatusCancelled:
		return true
	default:
		return false
	}
}

type LinkKind string

const (
	LinkPR    LinkKind = "pr"
	LinkIssue LinkKind = "issue"
	LinkDoc   LinkKind = "doc"
	LinkURL   LinkKind = "url"
)

func (k LinkKind) Valid() bool {
	switch k {
	case LinkPR, LinkIssue, LinkDoc, LinkURL:
		return true
	default:
		return false
	}
}

type Link struct {
	Kind  LinkKind
	URL   string
	Title string
}

func (l Link) Validate() error {
	if !l.Kind.Valid() {
		return fmt.Errorf("%w: unknown link kind %q", ErrInvalid, l.Kind)
	}
	if strings.TrimSpace(l.URL) == "" {
		return fmt.Errorf("%w: link url is required", ErrInvalid)
	}
	return nil
}

type Note struct {
	ID        string
	Author    ActorID
	Body      string
	Links     []Link
	CreatedAt time.Time
	// System marks a note ft generated (a triage or reassignment message) rather
	// than one a human or agent wrote. System notes stay visible on the task but
	// are excluded from search indexing as system noise.
	System bool
}

func (n Note) Validate() error {
	if n.ID == "" {
		return fmt.Errorf("%w: note id is required", ErrInvalid)
	}
	if strings.TrimSpace(n.Body) == "" {
		return fmt.Errorf("%w: note body is required", ErrInvalid)
	}
	for _, l := range n.Links {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("note link: %w", err)
		}
	}
	return nil
}

type MilestoneMeta struct {
	TargetDate *time.Time
	ReleaseRef string
}

// Snooze parks a task out of ranking until a condition passes: a date, another
// task resolving, or indefinitely (until explicitly unsnoozed). Exactly one
// condition is set.
type Snooze struct {
	Until      *time.Time
	UntilTask  *TicketID
	Indefinite bool
}

func (s Snooze) Validate() error {
	conditions := 0
	if s.Until != nil {
		conditions++
	}
	if s.UntilTask != nil {
		conditions++
	}
	if s.Indefinite {
		conditions++
	}
	if conditions != 1 {
		return fmt.Errorf("%w: snooze needs exactly one of until, until_task, or indefinite", ErrInvalid)
	}
	return nil
}

// Describe renders a snooze condition for humans.
func (s Snooze) Describe() string {
	switch {
	case s.Indefinite:
		return "indefinitely"
	case s.Until != nil:
		return "until " + s.Until.UTC().Format(time.RFC3339)
	case s.UntilTask != nil:
		return "until " + string(*s.UntilTask)
	default:
		return ""
	}
}

type Ticket struct {
	ID          TicketID
	ProjectID   ProjectID
	Repo        string
	Kind        TicketKind
	Title       string
	Description string
	Status      TicketStatus
	AssigneeID  *ActorID
	WaitingOn   []ActorID
	Labels      []string
	Priority    int
	Deps        []TicketID
	Notes       []Note
	Milestone   *MilestoneMeta
	NotBefore   *time.Time
	Snooze      *Snooze
	// Groomed marks a task as refined enough for an agent to execute on its
	// own: scope, approach, and acceptance criteria are decided. It is set
	// explicitly, never inferred from the "groomed" label or from the presence
	// of acceptance criteria, and it gates agent readiness.
	Groomed bool
	// AcceptanceCriteria are the observable conditions that define done. A
	// groomed task must carry at least one; an ungroomed task may carry a
	// partial set while it is still being refined.
	AcceptanceCriteria []string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ReadyAt reports whether the task's not_before constraint has elapsed at now.
// A task with no constraint is always ready.
func (t Ticket) ReadyAt(now time.Time) bool {
	return t.NotBefore == nil || !t.NotBefore.After(now)
}

func (t Ticket) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: task id is required", ErrInvalid)
	}
	if t.ProjectID == "" {
		return fmt.Errorf("%w: task project id is required", ErrInvalid)
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("%w: task title is required", ErrInvalid)
	}
	if !t.Kind.Valid() {
		return fmt.Errorf("%w: unknown task kind %q", ErrInvalid, t.Kind)
	}
	if !t.Status.Valid() {
		return fmt.Errorf("%w: unknown task status %q", ErrInvalid, t.Status)
	}
	if !t.Kind.AllowsStatus(t.Status) {
		return fmt.Errorf("%w: kind %q does not allow status %q", ErrInvalid, t.Kind, t.Status)
	}
	if t.Kind == KindIdea && t.AssigneeID != nil {
		return fmt.Errorf("%w: idea %s is not assignable", ErrInvalid, t.ID)
	}
	if t.Kind == KindIdea && t.Groomed {
		return fmt.Errorf("%w: idea %s is not groomable", ErrInvalid, t.ID)
	}
	for _, criterion := range t.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return fmt.Errorf("%w: task %s has an empty acceptance criterion", ErrInvalid, t.ID)
		}
	}
	if t.Groomed && len(t.AcceptanceCriteria) == 0 {
		return fmt.Errorf("%w: groomed task %s requires at least one acceptance criterion", ErrInvalid, t.ID)
	}

	seen := make(map[TicketID]struct{}, len(t.Deps))
	for _, dep := range t.Deps {
		if dep == "" {
			return fmt.Errorf("%w: task %s has an empty dependency", ErrInvalid, t.ID)
		}
		if dep == t.ID {
			return fmt.Errorf("%w: task %s cannot depend on itself", ErrInvalid, t.ID)
		}
		if _, ok := seen[dep]; ok {
			return fmt.Errorf("%w: task %s has duplicate dependency %s", ErrInvalid, t.ID, dep)
		}
		seen[dep] = struct{}{}
	}

	for _, n := range t.Notes {
		if err := n.Validate(); err != nil {
			return fmt.Errorf("task note: %w", err)
		}
	}

	if t.Snooze != nil {
		if err := t.Snooze.Validate(); err != nil {
			return fmt.Errorf("task snooze: %w", err)
		}
	}

	return nil
}

func (t Ticket) IsMilestone() bool {
	return t.Kind == KindMilestone
}

func (t Ticket) IsIdea() bool {
	return t.Kind == KindIdea
}

func (t Ticket) Resolves(policy ResolutionPolicy) bool {
	return policy.Resolves(t.Kind, t.Status)
}
