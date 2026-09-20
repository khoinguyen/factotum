package core

import (
	"fmt"
	"time"
)

type EventKind string

const (
	EventProjectCreated     EventKind = "project.created"
	EventProjectUpdated     EventKind = "project.updated"
	EventProjectDeleted     EventKind = "project.deleted"
	EventProjectRepoAdded   EventKind = "project.repo_added"
	EventProjectRepoUpdated EventKind = "project.repo_updated"
	EventProjectRepoRemoved EventKind = "project.repo_removed"

	EventTaskCreated          EventKind = "task.created"
	EventTaskUpdated          EventKind = "task.updated"
	EventTaskDeleted          EventKind = "task.deleted"
	EventTaskStatusChanged    EventKind = "task.status_changed"
	EventTaskAssigned         EventKind = "task.assigned"
	EventTaskWaitingOnChanged EventKind = "task.waiting_on_changed"
	EventTaskDepAdded         EventKind = "task.dep_added"
	EventTaskDepRemoved       EventKind = "task.dep_removed"
	EventTaskNoteAdded        EventKind = "task.note_added"
	EventTaskSnoozed          EventKind = "task.snoozed"
	EventTaskUnsnoozed        EventKind = "task.unsnoozed"

	EventActorCreated EventKind = "actor.created"
	EventActorUpdated EventKind = "actor.updated"
	EventActorDeleted EventKind = "actor.deleted"

	EventArtifactCreated EventKind = "artifact.created"
	EventArtifactUpdated EventKind = "artifact.updated"
	EventArtifactDeleted EventKind = "artifact.deleted"
)

func (k EventKind) Valid() bool {
	switch k {
	case EventProjectCreated, EventProjectUpdated, EventProjectDeleted,
		EventProjectRepoAdded, EventProjectRepoUpdated, EventProjectRepoRemoved,
		EventTaskCreated, EventTaskUpdated, EventTaskDeleted, EventTaskStatusChanged,
		EventTaskAssigned, EventTaskWaitingOnChanged, EventTaskDepAdded, EventTaskDepRemoved,
		EventTaskNoteAdded, EventTaskSnoozed, EventTaskUnsnoozed,
		EventActorCreated, EventActorUpdated, EventActorDeleted,
		EventArtifactCreated, EventArtifactUpdated, EventArtifactDeleted:
		return true
	default:
		return false
	}
}

type Tally struct {
	Scope      int
	Done       int
	ReadyAgent int
	ReadyHuman int
	Blocked    int
	Cycles     int
	Waves      int
}

type Event struct {
	ID        EventID
	ProjectID ProjectID
	TaskID    *TaskID
	Kind      EventKind
	By        *ActorID
	Summary   string
	Data      map[string]any
	Tally     *Tally
	CreatedAt time.Time
}

func (e Event) Validate() error {
	if e.ID == "" {
		return fmt.Errorf("%w: event id is required", ErrInvalid)
	}
	if !e.Kind.Valid() {
		return fmt.Errorf("%w: unknown event kind %q", ErrInvalid, e.Kind)
	}
	return nil
}
