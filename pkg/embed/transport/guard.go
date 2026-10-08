package transport

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/khoinguyen/factotum/pkg/doctor"
	"github.com/khoinguyen/factotum/pkg/embed"
)

// Guard wraps an embedder to make a broken backend actionable and cheap. Before
// the first embed it probes the configured endpoint through doctor: an endpoint
// that does not answer, or does not serve the model, is named as the cause and
// cached, so the embed itself is skipped and every later call returns the same
// error without touching the network again. A failure that surfaces after a clean
// probe (for example an empty response) is named and cached too; a failure the
// guard cannot attribute is passed through un-cached, since it may be transient.
// It is the only place that pairs the wire transports with doctor's probes.
type Guard struct {
	inner    embed.Embedder
	probe    doctor.Prober
	endpoint doctor.Endpoint

	mu     sync.Mutex
	probed bool
	fatal  error
}

// NewGuard builds a guard over an embedder, a prober, and the endpoint to probe.
func NewGuard(inner embed.Embedder, probe doctor.Prober, endpoint doctor.Endpoint) *Guard {
	return &Guard{inner: inner, probe: probe, endpoint: endpoint}
}

func (g *Guard) Embed(ctx context.Context, input embed.Input, texts []string) ([][]float32, error) {
	if g == nil || g.inner == nil {
		return nil, embed.ErrUnavailable
	}
	if fatal := g.cached(); fatal != nil {
		return nil, fatal
	}
	if cause := g.detect(ctx); cause != nil {
		g.cache(cause)
		return nil, cause
	}
	vectors, err := g.inner.Embed(ctx, input, texts)
	if err == nil {
		return vectors, nil
	}
	if isNamedCause(err) {
		g.cache(err)
	}
	return nil, err
}

// detect probes the endpoint once and returns a named cause when the endpoint is
// down or the model is not served, before any embed is paid. Once the probe is
// clean it is not repeated, so a healthy backend is probed only once per process.
func (g *Guard) detect(ctx context.Context) error {
	g.mu.Lock()
	if g.probed || g.probe == nil {
		g.mu.Unlock()
		return nil
	}
	g.probed = true
	g.mu.Unlock()

	if err := g.probe.Reachable(ctx, g.endpoint); err != nil {
		return fmt.Errorf("%w: %s: %w", embed.ErrEndpointUnreachable, g.endpoint.URL, err)
	}
	if err := g.probe.HasModel(ctx, g.endpoint); err != nil {
		return fmt.Errorf("%w: %w", embed.ErrModelNotServed, err)
	}
	return nil
}

func (g *Guard) cached() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fatal
}

func (g *Guard) cache(err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.fatal == nil {
		g.fatal = err
	}
}

// isNamedCause reports whether an error carries one of the causes a caller can act
// on, and so is worth caching for the session.
func isNamedCause(err error) bool {
	return errors.Is(err, embed.ErrEndpointUnreachable) ||
		errors.Is(err, embed.ErrModelNotServed) ||
		errors.Is(err, embed.ErrEmptyResponse)
}
