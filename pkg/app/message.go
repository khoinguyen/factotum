package app

import (
	"context"
	"fmt"
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
	// Target is a canonical address ("actor:", "run:", "task:") or a bare
	// ticket id, which is sugar for task:<id>.
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

// resolveTarget turns a send target into the stored address and, for task
// targets, the originating task id (section 4 resolution).
func (s *MessageService) resolveTarget(ctx context.Context, project core.ProjectID, target string) (core.Address, *core.TicketID, error) {
	if !strings.ContainsRune(target, ':') {
		return s.resolveTask(ctx, project, core.TicketID(target))
	}
	kind, id, err := core.ParseAddress(target)
	if err != nil {
		return "", nil, err
	}
	switch kind {
	case core.AddressActor:
		return core.ActorAddress(core.ActorID(id)), nil, nil
	case core.AddressRun:
		return core.RunAddress(core.RunID(id)), nil, nil
	case core.AddressTask:
		return s.resolveTask(ctx, project, core.TicketID(id))
	default:
		return "", nil, fmt.Errorf("%w: unknown address kind %q", core.ErrInvalid, kind)
	}
}

func (s *MessageService) resolveTask(ctx context.Context, project core.ProjectID, ticketID core.TicketID) (core.Address, *core.TicketID, error) {
	ticket, err := s.backend.Tickets().Get(ctx, ticketID)
	if err != nil {
		return "", nil, fmt.Errorf("message task: %w", err)
	}
	live := true
	runs, err := s.backend.Runs().List(ctx, store.RunFilter{ProjectID: project, Task: &ticketID, Live: &live, Now: s.clock.Now(), Limit: 1})
	if err == nil && len(runs) > 0 {
		return core.RunAddress(runs[0].ID), &ticketID, nil
	}
	if ticket.AssigneeID != nil {
		if actor, err := s.backend.Actors().Get(ctx, *ticket.AssigneeID); err == nil && actor.Kind == core.ActorAgent {
			return core.ActorAddress(actor.ID), &ticketID, nil
		}
	}
	return core.TaskAddress(ticketID), &ticketID, nil
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
	if message.State == core.MessageRead {
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
