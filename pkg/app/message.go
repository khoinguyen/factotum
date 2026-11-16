package app

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// MessageService orchestrates the message store: it resolves targets, stamps
// timestamps and ids, and records the message event stream.
type MessageService struct {
	backend   store.Backend
	clock     Clock
	ids       IDGen
	bodyLimit int
}

func NewMessageService(backend store.Backend, clock Clock, ids IDGen) *MessageService {
	return &MessageService{backend: backend, clock: clock, ids: ids, bodyLimit: store.DefaultMessageBodyMax}
}

type SendMessageInput struct {
	ProjectID core.ProjectID
	From      *core.ActorID
	// Target is a canonical address ("actor:", "run:", "task:"), a bare loop
	// role (chief, builder-<task>, reviewer-<task>, qa-<task>), or a bare
	// ticket-shaped id, which is sugar for task:<id>.
	Target  string
	Body    string
	Links   []core.Link
	ReplyTo *core.MessageID
}

// Send validates and stores a queued message, resolving a task target to a
// live run or the assignee actor when possible.
func (s *MessageService) Send(ctx context.Context, in SendMessageInput) (*core.Message, error) {
	if _, err := s.backend.Projects().Get(ctx, in.ProjectID); err != nil {
		return nil, fmt.Errorf("message project: %w", err)
	}
	to, taskID, err := s.resolveTarget(ctx, in.ProjectID, in.Target)
	if err != nil {
		return nil, err
	}
	now := s.clock.Now()
	message := &core.Message{
		ID:        core.MessageID(s.ids.NewID("msg")),
		ProjectID: in.ProjectID,
		From:      in.From,
		To:        to,
		TaskID:    taskID,
		Body:      in.Body,
		Links:     in.Links,
		ReplyTo:   in.ReplyTo,
		State:     core.MessageQueued,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := message.Validate(s.bodyLimit); err != nil {
		return nil, err
	}
	if err := s.backend.Messages().Create(ctx, message); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: in.ProjectID,
		TicketID:  message.TaskID,
		Kind:      core.EventMessageSent,
		Summary:   fmt.Sprintf("sent message to %s", message.To),
	}); err != nil {
		return nil, err
	}
	return message, nil
}

// LoopRole reports whether ref names a chief-loop role and returns it as the
// actor id that addresses the role's mailbox. The roles are the task-scoped
// builder-<task>, reviewer-<task>, and qa-<task>, plus the singleton chief. A
// role is an ordinary actor id, so a peer addresses it as actor:<role> and a
// receiver that registered under it claims its messages; the sugar just lets a
// sender (and ft msg send) name the peer without a cmux surface or a colon.
func LoopRole(ref string) (core.ActorID, bool) {
	if ref == "chief" {
		return core.ActorID(ref), true
	}
	for _, prefix := range []string{"builder-", "reviewer-", "qa-"} {
		if strings.HasPrefix(ref, prefix) && len(ref) > len(prefix) {
			return core.ActorID(ref), true
		}
	}
	return "", false
}

// loopDefaultRoles are the role names a chief-loop wrapper arms itself with
// (loop-agent.sh <actor>). They are LoopRole's four plus grooming-agent and
// architecture-reviewer, which the loop only ever sets as a configured default,
// never as a bare send target.
var loopDefaultRoles = []string{"builder", "reviewer", "qa", "grooming-agent", "architecture-reviewer"}

// roleIDPrefixes are the id shapes this codebase mints that a role name may be
// scoped by: a task (t-), an artifact (art-), and a grooming session (groom-).
var roleIDPrefixes = []string{"t-", "art-", "groom-"}

