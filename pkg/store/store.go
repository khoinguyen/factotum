// Package store defines the storage ports. Adapters implement Backend; the
// conformance suite in pkg/store/conformance is the shared contract.
package store

import (
	"context"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

type Config struct {
	Backend string
	Options map[string]string
	// Noticef, when set, receives human-readable notices (such as a schema
	// migration and its backup path) that must not pollute structured stdout.
	Noticef func(format string, args ...any)
}

func (c Config) Option(key string) string {
	if c.Options == nil {
		return ""
	}
	return c.Options[key]
}

type ProjectRepo interface {
	Create(ctx context.Context, project *core.Project) error
	Get(ctx context.Context, id core.ProjectID) (*core.Project, error)
	List(ctx context.Context) ([]*core.Project, error)
	Update(ctx context.Context, project *core.Project) error
	Delete(ctx context.Context, id core.ProjectID) error
}

type TicketFilter struct {
	ProjectID core.ProjectID
	Repo      *string
	Statuses  []core.TicketStatus
	Kind      *core.TicketKind
	Labels    []string
	// Groomed, when set, keeps only tasks whose Groomed flag matches. It is a
	// pointer so an explicit false (ungroomed) is distinct from "unset".
	Groomed *bool
	// DependsOn, when set, keeps only tasks that declare the given task id in
	// their Deps, i.e. the direct dependents of that id. It is a reverse-edge
	// lookup, so a backend should serve it without scanning every task.
	DependsOn *core.TicketID
}

// MatchGroomed reports whether task's groomed flag satisfies the filter. A nil
// filter matches every task.
func MatchGroomed(task core.Ticket, want *bool) bool {
	return want == nil || task.Groomed == *want
}

// MatchLabels reports whether task carries every label in labels (AND).
func MatchLabels(task core.Ticket, labels []string) bool {
	for _, want := range labels {
		found := false
		for _, have := range task.Labels {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type TicketRepo interface {
	Create(ctx context.Context, task *core.Ticket) error
	Get(ctx context.Context, id core.TicketID) (*core.Ticket, error)
	List(ctx context.Context, filter TicketFilter) ([]*core.Ticket, error)
	Update(ctx context.Context, task *core.Ticket) error
	// UpdateExpected writes the task only if its stored UpdatedAt still equals
	// expected, returning ErrConflict otherwise (compare-and-swap). It lets a
	// caller that read a document reject a stale write atomically.
	UpdateExpected(ctx context.Context, task *core.Ticket, expected time.Time) error
	Delete(ctx context.Context, id core.TicketID) error
	// Search returns tasks in filter scope whose title, description, or notes
	// match the query, ranked by relevance. An empty query matches everything in
	// scope.
	Search(ctx context.Context, filter TicketFilter, query string) ([]TicketSearchHit, error)
}

type ActorRepo interface {
	Create(ctx context.Context, actor *core.Actor) error
	Get(ctx context.Context, id core.ActorID) (*core.Actor, error)
	FindByName(ctx context.Context, name string) (*core.Actor, error)
	List(ctx context.Context) ([]*core.Actor, error)
	Update(ctx context.Context, actor *core.Actor) error
	Delete(ctx context.Context, id core.ActorID) error
}

type ArtifactFilter struct {
	ProjectID core.ProjectID
	TicketID  *core.TicketID
	Kind      *core.ArtifactKind
}

type ArtifactRepo interface {
	Create(ctx context.Context, artifact *core.Artifact) error
	Get(ctx context.Context, id core.ArtifactID) (*core.Artifact, error)
	List(ctx context.Context, filter ArtifactFilter) ([]*core.Artifact, error)
	// Search returns artifacts in filter scope whose title or body matches the
	// query, ranked by relevance. An empty query matches everything in scope.
	Search(ctx context.Context, filter ArtifactFilter, query string) ([]SearchHit, error)
	Update(ctx context.Context, artifact *core.Artifact) error
	Delete(ctx context.Context, id core.ArtifactID) error
}

type EventFilter struct {
	ProjectID core.ProjectID
	TicketID  *core.TicketID
	Kinds     []core.EventKind
	Since     *time.Time
	Limit     int
}

type EventRepo interface {
	Append(ctx context.Context, event *core.Event) error
	List(ctx context.Context, filter EventFilter) ([]*core.Event, error)
}

// DefaultMessageLease is how long a claim owns a message before it may be
// redelivered. MaxMessageAttempts parks a message failed once a claim cycle has
// failed this many times.
const (
	DefaultMessageLease   = 30 * time.Second
	DefaultMessageWait    = 25 * time.Second
	MaxMessageAttempts    = 5
	DefaultMessageBodyMax = 16 * 1024
)

type MessageFilter struct {
	ProjectID core.ProjectID
	To        *core.Address  // exact stored To match
	Actor     *core.ActorID  // claimable by this actor: To == actor:<id>
	Run       *core.RunID    // claimable by this run: To == run:<id>
	Task      *core.TicketID // Message.TaskID == <id> (the originating task)
	States    []core.MessageState
	Since     *time.Time
	Limit     int
}

// ClaimRecipient is the union of addresses a run can be reached at, resolved
// from the run by the caller.
type ClaimRecipient struct {
	ActorID *core.ActorID
	TaskID  *core.TicketID
}

// ClaimRequest selects one message for a run. Recipient is the union of the
// run's own address, its actor's address, and its task address. ProjectID,
// when set, scopes the claim so a run never claims another project's mailbox.
type ClaimRequest struct {
	ProjectID core.ProjectID
	RunID     core.RunID
	Recipient ClaimRecipient
	Lease     time.Duration // delivered lease; 0 uses DefaultMessageLease
	Wait      time.Duration // long-poll window; 0 returns immediately
}

// AckRequest finalizes one delivered message owned by a run.
type AckRequest struct {
	ID    core.MessageID
	RunID core.RunID
	State core.MessageState // read | failed
	Error string            // reason when State == failed
}

type MessageRepo interface {
	Create(ctx context.Context, message *core.Message) error
	Get(ctx context.Context, id core.MessageID) (*core.Message, error)
	List(ctx context.Context, filter MessageFilter) ([]*core.Message, error)
	Update(ctx context.Context, message *core.Message) error

	// Claim atomically selects the oldest queued message claimable by
	// (RunID, Recipient) and transitions it to delivered, setting RunID,
	// LeaseUntil, DeliveredAt, and Attempts. It returns ErrNotFound when
	// nothing is claimable. Two concurrent claims never return the same message.
	Claim(ctx context.Context, req ClaimRequest) (*core.Message, error)

	// Ack finalizes a delivered message owned by RunID: read is terminal
	// success and idempotent; failed records Error and is terminal.
	Ack(ctx context.Context, req AckRequest) error

	// Nack requeues a delivered message (attempts already counted) or, once
	// attempts reach MaxMessageAttempts, parks it as failed.
	Nack(ctx context.Context, id core.MessageID, runID core.RunID, reason string) error

	// RequeueExpired reclaims every delivered message whose lease lapsed: back
	// to queued, or to failed once attempts >= maxAttempts. It returns how many
	// moved. It is the crash-recovery sweep.
	RequeueExpired(ctx context.Context, now time.Time, maxAttempts int) (int, error)

	// Prune removes terminal messages in states older than before and returns
	// how many were removed.
	Prune(ctx context.Context, before time.Time, states []core.MessageState) (int, error)
}

type RunFilter struct {
	ProjectID core.ProjectID
	ActorID   *core.ActorID
	Task      *core.TicketID
	// Live, when set, keeps only runs whose lease is valid at Now (or the
	// backend clock when Now is zero).
	Live  *bool
	Now   time.Time
	Limit int
}

type RunRepo interface {
	// Register upserts a run idempotently on (ProjectID, ActorID, Host, PID):
	// re-registering the same session returns the stored run with a stable ID.
	Register(ctx context.Context, run *core.Run) (*core.Run, error)
	// Heartbeat renews a run's lease from now.
	Heartbeat(ctx context.Context, id core.RunID, now time.Time, lease time.Duration) error
	List(ctx context.Context, filter RunFilter) ([]*core.Run, error)
	Delete(ctx context.Context, id core.RunID) error
}

// PipelineFilter selects pipelines. CaptureID, States, and Gates are optional
// (empty means "any"); ProjectID scopes the list.
type PipelineFilter struct {
	ProjectID core.ProjectID
	CaptureID *core.TicketID
	States    []core.PipelineState
	Gates     []core.GateKind
	Limit     int
}

// PipelineRepo is the storage port for factory pipelines. It mirrors
// MessageRepo: Create/Get/List/Update plus an atomic Claim. See
// docs/up/design.md §7.
type PipelineRepo interface {
	// Create stores a new pipeline. It returns ErrAlreadyExists when the id is
	// taken, or when another pipeline already drives the same (project,
	// capture): one pipeline per capture, so a repeated capture cannot start a
	// second one.
	Create(ctx context.Context, pipeline *core.Pipeline) error
	Get(ctx context.Context, id core.PipelineID) (*core.Pipeline, error)
	List(ctx context.Context, filter PipelineFilter) ([]*core.Pipeline, error)
	// Update writes mutable pipeline fields. (ProjectID, CaptureID) is the
	// pipeline's identity and is immutable: changing either returns
	// ErrConflict, as does updating a terminal pipeline.
	Update(ctx context.Context, pipeline *core.Pipeline) error

	// Claim atomically selects the oldest queued pipeline for the project and
	// moves it to grooming, marking it in flight so the single controller
	// worker owns it. It returns ErrNotFound when nothing is queued. Two
	// concurrent claims never return the same pipeline.
	Claim(ctx context.Context, req PipelineClaimRequest) (*core.Pipeline, error)
}

// PipelineClaimRequest selects the next queued pipeline to claim. ProjectID
// scopes the claim so a controller never takes another project's work.
type PipelineClaimRequest struct {
	ProjectID core.ProjectID
}

type Backend interface {
	Projects() ProjectRepo
	Tickets() TicketRepo
	Actors() ActorRepo
	Artifacts() ArtifactRepo
	Events() EventRepo
	Messages() MessageRepo
	Runs() RunRepo
	Pipelines() PipelineRepo
	Close() error
}

// Claimable reports whether message can be claimed by req's run: it must be
// queued and addressed to the run itself, its actor, or its task.
func Claimable(message *core.Message, req ClaimRequest) bool {
	if message.State != core.MessageQueued {
		return false
	}
	if req.ProjectID != "" && message.ProjectID != req.ProjectID {
		return false
	}
	if message.To == core.RunAddress(req.RunID) {
		return true
	}
	if req.Recipient.ActorID != nil && message.To == core.ActorAddress(*req.Recipient.ActorID) {
		return true
	}
	if req.Recipient.TaskID != nil && message.To == core.TaskAddress(*req.Recipient.TaskID) {
		return true
	}
	return false
}

// MatchesMessageFilter reports whether message satisfies filter. Run and Actor
// match the stored To; Task matches Message.TaskID regardless of To.
func MatchesMessageFilter(message *core.Message, filter MessageFilter) bool {
	if filter.ProjectID != "" && message.ProjectID != filter.ProjectID {
		return false
	}
	if filter.To != nil && message.To != *filter.To {
		return false
	}
	if filter.Actor != nil && message.To != core.ActorAddress(*filter.Actor) {
		return false
	}
	if filter.Run != nil && message.To != core.RunAddress(*filter.Run) {
		return false
	}
	if filter.Task != nil && (message.TaskID == nil || *message.TaskID != *filter.Task) {
		return false
	}
	if len(filter.States) > 0 && !containsMessageState(filter.States, message.State) {
		return false
	}
	if filter.Since != nil && message.CreatedAt.Before(*filter.Since) {
		return false
	}
	return true
}

func containsMessageState(states []core.MessageState, state core.MessageState) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

// CompareMessages orders messages oldest first by CreatedAt, then id, so
// delivery is deterministic.
func CompareMessages(a, b *core.Message) int {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		if a.CreatedAt.Before(b.CreatedAt) {
			return -1
		}
		return 1
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

// MatchesPipelineFilter reports whether pipeline satisfies filter.
func MatchesPipelineFilter(pipeline *core.Pipeline, filter PipelineFilter) bool {
	if filter.ProjectID != "" && pipeline.ProjectID != filter.ProjectID {
		return false
	}
	if filter.CaptureID != nil && pipeline.CaptureID != *filter.CaptureID {
		return false
	}
	if len(filter.States) > 0 && !containsPipelineState(filter.States, pipeline.State) {
		return false
	}
	if len(filter.Gates) > 0 && !containsGateKind(filter.Gates, pipeline.Gate) {
		return false
	}
	return true
}

func containsPipelineState(states []core.PipelineState, state core.PipelineState) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

func containsGateKind(gates []core.GateKind, gate core.GateKind) bool {
	for _, candidate := range gates {
		if candidate == gate {
			return true
		}
	}
	return false
}

// ComparePipelines orders pipelines oldest first by CreatedAt, then id, so
// claiming and listing are deterministic.
func ComparePipelines(a, b *core.Pipeline) int {
	if !a.CreatedAt.Equal(b.CreatedAt) {
		if a.CreatedAt.Before(b.CreatedAt) {
			return -1
		}
		return 1
	}
	return strings.Compare(string(a.ID), string(b.ID))
}

// MatchRunFilter reports whether run satisfies filter. now resolves the Live
// predicate (filter.Now when set, else the caller's clock).
func MatchRunFilter(run *core.Run, filter RunFilter, now time.Time) bool {
	if filter.ProjectID != "" && run.ProjectID != filter.ProjectID {
		return false
	}
	if filter.ActorID != nil && run.ActorID != *filter.ActorID {
		return false
	}
	if filter.Task != nil && (run.TaskID == nil || *run.TaskID != *filter.Task) {
		return false
	}
	if filter.Live != nil && run.Live(now) != *filter.Live {
		return false
	}
	return true
}

// claimPollInterval is how often AwaitClaim retries within a long-poll window.
const claimPollInterval = 50 * time.Millisecond

// AwaitClaim long-polls claim for up to wait, returning ErrNotFound once the
// window elapses with nothing claimable. A claim attempt returning (nil, nil)
// means nothing was available right now.
func AwaitClaim(ctx context.Context, wait time.Duration, claim func() (*core.Message, error)) (*core.Message, error) {
	if wait <= 0 {
		message, err := claim()
		if err != nil {
			return nil, err
		}
		if message == nil {
			return nil, core.ErrNotFound
		}
		return message, nil
	}
	deadline := time.Now().Add(wait)
	for {
		message, err := claim()
		if err != nil {
			return nil, err
		}
		if message != nil {
			return message, nil
		}
		if !time.Now().Before(deadline) {
			return nil, core.ErrNotFound
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(claimPollInterval):
		}
	}
}

type Factory func(ctx context.Context, cfg Config) (Backend, error)

// TxBackend is an optional capability: a backend that can run a function inside
// a transaction over the same repositories.
type TxBackend interface {
	WithTx(ctx context.Context, fn func(Backend) error) error
}

// Migrator is an optional capability: a backend with schema migrations.
type Migrator interface {
	Migrate(ctx context.Context) error
}
