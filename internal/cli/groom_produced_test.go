package cli

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

// TestProducedTaskIDsAttributesTheSession pins the produced set's shape: the
// session's own create is kept (a task and a capture alike), one it created and
// then deleted is dropped because it left nothing stranded, and a task stamped
// with another session is excluded so a concurrent writer cannot be judged as
// the session's work.
func TestProducedTaskIDsAttributesTheSession(t *testing.T) {
	ctx := context.Background()
	backend := memory.New()
	t.Cleanup(func() { _ = backend.Close() })
	clock, ids := app.SystemClock{}, app.RandomIDGen{}
	projects := app.NewProjectService(backend, clock, ids)
	tasks := app.NewTicketService(backend, clock, ids)

	project, err := projects.Create(ctx, "Acme", "acme", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	mine := app.WithCaller(ctx, "", "groom-1")
	add := func(ctx context.Context, kind core.TicketKind, title string) *core.Ticket {
		t.Helper()
		task, err := tasks.Add(ctx, app.TicketInput{ProjectID: project.ID, Kind: kind, Title: title})
		if err != nil {
			t.Fatalf("Add(%s) error = %v", title, err)
		}
		return task
	}

	kept := add(mine, core.KindTask, "Split child")
	capture := add(mine, core.KindIdea, "Maybe cache")
	gone := add(mine, core.KindTask, "Abandoned branch")
	if err := tasks.Delete(mine, gone.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	add(app.WithCaller(ctx, "", "groom-other"), core.KindTask, "Builder follow-up")

	produced, err := producedTaskIDs(ctx, backend, tasks, project.ID, "groom-1")
	if err != nil {
		t.Fatalf("producedTaskIDs() error = %v", err)
	}
	want := []string{string(capture.ID), string(kept.ID)}
	sort.Strings(want)
	if !reflect.DeepEqual(produced, want) {
		t.Fatalf("produced = %v, want %v (sorted; deleted and other-session ids dropped)", produced, want)
	}
}