// LoopDefaultRole reports whether ref is a role-shaped identity a loop wrapper
// exports as a configured default actor, and returns it as the actor id that
// attributes the call. It is the default-path counterpart of LoopRole: the same
// loop vocabulary widened to the grooming-agent and architecture-reviewer roles,
// which are only ever configured, and narrowed so the role must carry a
// well-formed ft id. The check is the id's shape, not its existence: the
// requirement is what keeps a token that merely starts with a role name
// (builder-gpt-4, qa-bot, a truncated builder-t) from being trusted as a role and
// attributed to a mailbox nobody claims.
func LoopDefaultRole(ref string) (core.ActorID, bool) {
	if ref == "chief" {
		return core.ActorID(ref), true
	}
	for _, role := range loopDefaultRoles {
		id, ok := strings.CutPrefix(ref, role+"-")
		if ok && roleID(id) {
			return core.ActorID(ref), true
		}
	}
	return "", false
}

// roleID reports whether token has the shape of the ft id a role is scoped by:
// an id prefix plus a non-empty run of the lowercase alphanumerics ids are
// minted from.
func roleID(token string) bool {
	for _, prefix := range roleIDPrefixes {
		rest, ok := strings.CutPrefix(token, prefix)
		if !ok {
			continue
		}
		if rest == "" {
			return false
		}
		for _, r := range rest {
			if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
				return false
			}
		}
		return true
	}
	return false
}

// ticketSugar reports whether ref is a bare ticket reference (the "t-..." shape
// every generated task/idea/bug id has), the only bare token besides a loop role
// that `ft msg send` accepts. Anything else bare is a mistake - a garbled role
// or an actor name missing its actor: prefix - and must be rejected loudly
// rather than coerced into ticket sugar and misreported as a missing task.
func ticketSugar(ref string) bool {
	return strings.HasPrefix(ref, "t-") && len(ref) > len("t-")
}

// resolveTarget turns a send target into the stored address and, for task
// targets, the originating task id (section 4 resolution). A bare loop role is
// sugar for its actor address; a bare ticket-shaped token is ticket sugar; any
// other bare token is rejected as unresolvable.
func (s *MessageService) resolveTarget(ctx context.Context, project core.ProjectID, target string) (core.Address, *core.TicketID, error) {
	if !strings.ContainsRune(target, ':') {
		if actor, ok := LoopRole(target); ok {
			return core.ActorAddress(actor), nil, nil
		}
		if !ticketSugar(target) {
			return "", nil, fmt.Errorf("%w: unresolvable target %q: name a role (chief, builder-<task>, reviewer-<task>, qa-<task>), a canonical address (actor:<id>, run:<id>, task:<id>), or a ticket id", core.ErrInvalid, target)
		}
		return s.resolveTask(ctx, project, core.TicketID(target))
	}
	kind, id, err := core.ParseAddress(target)
	if err != nil {
		return "", nil, err
	}
	switch kind {
	case core.AddressActor:
		// The target takes the same reference parsing as an actor flag, so a
		// doubled (actor:actor:<id>) or nested address is rejected here instead
		// of being stored as a mailbox no actor can claim.
		ref, err := core.ParseActorRef(target)
		if err != nil {
			return "", nil, err
		}
		return core.ActorAddress(ref), nil, nil
	case core.AddressRun:
		return core.RunAddress(core.RunID(id)), nil, nil
	case core.AddressTask:
		return s.resolveTask(ctx, project, core.TicketID(id))
	default:
		return "", nil, fmt.Errorf("%w: unknown address kind %q", core.ErrInvalid, kind)
	}
}

