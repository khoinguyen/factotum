package core

import (
	"fmt"
	"strings"
	"time"
)

type MessageID string
type RunID string

func (id MessageID) String() string { return string(id) }
func (id RunID) String() string     { return string(id) }

type MessageState string

const (
	// MessageQueued is at rest: accepted and not yet claimed.
	MessageQueued MessageState = "queued"
	// MessageDelivered is leased to exactly one run, awaiting an ack.
	MessageDelivered MessageState = "delivered"
	// MessageRead is the success terminal ack; redelivery stops.
	MessageRead MessageState = "read"
	// MessageFailed is a terminal failure after retries, or an explicit nack.
	MessageFailed MessageState = "failed"
)

func (s MessageState) Valid() bool {
	switch s {
	case MessageQueued, MessageDelivered, MessageRead, MessageFailed:
		return true
	default:
		return false
	}
}

// Terminal reports whether a state is final (no further delivery).
func (s MessageState) Terminal() bool {
	return s == MessageRead || s == MessageFailed
}

type AddressKind string

const (
	// AddressActor targets any run registered for an actor (actor mailbox).
	AddressActor AddressKind = "actor"
	// AddressRun targets exactly one running session.
	AddressRun AddressKind = "run"
	// AddressTask is sugar resolved to run/actor at enqueue time.
	AddressTask AddressKind = "task"
)

func (k AddressKind) Valid() bool {
	return k == AddressActor || k == AddressRun || k == AddressTask
}

// Address is a canonical "kind:id" message target.
type Address string

func (a Address) String() string { return string(a) }

// ParseAddress splits a canonical address into its kind and id.
func ParseAddress(value string) (AddressKind, string, error) {
	kind, id, ok := strings.Cut(value, ":")
	if !ok || strings.TrimSpace(id) == "" {
		return "", "", fmt.Errorf("%w: address %q must be kind:id", ErrInvalid, value)
	}
	addressKind := AddressKind(kind)
	if !addressKind.Valid() {
		return "", "", fmt.Errorf("%w: unknown address kind %q", ErrInvalid, kind)
	}
	return addressKind, id, nil
}

// Kind returns the address kind, or "" when the address is malformed.
func (a Address) Kind() AddressKind {
	kind, _, err := ParseAddress(string(a))
	if err != nil {
		return ""
	}
	return kind
}

func ActorAddress(id ActorID) Address { return Address("actor:" + string(id)) }
func RunAddress(id RunID) Address     { return Address("run:" + string(id)) }
func TaskAddress(id TicketID) Address { return Address("task:" + string(id)) }

// ValidateAddress rejects a malformed address or an unknown kind.
func ValidateAddress(a Address) error {
	_, _, err := ParseAddress(string(a))
	return err
}

// Message is a durable agent-to-agent message. State transitions and delivery
// bookkeeping (RunID, LeaseUntil, Attempts, Error) are set by Claim/Ack, never by
// the sender.
type Message struct {
	ID        MessageID
	ProjectID ProjectID
	From      *ActorID // sender; nil means a system/automated message
	To        Address  // canonical target (see AddressKind)
	// TaskID is the originating task this message concerns, when any. It is set
	// when the target was task:<t> even though To may be rewritten to run:/actor:,
	// so the task link is never lost.
	TaskID  *TicketID
	Body    string
	Links   []Link     // large content lives in an artifact, not inline
	ReplyTo *MessageID // threading, optional

	State MessageState
	// Delivery bookkeeping, set by Claim/Ack.
	RunID      *RunID
	LeaseUntil *time.Time
	Attempts   int
	Error      string

	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeliveredAt *time.Time
	ReadAt      *time.Time
}

// Validate rejects a malformed message. bodyLimit, when > 0, caps the body
// length; the limit is supplied at send time so core stays pure.
func (m Message) Validate(bodyLimit int) error {
	if m.ID == "" {
		return fmt.Errorf("%w: message id is required", ErrInvalid)
	}
	if m.ProjectID == "" {
		return fmt.Errorf("%w: message project id is required", ErrInvalid)
	}
	kind, id, err := ParseAddress(string(m.To))
	if err != nil {
		return fmt.Errorf("message %s: %w", m.ID, err)
	}
	if !m.State.Valid() {
		return fmt.Errorf("%w: unknown message state %q", ErrInvalid, m.State)
	}
	if strings.TrimSpace(m.Body) == "" {
		return fmt.Errorf("%w: message body is required", ErrInvalid)
	}
	if bodyLimit > 0 && len(m.Body) > bodyLimit {
		return fmt.Errorf("%w: message body exceeds %d bytes", ErrInvalid, bodyLimit)
	}
	for _, link := range m.Links {
		if err := link.Validate(); err != nil {
			return fmt.Errorf("message link: %w", err)
		}
	}
	if kind == AddressTask {
		if m.TaskID == nil || *m.TaskID != TicketID(id) {
			return fmt.Errorf("%w: message %s To %s disagrees with task id", ErrInvalid, m.ID, m.To)
		}
	}
	return nil
}

// Run is a durable identity for a live receiver session. It is created by
// register and stays live while its lease is renewed.
type Run struct {
	ID        RunID
	ProjectID ProjectID
	ActorID   ActorID
	TaskID    *TicketID // the task this session is working, when any
	Harness   string    // "opencode", "pi", ... (free-form, for display)
	Host      string    // machine identity, for cross-host diagnostics
	PID       int
	// CanInject means a receiver adapter is running that can accept an
	// out-of-band injection. Empty means "poll only".
	CanInject  bool
	CreatedAt  time.Time
	SeenAt     time.Time // last heartbeat
	LeaseUntil time.Time // SeenAt + TTL; stale once past
}

func (r Run) Validate() error {
	if r.ID == "" {
		return fmt.Errorf("%w: run id is required", ErrInvalid)
	}
	if r.ProjectID == "" {
		return fmt.Errorf("%w: run project id is required", ErrInvalid)
	}
	if r.ActorID == "" {
		return fmt.Errorf("%w: run actor id is required", ErrInvalid)
	}
	return nil
}

// Live reports whether the run's lease is still valid at now.
func (r Run) Live(now time.Time) bool {
	return r.LeaseUntil.After(now)
}
