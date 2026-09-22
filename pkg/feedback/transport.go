package feedback

import (
	"context"
	"errors"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/store"
)

// Transport delivers one report to a sink and returns the stored task.
type Transport interface {
	Send(ctx context.Context, report Report) (*core.Task, error)
}

// Env is the resolved sink a transport writes to: the opened backend, the
// project that receives reports, and the clock/ids the store needs.
type Env struct {
	Backend store.Backend
	Project core.ProjectID
	Clock   app.Clock
	IDs     app.IDGen
}

// Factory builds a Transport from a resolved sink.
type Factory func(env Env) (Transport, error)

// DefaultTransport names the transport used when none is selected.
const DefaultTransport = "db"

// Builtins returns a registry of the built-in transports. The direct-database
// transport ships today; a GitHub or outbox transport is a later registration
// that leaves the command unchanged.
func Builtins() *registry.Registry[Factory] {
	reg := registry.New[Factory]()
	if err := reg.Register(DefaultTransport, DBFactory); err != nil {
		panic(err)
	}
	return reg
}

// DBFactory builds the direct-to-database transport: it stores the report as a
// task labeled external-feedback in the sink project, creating that project when
// the sink has never seen it.
func DBFactory(env Env) (Transport, error) {
	if env.Backend == nil {
		return nil, errors.New("feedback: direct-db transport needs a store backend")
	}
	return &dbTransport{
		backend: env.Backend,
		tasks:   app.NewTaskService(env.Backend, env.Clock, env.IDs),
		clock:   env.Clock,
		project: env.Project,
	}, nil
}

type dbTransport struct {
	backend store.Backend
	tasks   *app.TaskService
	clock   app.Clock
	project core.ProjectID
}

func (t *dbTransport) Send(ctx context.Context, report Report) (*core.Task, error) {
	if err := t.ensureProject(ctx); err != nil {
		return nil, err
	}
	task, err := t.tasks.Add(ctx, app.TaskInput{
		ProjectID:   t.project,
		Kind:        core.KindTask,
		Title:       report.Title(),
		Description: report.Body(),
		Labels:      []string{LabelExternalFeedback},
	})
	if err != nil {
		return nil, fmt.Errorf("store feedback: %w", err)
	}
	return task, nil
}

func (t *dbTransport) ensureProject(ctx context.Context) error {
	if _, err := t.backend.Projects().Get(ctx, t.project); err == nil {
		return nil
	} else if !errors.Is(err, core.ErrNotFound) {
		return fmt.Errorf("feedback sink project %q: %w", t.project, err)
	}
	now := t.clock.Now()
	project := &core.Project{
		ID:        t.project,
		Name:      string(t.project),
		Policy:    core.DefaultResolutionPolicy(),
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := t.backend.Projects().Create(ctx, project); err != nil && !errors.Is(err, core.ErrAlreadyExists) {
		return fmt.Errorf("create feedback sink project %q: %w", t.project, err)
	}
	return nil
}
