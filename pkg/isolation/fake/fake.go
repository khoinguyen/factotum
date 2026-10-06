// Package fake is a deterministic, in-memory isolation backend for tests. It
// never creates a sandbox, container, or process: it records every request and
// replays a programmed result, so port contracts and composition can be tested
// without a runtime.
package fake

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// Backend is an in-memory IsolationBackend.
type Backend struct {
	name string

	mu       sync.Mutex
	prepared []isolation.Spec
	commands []isolation.Command
	files    map[string]isolation.File
	policies []isolation.Policy
	creds    []isolation.Credential
	stopped  []string
	deleted  []string
	result   isolation.ExecResult
	events   []isolation.Event
	nextID   int
	fail     error
}

// New returns a fake backend registered under name.
func New(name string) *Backend {
	return &Backend{name: name, files: map[string]isolation.File{}}
}

// Program sets the result and streamed events every Exec replays.
func (b *Backend) Program(result isolation.ExecResult, events ...isolation.Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.result = result
	b.events = events
}

// FailWith makes every operation return err, so callers can exercise their
// error path.
func (b *Backend) FailWith(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fail = err
}

func (b *Backend) Name() string { return b.name }

func (b *Backend) Prepare(_ context.Context, spec isolation.Spec) (isolation.Handle, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return nil, b.fail
	}
	b.prepared = append(b.prepared, spec)
	for _, f := range spec.Files {
		b.files[f.Path] = f
	}
	b.nextID++
	return &handle{id: fmt.Sprintf("%s-%d", b.name, b.nextID)}, nil
}

func (b *Backend) Exec(_ context.Context, _ isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return nil, b.fail
	}
	b.commands = append(b.commands, cmd)
	events := make(chan isolation.Event, len(b.events))
	for _, ev := range b.events {
		if ev.Time.IsZero() {
			ev.Time = time.Unix(0, 0).UTC()
		}
		events <- ev
	}
	close(events)
	return &execution{events: events, result: b.result}, nil
}

func (b *Backend) Upload(_ context.Context, _ isolation.Handle, files []isolation.File) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	for _, f := range files {
		b.files[f.Path] = f
	}
	return nil
}

func (b *Backend) Download(_ context.Context, _ isolation.Handle, paths []string) ([]isolation.File, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return nil, b.fail
	}
	out := make([]isolation.File, 0, len(paths))
	for _, p := range paths {
		if f, ok := b.files[p]; ok {
			out = append(out, f)
		}
	}
	return out, nil
}

func (b *Backend) Logs(_ context.Context, _ isolation.Handle, _ isolation.LogOptions) (<-chan isolation.Event, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return nil, b.fail
	}
	events := make(chan isolation.Event, len(b.events))
	for _, ev := range b.events {
		events <- ev
	}
	close(events)
	return events, nil
}

func (b *Backend) Stop(_ context.Context, h isolation.Handle) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.stopped = append(b.stopped, h.ID())
	return nil
}

func (b *Backend) Delete(_ context.Context, h isolation.Handle) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.deleted = append(b.deleted, h.ID())
	return nil
}

func (b *Backend) ApplyPolicy(_ context.Context, _ isolation.Handle, p isolation.Policy) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.policies = append(b.policies, p)
	return nil
}

func (b *Backend) AttachCredential(_ context.Context, _ isolation.Handle, c isolation.Credential) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail != nil {
		return b.fail
	}
	b.creds = append(b.creds, c)
	return nil
}

// Prepared returns the specs passed to Prepare, in order.
func (b *Backend) Prepared() []isolation.Spec {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]isolation.Spec(nil), b.prepared...)
}

// Commands returns the commands passed to Exec, in order.
func (b *Backend) Commands() []isolation.Command {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]isolation.Command(nil), b.commands...)
}

// Policies returns the policies passed to ApplyPolicy, in order.
func (b *Backend) Policies() []isolation.Policy {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]isolation.Policy(nil), b.policies...)
}

// Credentials returns the credentials passed to AttachCredential, in order.
func (b *Backend) Credentials() []isolation.Credential {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]isolation.Credential(nil), b.creds...)
}

// Stopped returns the ids passed to Stop, in order.
func (b *Backend) Stopped() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.stopped...)
}

// Deleted returns the ids passed to Delete, in order.
func (b *Backend) Deleted() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.deleted...)
}

type handle struct{ id string }

func (h *handle) ID() string { return h.id }

type execution struct {
	events <-chan isolation.Event
	result isolation.ExecResult
}

func (e *execution) Events() <-chan isolation.Event { return e.events }

func (e *execution) Wait(context.Context) (isolation.ExecResult, error) {
	return e.result, nil
}

var _ isolation.IsolationBackend = (*Backend)(nil)
