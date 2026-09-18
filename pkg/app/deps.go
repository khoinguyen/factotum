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

func appendEvent(ctx context.Context, backend store.Backend, clock Clock, ids IDGen, event *core.Event) error {
	event.ID = core.EventID(ids.NewID("ev"))
	event.CreatedAt = clock.Now()
	if err := event.Validate(); err != nil {
		return err
	}
	return backend.Events().Append(ctx, event)
}
