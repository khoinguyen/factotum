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

	actor := &core.Actor{
		ID:        core.ActorID(s.ids.NewID("act")),
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
