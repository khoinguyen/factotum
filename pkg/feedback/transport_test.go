package feedback

import (
	"context"
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/memory"
)

func testEnv(t *testing.T) (Env, *memory.Backend) {
	t.Helper()
	backend := memory.New()
	return Env{
		Backend: backend,
		Project: core.ProjectID("factotum"),
		Clock:   app.SystemClock{},
		IDs:     app.RandomIDGen{},
	}, backend
}

func TestBuiltinsRegistersDirectDB(t *testing.T) {
	if _, ok := Builtins().Lookup("db"); !ok {
		t.Fatal("Builtins() has no db transport")
	}
}

func TestDBFactoryRequiresBackend(t *testing.T) {
	if _, err := DBFactory(Env{}); err == nil {
		t.Fatal("DBFactory(Env{}) error = nil, want an error")
	}
}

func TestDBSendStoresLabeledTask(t *testing.T) {
	env, backend := testEnv(t)
	transport, err := DBFactory(env)
	if err != nil {
		t.Fatalf("DBFactory() error = %v", err)
	}
	report := Report{Message: "blocked tasks show in next", Flow: "ft task next", Version: "dev", Project: "acme"}

	task, err := transport.Send(context.Background(), report)
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if task.Kind != core.KindTask {
		t.Errorf("kind = %q, want task", task.Kind)
	}
	if task.ProjectID != env.Project {
		t.Errorf("project = %q, want %q", task.ProjectID, env.Project)
	}
	if !store.MatchLabels(*task, []string{LabelExternalFeedback}) {
		t.Errorf("labels = %v, want %s", task.Labels, LabelExternalFeedback)
	}
	if task.Title != "blocked tasks show in next" {
		t.Errorf("title = %q", task.Title)
	}
	if task.Description != report.Body() {
		t.Errorf("description = %q, want the report body", task.Description)
	}

	stored, err := backend.Tasks().Get(context.Background(), task.ID)
	if err != nil {
		t.Fatalf("stored Get() error = %v", err)
	}
	if stored.Description != report.Body() {
		t.Errorf("stored description = %q, want the report body", stored.Description)
	}
}

func TestDBSendCreatesMissingProject(t *testing.T) {
	env, backend := testEnv(t)
	transport, err := DBFactory(env)
	if err != nil {
		t.Fatalf("DBFactory() error = %v", err)
	}
	if _, err := backend.Projects().Get(context.Background(), env.Project); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("precondition: project exists (%v)", err)
	}
	if _, err := transport.Send(context.Background(), Report{Message: "hi"}); err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if _, err := backend.Projects().Get(context.Background(), env.Project); err != nil {
		t.Fatalf("project not created: %v", err)
	}
	events, err := backend.Events().List(context.Background(), store.EventFilter{Kinds: []core.EventKind{core.EventProjectCreated}})
	if err != nil {
		t.Fatalf("List(events) error = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("project.created events = %d, want 1", len(events))
	}
}

func TestDBSendRejectsEmptyMessage(t *testing.T) {
	env, _ := testEnv(t)
	transport, err := DBFactory(env)
	if err != nil {
		t.Fatalf("DBFactory() error = %v", err)
	}
	if _, err := transport.Send(context.Background(), Report{}); err == nil {
		t.Fatal("Send(empty) error = nil, want a validation error")
	}
}
