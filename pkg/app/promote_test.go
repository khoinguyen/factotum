package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
	"github.com/khoinguyen/factotum/pkg/store/sqlite"
)

// atomicIDs is a thread-safe IDGen, so a parallel test does not race on the
// harness's sequential generator.
type atomicIDs struct{ count atomic.Int64 }

func (g *atomicIDs) NewID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, g.count.Add(1))
}

// newBackendHarness builds the services over a given backend, so a test can
// prove a store-level guarantee on the durable backends rather than only on
// memory.
func newBackendHarness(t *testing.T, backend store.Backend) *harness {
	t.Helper()
	t.Cleanup(func() { _ = backend.Close() })
	clock := fixedClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	ids := &atomicIDs{}
	h := &harness{
		backend:   backend,
		clock:     clock,
		ids:       ids,
		projects:  NewProjectService(backend, clock, ids),
		tasks:     NewTicketService(backend, clock, ids),
		actors:    NewActorService(backend, clock, ids),
		artifacts: NewArtifactService(backend, clock, ids),
		qa:        NewQAService(backend, clock, ids),
	}
	h.tasks.SetCaptureReminder(h.qa)
	return h
}

// TestPromoteIsAtomicUnderConcurrency proves capture promotion has no
// check-then-write race inside the process: N parallel promotes yield exactly
// one task, one origin edge, one promotion note, and one created event, and
// every caller returns the winning task rather than an error. The two file
// backends serialize on a process-local lock, so two processes on one file are
// outside this guarantee (sqlite's is additionally cross-process).
func TestPromoteIsAtomicUnderConcurrency(t *testing.T) {
	backends := map[string]func(t *testing.T) store.Backend{
		"jsonfile": func(t *testing.T) store.Backend {
			backend, err := jsonfile.Open(context.Background(), store.Config{
				Backend: "jsonfile",
				Options: map[string]string{"path": filepath.Join(t.TempDir(), "db.json")},
			})
			if err != nil {
				t.Fatalf("open jsonfile: %v", err)
			}
			return backend
		},
		"sqlite": func(t *testing.T) store.Backend {
			backend, err := sqlite.Open(context.Background(), store.Config{
				Backend: "sqlite",
				Options: map[string]string{"path": filepath.Join(t.TempDir(), "db.sqlite")},
			})
			if err != nil {
				t.Fatalf("open sqlite: %v", err)
			}
			return backend
		},
	}
	for name, open := range backends {
		t.Run(name, func(t *testing.T) { testPromoteConcurrent(t, open(t)) })
	}
}

func testPromoteConcurrent(t *testing.T, backend store.Backend) {
	h := newBackendHarness(t, backend)
	ctx := context.Background()
	project := h.newProject(t)
	capture := newIdea(t, h, project, "a spark")
	agent := newAgent(t, h, "claude")

	const calls = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make([]*core.Ticket, calls)
	errs := make([]error, calls)
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = h.tasks.Promote(ctx, capture.ID, groomedPromote(agent))
		}(i)
	}
	close(start)
	wg.Wait()

	for i := range results {
		if errs[i] != nil {
			t.Fatalf("Promote(%d) error = %v", i, errs[i])
		}
		if results[i] == nil {
			t.Fatalf("Promote(%d) returned no task", i)
		}
		if results[i].ID != results[0].ID {
			t.Fatalf("Promote(%d) = %s, want every caller to get %s", i, results[i].ID, results[0].ID)
		}
	}
	promoted := results[0]

	dependents, err := h.tasks.List(ctx, store.TicketFilter{ProjectID: project.ID, DependsOn: &capture.ID})
	if err != nil {
		t.Fatalf("List(dependents) error = %v", err)
	}
	if len(dependents) != 1 || dependents[0].ID != promoted.ID || dependents[0].Kind != core.KindTask {
		t.Fatalf("dependents of the capture = %v, want only the task %s", dependents, promoted.ID)
	}

	stored, err := h.tasks.Get(ctx, capture.ID)
	if err != nil {
		t.Fatalf("Get(capture) error = %v", err)
	}
	promotionNotes := 0
	for _, note := range stored.Notes {
		if note.Body == fmt.Sprintf("Promoted to %s.", promoted.ID) {
			promotionNotes++
		}
	}
	if promotionNotes != 1 {
		t.Fatalf("capture has %d promotion notes, want exactly 1: %+v", promotionNotes, stored.Notes)
	}

	events, err := h.backend.Events().List(ctx, store.EventFilter{ProjectID: project.ID, TicketID: &promoted.ID})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	createdEvents := 0
	for _, event := range events {
		if event.Kind == core.EventTaskCreated {
			createdEvents++
		}
	}
	if createdEvents != 1 {
		t.Fatalf("task %s has %d created events, want exactly 1", promoted.ID, createdEvents)
	}
}
