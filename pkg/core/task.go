package core

import (
	"fmt"
	"strings"
	"time"
)

type TaskKind string

const (
	KindTask      TaskKind = "task"
	KindMilestone TaskKind = "milestone"
)

func (k TaskKind) Valid() bool {
	return k == KindTask || k == KindMilestone
}

type TaskStatus string

const (
	StatusTodo           TaskStatus = "todo"
	StatusInProgress     TaskStatus = "in_progress"
	StatusBlocked        TaskStatus = "blocked"
	StatusReadyForReview TaskStatus = "ready_for_review"
	StatusDone           TaskStatus = "done"
	StatusCancelled      TaskStatus = "cancelled"
)

func (s TaskStatus) Valid() bool {
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

type Task struct {
	ID          TaskID
	ProjectID   ProjectID
	Repo        string
	Kind        TaskKind
	Title       string
	Description string
	Status      TaskStatus
	AssigneeID  *ActorID
	WaitingOn   []ActorID
	Labels      []string
	Priority    int
	Deps        []TaskID
	Notes       []Note
	Milestone   *MilestoneMeta
	NotBefore   *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReadyAt reports whether the task's not_before constraint has elapsed at now.
// A task with no constraint is always ready.
func (t Task) ReadyAt(now time.Time) bool {
	return t.NotBefore == nil || !t.NotBefore.After(now)
}

func (t Task) Validate() error {
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

	seen := make(map[TaskID]struct{}, len(t.Deps))
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

	return nil
}

func (t Task) IsMilestone() bool {
	return t.Kind == KindMilestone
}

func (t Task) Resolves(policy ResolutionPolicy) bool {
	return policy.Resolves(t.Kind, t.Status)
}
