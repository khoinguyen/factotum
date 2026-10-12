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
	EventTaskRunStarted       EventKind = "task.run_started"
	EventTaskRunFinished      EventKind = "task.run_finished"

	EventActorCreated EventKind = "actor.created"
	EventActorUpdated EventKind = "actor.updated"
	EventActorDeleted EventKind = "actor.deleted"

	EventArtifactCreated EventKind = "artifact.created"
	EventArtifactUpdated EventKind = "artifact.updated"
	EventArtifactDeleted EventKind = "artifact.deleted"

	EventMessageSent    EventKind = "message.sent"
	EventMessageClaimed EventKind = "message.claimed"
	EventMessageRead    EventKind = "message.read"
	EventMessageFailed  EventKind = "message.failed"

	// EventSessionStarted and EventSessionRetired bracket a chief session. A
	// retired event names its successor in Data["successor"], so a session's
	// stop and the reason (reincarnation) are derivable from the log.
	EventSessionStarted EventKind = "session.started"
	EventSessionRetired EventKind = "session.retired"

	// Pipeline events trace one factory pipeline (docs/up/design.md §7). They are
	// appended with TicketID set to the origin capture, so a pipeline also
	// surfaces on the capture's page and through the existing broker.
	EventPipelineQueued        EventKind = "pipeline.queued"
	EventPipelineStageStarted  EventKind = "pipeline.stage_started"
	EventPipelineStageFinished EventKind = "pipeline.stage_finished"
	EventPipelineGateOpened    EventKind = "pipeline.gate_opened"
	EventPipelineGateCleared   EventKind = "pipeline.gate_cleared"
	EventPipelineDone          EventKind = "pipeline.done"
	EventPipelineFailed        EventKind = "pipeline.failed"
)

func (k EventKind) Valid() bool {
	switch k {
	case EventProjectCreated, EventProjectUpdated, EventProjectDeleted,
		EventProjectRepoAdded, EventProjectRepoUpdated, EventProjectRepoRemoved,
		EventTaskCreated, EventTaskUpdated, EventTaskDeleted, EventTaskStatusChanged,
		EventTaskAssigned, EventTaskWaitingOnChanged, EventTaskDepAdded, EventTaskDepRemoved,
		EventTaskNoteAdded, EventTaskSnoozed, EventTaskUnsnoozed,
		EventTaskRunStarted, EventTaskRunFinished,
		EventActorCreated, EventActorUpdated, EventActorDeleted,
		EventArtifactCreated, EventArtifactUpdated, EventArtifactDeleted,
		EventMessageSent, EventMessageClaimed, EventMessageRead, EventMessageFailed,
		EventSessionStarted, EventSessionRetired,
		EventPipelineQueued, EventPipelineStageStarted, EventPipelineStageFinished,
		EventPipelineGateOpened, EventPipelineGateCleared, EventPipelineDone, EventPipelineFailed:
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
	// The JSON key stays "TaskID": backends persist Event verbatim, so changing
	// it would silently drop the link in documents written before the
	// Task->Ticket rename.
	TicketID *TicketID `json:"TaskID"`
	Kind     EventKind
	By       *ActorID
	// Session is the chief session the acting actor belongs to, stamped at
	// append time from the caller context (FACTOTUM_SESSION). It lets events be
	// grouped by session without parsing every actor id.
	Session   string
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
