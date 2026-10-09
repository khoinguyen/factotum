package transport

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/khoinguyen/factotum/pkg/doctor"
	"github.com/khoinguyen/factotum/pkg/embed"
)

// countingEmbedder is a fake inner embedder that fails on demand and records how
// many times it was called, so a test can prove the guard skipped the embed.
type countingEmbedder struct {
	calls int
	err   error
}

func (c *countingEmbedder) Embed(context.Context, embed.Input, []string) ([][]float32, error) {
	c.calls++
	if c.err != nil {
		return nil, c.err
	}
	return [][]float32{{1}}, nil
}

// fakeProber records probe calls and returns configured errors, so a test can
// model an endpoint that is down, up, or up but missing the model.
type fakeProber struct {
	reachableErr error
	modelErr     error
	reachCalls   int
	modelCalls   int
}

func (f *fakeProber) Reachable(context.Context, doctor.Endpoint) error {
	f.reachCalls++
	return f.reachableErr
}

func (f *fakeProber) HasModel(context.Context, doctor.Endpoint) error {
	f.modelCalls++
	return f.modelErr
}

func (f *fakeProber) Resolvable(context.Context, string) error { return nil }

func testEndpoint() doctor.Endpoint {
	return doctor.Endpoint{Protocol: "openai", URL: "http://127.0.0.1:8080", Model: "nomic-embed"}
}

func TestGuardNamesModelNotServedAndCaches(t *testing.T) {
	inner := &countingEmbedder{err: embed.ErrEmptyResponse}
	prober := &fakeProber{modelErr: fmt.Errorf("%w: model %q not served", doctor.ErrModelNotServed, "nomic-embed")}
	guard := NewGuard(inner, prober, testEndpoint())

	for i := 1; i <= 2; i++ {
		_, err := guard.Embed(context.Background(), embed.InputQuery, []string{"q"})
		if !errors.Is(err, embed.ErrModelNotServed) {
			t.Fatalf("call %d: error = %v, want ErrModelNotServed", i, err)
		}
	}
	if inner.calls != 0 {
		t.Fatalf("inner embed calls = %d, want 0 (the embed must be skipped)", inner.calls)
	}
	if prober.reachCalls != 1 || prober.modelCalls != 1 {
		t.Fatalf("probe calls = (reach %d, model %d), want (1, 1)", prober.reachCalls, prober.modelCalls)
	}
}

func TestGuardNamesEndpointUnreachableAndCaches(t *testing.T) {
	inner := &countingEmbedder{err: errors.New("embed: request failed")}
	prober := &fakeProber{reachableErr: errors.New("connection refused")}
	guard := NewGuard(inner, prober, testEndpoint())

	for i := 1; i <= 2; i++ {
		_, err := guard.Embed(context.Background(), embed.InputQuery, []string{"q"})
		if !errors.Is(err, embed.ErrEndpointUnreachable) {
			t.Fatalf("call %d: error = %v, want ErrEndpointUnreachable", i, err)
		}
	}
	if inner.calls != 0 {
		t.Fatalf("inner embed calls = %d, want 0 (the embed must be skipped)", inner.calls)
	}
	if prober.reachCalls != 1 || prober.modelCalls != 0 {
		t.Fatalf("probe calls = (reach %d, model %d), want (1, 0)", prober.reachCalls, prober.modelCalls)
	}
}

// A model probe that cannot attribute the failure to "model not served" - a
// rejected credential (401/403), an endpoint that does not implement the
// model-list route (404), a decode failure - must not be named as a missing
// model and must not skip an embed that would still work.
func TestGuardDoesNotMislabelUnattributableModelProbe(t *testing.T) {
	tests := []struct {
		name     string
		modelErr error
	}{
		{"auth failure", fmt.Errorf("%w: 401 Unauthorized", doctor.ErrAuth)},
		{"models route unimplemented", errors.New("embed: 404 Not Found: not found")},
		{"decode failure", errors.New("embed: decode model list: unexpected EOF")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &countingEmbedder{}
			prober := &fakeProber{modelErr: tt.modelErr}
			guard := NewGuard(inner, prober, testEndpoint())

			for i := 1; i <= 2; i++ {
				vectors, err := guard.Embed(context.Background(), embed.InputQuery, []string{"q"})
				if err != nil {
					t.Fatalf("call %d: a probe that cannot name the model failure must not skip the embed: %v", i, err)
				}
				if errors.Is(err, embed.ErrModelNotServed) {
					t.Fatalf("call %d: probe failure %v was mislabeled as ErrModelNotServed", i, tt.modelErr)
				}
				if len(vectors) != 1 {
					t.Fatalf("call %d: vectors = %v, want one", i, vectors)
				}
			}
			if inner.calls != 2 {
				t.Fatalf("inner embed calls = %d, want 2 (the unattributable probe must not be cached as fatal)", inner.calls)
			}
			if prober.modelCalls != 1 {
				t.Fatalf("model probe calls = %d, want 1 (probed once per process)", prober.modelCalls)
			}
		})
	}
}

func TestGuardPassesThroughSuccess(t *testing.T) {
	inner := &countingEmbedder{}
	prober := &fakeProber{}
	guard := NewGuard(inner, prober, testEndpoint())

	for i := 1; i <= 2; i++ {
		vectors, err := guard.Embed(context.Background(), embed.InputQuery, []string{"q"})
		if err != nil || len(vectors) != 1 {
			t.Fatalf("Embed() = %v, %v", vectors, err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("inner embed calls = %d, want 2", inner.calls)
	}
	if prober.reachCalls != 1 || prober.modelCalls != 1 {
		t.Fatalf("probe calls = (reach %d, model %d), want (1, 1): a clean probe must not repeat", prober.reachCalls, prober.modelCalls)
	}
}

// A failure the probe cannot attribute stays unnamed and uncached: it may be
// transient, so the next call must try the embed again.
func TestGuardDoesNotCacheUnattributedFailure(t *testing.T) {
	inner := &countingEmbedder{err: errors.New("embed: decode response: unexpected EOF")}
	guard := NewGuard(inner, &fakeProber{}, testEndpoint())

	for i := 1; i <= 2; i++ {
		_, err := guard.Embed(context.Background(), embed.InputQuery, []string{"q"})
		if errors.Is(err, embed.ErrEndpointUnreachable) || errors.Is(err, embed.ErrModelNotServed) {
			t.Fatalf("call %d: unattributable failure was named: %v", i, err)
		}
	}
	if inner.calls != 2 {
		t.Fatalf("inner embed calls = %d, want 2 (an unattributed failure is not cached)", inner.calls)
	}
}