// resolveTask implements ticket sugar (design section 4). It never guesses
// between peers: exactly one live run is addressed directly, several runs of one
// actor settle on that actor's mailbox, and runs of different actors refuse,
// naming the candidate actor addresses so the sender re-addresses the peer it
// meant instead of delivering to whichever run id sorts first.
func (s *MessageService) resolveTask(ctx context.Context, project core.ProjectID, ticketID core.TicketID) (core.Address, *core.TicketID, error) {
	ticket, err := s.backend.Tickets().Get(ctx, ticketID)
	if err != nil {
		return "", nil, fmt.Errorf("message task: %w", err)
	}
	live := true
	runs, err := s.backend.Runs().List(ctx, store.RunFilter{ProjectID: project, Task: &ticketID, Live: &live, Now: s.clock.Now()})
	if err != nil {
		return "", nil, fmt.Errorf("message task runs: %w", err)
	}
	actors := runActors(runs)
	switch len(actors) {
	case 1:
		if len(runs) == 1 {
			return core.RunAddress(runs[0].ID), &ticketID, nil
		}
		// Several sessions of one actor: the mailbox is the deterministic target
		// and every one of those runs can claim it.
		return core.ActorAddress(actors[0]), &ticketID, nil
	case 0:
		if ticket.AssigneeID != nil {
			if actor, err := s.backend.Actors().Get(ctx, *ticket.AssigneeID); err == nil && actor.Kind == core.ActorAgent {
				return core.ActorAddress(actor.ID), &ticketID, nil
			}
		}
		return core.TaskAddress(ticketID), &ticketID, nil
	default:
		sort.Slice(actors, func(i, j int) bool { return actors[i] < actors[j] })
		candidates := make([]string, len(actors))
		for i, actor := range actors {
			candidates[i] = string(core.ActorAddress(actor))
		}
		return "", nil, fmt.Errorf("%w: task %s has live runs for %d actors (%s): address the peer you mean, e.g. ft msg send %s", core.ErrInvalid, ticketID, len(actors), strings.Join(candidates, ", "), candidates[0])
	}
}

// runActors returns the distinct actors behind runs, in run order.
func runActors(runs []*core.Run) []core.ActorID {
	actors := make([]core.ActorID, 0, len(runs))
	seen := make(map[core.ActorID]bool, len(runs))
	for _, run := range runs {
		if run == nil || seen[run.ActorID] {
			continue
		}
		seen[run.ActorID] = true
		actors = append(actors, run.ActorID)
	}
	return actors
}

// InboxQuery filters an inbox listing.
type InboxQuery struct {
	ProjectID core.ProjectID
	States    []core.MessageState
	To        *core.Address
	Actor     *core.ActorID
	Run       *core.RunID
	Limit     int
}

func (s *MessageService) Inbox(ctx context.Context, q InboxQuery) ([]*core.Message, error) {
	filter := store.MessageFilter{
		ProjectID: q.ProjectID,
		To:        q.To,
		Actor:     q.Actor,
		Run:       q.Run,
		States:    q.States,
		Limit:     q.Limit,
	}
	if filter.Limit == 0 {
		filter.Limit = 50
	}
	return s.backend.Messages().List(ctx, filter)
}

func (s *MessageService) Get(ctx context.Context, id core.MessageID) (*core.Message, error) {
	return s.backend.Messages().Get(ctx, id)
}

// Read marks a message read (terminal) and records the event. It is idempotent:
// an already-read message is returned unchanged.
func (s *MessageService) Read(ctx context.Context, id core.MessageID) (*core.Message, error) {
	message, err := s.backend.Messages().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// read and failed are terminal; reading never resurrects a failed message.
	if message.State == core.MessageRead || message.State == core.MessageFailed {
		return message, nil
	}
	now := s.clock.Now()
	switch {
	case message.State == core.MessageDelivered && message.RunID != nil:
		if err := s.backend.Messages().Ack(ctx, store.AckRequest{ID: id, RunID: *message.RunID, State: core.MessageRead}); err != nil {
			return nil, err
		}
	default:
		message.State = core.MessageRead
		message.ReadAt = &now
		message.UpdatedAt = now
		if err := s.backend.Messages().Update(ctx, message); err != nil {
			return nil, err
		}
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: message.ProjectID,
		TicketID:  message.TaskID,
		Kind:      core.EventMessageRead,
		Summary:   fmt.Sprintf("read message %s", message.ID),
	}); err != nil {
		return nil, err
	}
	return s.backend.Messages().Get(ctx, id)
}
