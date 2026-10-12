// Package app contains the use-case services that orchestrate the domain model
// and the storage ports. Every mutation records an event.
package app

import (
	"context"
	"crypto/rand"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// Clock is injected so timestamps are deterministic in tests.
type Clock interface {
	Now() time.Time
}

type SystemClock struct{}

func (SystemClock) Now() time.Time { return time.Now().UTC() }

// IDGen is injected so identifiers are deterministic in tests.
type IDGen interface {
	NewID(prefix string) string
}

type RandomIDGen struct{}

func (RandomIDGen) NewID(prefix string) string {
	return prefix + "-" + strings.ToLower(rand.Text()[:10])
}

// callerKey carries the acting actor and its chief session through a context so
// every event appended while it is live is attributed without threading the
// caller through every service constructor.
type callerKey struct{}

type caller struct {
	actor   core.ActorID
	session string
}

// WithCaller records the acting actor and chief session on ctx. An empty actor
// or session is ignored, so a command with neither stays unattributed.
func WithCaller(ctx context.Context, actor core.ActorID, session string) context.Context {
	if actor == "" && session == "" {
		return ctx
	}
	return context.WithValue(ctx, callerKey{}, caller{actor: actor, session: session})
}

// Caller returns the acting actor and session recorded on ctx, if any.
func Caller(ctx context.Context) (core.ActorID, string, bool) {
	c, ok := ctx.Value(callerKey{}).(caller)
	if !ok {
		return "", "", false
	}
	return c.actor, c.session, true
}

func appendEvent(ctx context.Context, backend store.Backend, clock Clock, ids IDGen, event *core.Event) error {
	if actor, session, ok := Caller(ctx); ok {
		if event.By == nil && actor != "" {
			id := actor
			event.By = &id
		}
		if event.Session == "" && session != "" {
			event.Session = session
		}
	}
	event.ID = core.EventID(ids.NewID("ev"))
	event.CreatedAt = clock.Now()
	if err := event.Validate(); err != nil {
		return err
	}
	return backend.Events().Append(ctx, event)
}
