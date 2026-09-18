package core

import (
	"fmt"
	"strings"
	"time"
)

type ActorKind string

const (
	ActorHuman ActorKind = "human"
	ActorAgent ActorKind = "agent"
)

func (k ActorKind) Valid() bool {
	return k == ActorHuman || k == ActorAgent
}

type Actor struct {
	ID        ActorID
	Kind      ActorKind
	Name      string
	Active    bool
	CreatedAt time.Time
}

func (a Actor) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("%w: actor id is required", ErrInvalid)
	}
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: unknown actor kind %q", ErrInvalid, a.Kind)
	}
	if strings.TrimSpace(a.Name) == "" {
		return fmt.Errorf("%w: actor name is required", ErrInvalid)
	}
	return nil
}
