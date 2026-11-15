package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// SessionService records the start and retirement of a chief session. The
// events it appends carry the session id and, from the caller context, the
// acting actor, so a session's lifecycle and the work it drove are one timeline.
type SessionService struct {
	backend store.Backend
	clock   Clock
	ids     IDGen
}

func NewSessionService(backend store.Backend, clock Clock, ids IDGen) *SessionService {
	return &SessionService{backend: backend, clock: clock, ids: ids}
}

// NewID mints a fresh, short session id. A reincarnation always gets a new one,
// so no two chief sessions share an id.
func (s *SessionService) NewID() string {
	return newSessionID(s.ids)
}

// newSessionID mints a fresh, short session id from ids. It is shared by the
// session lifecycle and by a run that has no session to inherit, so a minted run
// session looks exactly like a launched one.
func newSessionID(ids IDGen) string {
	token := strings.TrimPrefix(ids.NewID(""), "-")
	if len(token) > 5 {
		token = token[:5]
	}
	return token
}

// Start records a session.started event for sid in project.
func (s *SessionService) Start(ctx context.Context, project core.ProjectID, sid string) (*core.Event, error) {
	if sid == "" {
		return nil, fmt.Errorf("%w: session id is required", core.ErrInvalid)
	}
	event := &core.Event{
		ProjectID: project,
		Kind:      core.EventSessionStarted,
		Session:   sid,
		Summary:   fmt.Sprintf("started session %s", sid),
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, event); err != nil {
		return nil, err
	}
	return event, nil
}

// Retire records a session.retired event for sid, naming successor (the session
// that replaces it) when there is one, so the stop reason is derivable.
func (s *SessionService) Retire(ctx context.Context, project core.ProjectID, sid, successor string) (*core.Event, error) {
	if sid == "" {
		return nil, fmt.Errorf("%w: session id is required", core.ErrInvalid)
	}
	summary := fmt.Sprintf("retired session %s", sid)
	event := &core.Event{
		ProjectID: project,
		Kind:      core.EventSessionRetired,
		Session:   sid,
		Summary:   summary,
	}
	if successor != "" {
		event.Summary = summary + " for " + successor
		event.Data = map[string]any{"successor": successor}
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, event); err != nil {
		return nil, err
	}
	return event, nil
}

// Ack records the caller's acknowledgement of the session its environment
// carries. The event is deliberately given no session of its own: appendEvent
// stamps it from the caller context (FACTOTUM_SESSION), so an ft invocation that
// does not carry a session is refused instead of recording an ack that names a
// session it never had. A run that judges a child's work by attribution
// requires the ack, which is what keeps an empty produced set from being
// indistinguishable from a child that never picked up the id.
func (s *SessionService) Ack(ctx context.Context, project core.ProjectID) (*core.Event, error) {
	_, sid, ok := Caller(ctx)
	if !ok || sid == "" {
		return nil, fmt.Errorf("%w: session ack needs a session: set FACTOTUM_SESSION", core.ErrInvalid)
	}
	event := &core.Event{
		ProjectID: project,
		Kind:      core.EventSessionAcked,
		Summary:   fmt.Sprintf("acknowledged session %s", sid),
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, event); err != nil {
		return nil, err
	}
	return event, nil
}
