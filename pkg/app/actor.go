package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

type ActorService struct {
	backend store.Backend
	clock   Clock
	ids     IDGen
}

func NewActorService(backend store.Backend, clock Clock, ids IDGen) *ActorService {
	return &ActorService{backend: backend, clock: clock, ids: ids}
}

func (s *ActorService) Add(ctx context.Context, kind core.ActorKind, name string) (*core.Actor, error) {
	if _, err := s.backend.Actors().FindByName(ctx, name); err == nil {
		return nil, fmt.Errorf("%w: actor named %q", core.ErrAlreadyExists, name)
	} else if !errors.Is(err, core.ErrNotFound) {
		return nil, err
	}

	id := core.ActorID(Slug(name))
	if id == "" {
		id = core.ActorID(s.ids.NewID("act"))
	} else if _, err := s.backend.Actors().Get(ctx, id); err == nil {
		return nil, fmt.Errorf("%w: actor id %q", core.ErrAlreadyExists, id)
	} else if !errors.Is(err, core.ErrNotFound) {
		return nil, err
	}

	actor := &core.Actor{
		ID:        id,
		Kind:      kind,
		Name:      name,
		Active:    true,
		CreatedAt: s.clock.Now(),
	}
	if err := actor.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Actors().Create(ctx, actor); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		Kind:    core.EventActorCreated,
		Summary: fmt.Sprintf("added %s %s", actor.Kind, actor.Name),
	}); err != nil {
		return nil, err
	}
	return actor, nil
}

func (s *ActorService) Get(ctx context.Context, id core.ActorID) (*core.Actor, error) {
	return s.backend.Actors().Get(ctx, id)
}

// Identity resolves an actor id to the identity it acts as: the registered record
// when there is one, otherwise a loop-role default as an active agent. A loop role
// is the mailbox a session registers as a run, not a stored Actor record, so it
// needs no registration; any other unregistered id stays ErrNotFound. Readiness,
// grooming, claim, assign, promote, and waiting-on share this resolution, so a
// role can hold work and counts as agent work on every surface that judges it.
func (s *ActorService) Identity(ctx context.Context, id core.ActorID) (*core.Actor, error) {
	return actorIdentity(ctx, s.backend.Actors(), id)
}

func actorIdentity(ctx context.Context, actors store.ActorRepo, id core.ActorID) (*core.Actor, error) {
	actor, err := actors.Get(ctx, id)
	if err == nil || !errors.Is(err, core.ErrNotFound) {
		return actor, err
	}
	if identity, ok := RoleIdentity(id); ok {
		return &identity, nil
	}
	return nil, err
}

// RoleIdentity reports whether id is a loop role and returns the agent identity
// it acts as. It is the record-free half of the identity rule, so a surface with
// only an actor id (a display label, a read-only filter) resolves a role without
// opening the store.
func RoleIdentity(id core.ActorID) (core.Actor, bool) {
	role, ok := LoopDefaultRole(string(id))
	if !ok {
		return core.Actor{}, false
	}
	return RoleActor(role), true
}

// RoleActor is the identity a loop role acts as: an active agent named by its
// role id. It is synthesized, never stored - the role is a mailbox, not a record.
func RoleActor(role core.ActorID) core.Actor {
	return core.Actor{ID: role, Kind: core.ActorAgent, Name: string(role), Active: true}
}

func (s *ActorService) List(ctx context.Context) ([]*core.Actor, error) {
	return s.backend.Actors().List(ctx)
}

// Resolve finds an actor by ID first, then by exact name.
func (s *ActorService) Resolve(ctx context.Context, ref string) (*core.Actor, error) {
	if actor, err := s.backend.Actors().Get(ctx, core.ActorID(ref)); err == nil {
		return actor, nil
	} else if !errors.Is(err, core.ErrNotFound) {
		return nil, err
	}
	return s.backend.Actors().FindByName(ctx, ref)
}

func (s *ActorService) Deactivate(ctx context.Context, id core.ActorID) (*core.Actor, error) {
	actor, err := s.backend.Actors().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	actor.Active = false
	if err := s.backend.Actors().Update(ctx, actor); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		Kind:    core.EventActorUpdated,
		Summary: fmt.Sprintf("deactivated %s", actor.Name),
	}); err != nil {
		return nil, err
	}
	return actor, nil
}
