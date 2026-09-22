// Package store defines the storage ports. Adapters implement Backend; the
// conformance suite in pkg/store/conformance is the shared contract.
package store

import (
	"context"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

type Config struct {
	Backend string
	Options map[string]string
	// Noticef, when set, receives human-readable notices (such as a schema
	// migration and its backup path) that must not pollute structured stdout.
	Noticef func(format string, args ...any)
}

func (c Config) Option(key string) string {
	if c.Options == nil {
		return ""
	}
	return c.Options[key]
}

type ProjectRepo interface {
	Create(ctx context.Context, project *core.Project) error
	Get(ctx context.Context, id core.ProjectID) (*core.Project, error)
	List(ctx context.Context) ([]*core.Project, error)
	Update(ctx context.Context, project *core.Project) error
	Delete(ctx context.Context, id core.ProjectID) error
}

type TaskFilter struct {
	ProjectID core.ProjectID
	Repo      *string
	Statuses  []core.TaskStatus
	Kind      *core.TaskKind
	Labels    []string
}

// MatchLabels reports whether task carries every label in labels (AND).
func MatchLabels(task core.Task, labels []string) bool {
	for _, want := range labels {
		found := false
		for _, have := range task.Labels {
			if have == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

type TaskRepo interface {
	Create(ctx context.Context, task *core.Task) error
	Get(ctx context.Context, id core.TaskID) (*core.Task, error)
	List(ctx context.Context, filter TaskFilter) ([]*core.Task, error)
	Update(ctx context.Context, task *core.Task) error
	// UpdateExpected writes the task only if its stored UpdatedAt still equals
	// expected, returning ErrConflict otherwise (compare-and-swap). It lets a
	// caller that read a document reject a stale write atomically.
	UpdateExpected(ctx context.Context, task *core.Task, expected time.Time) error
	Delete(ctx context.Context, id core.TaskID) error
	// Search returns tasks in filter scope whose title, description, or notes
	// match the query, ranked by relevance. An empty query matches everything in
	// scope.
	Search(ctx context.Context, filter TaskFilter, query string) ([]TaskSearchHit, error)
}

type ActorRepo interface {
	Create(ctx context.Context, actor *core.Actor) error
	Get(ctx context.Context, id core.ActorID) (*core.Actor, error)
	FindByName(ctx context.Context, name string) (*core.Actor, error)
	List(ctx context.Context) ([]*core.Actor, error)
	Update(ctx context.Context, actor *core.Actor) error
	Delete(ctx context.Context, id core.ActorID) error
}

type ArtifactFilter struct {
	ProjectID core.ProjectID
	TaskID    *core.TaskID
	Kind      *core.ArtifactKind
}

type ArtifactRepo interface {
	Create(ctx context.Context, artifact *core.Artifact) error
	Get(ctx context.Context, id core.ArtifactID) (*core.Artifact, error)
	List(ctx context.Context, filter ArtifactFilter) ([]*core.Artifact, error)
	// Search returns artifacts in filter scope whose title or body matches the
	// query, ranked by relevance. An empty query matches everything in scope.
	Search(ctx context.Context, filter ArtifactFilter, query string) ([]SearchHit, error)
	Update(ctx context.Context, artifact *core.Artifact) error
	Delete(ctx context.Context, id core.ArtifactID) error
}

type EventFilter struct {
	ProjectID core.ProjectID
	TaskID    *core.TaskID
	Kinds     []core.EventKind
	Since     *time.Time
	Limit     int
}

type EventRepo interface {
	Append(ctx context.Context, event *core.Event) error
	List(ctx context.Context, filter EventFilter) ([]*core.Event, error)
}

type Backend interface {
	Projects() ProjectRepo
	Tasks() TaskRepo
	Actors() ActorRepo
	Artifacts() ArtifactRepo
	Events() EventRepo
	Close() error
}

type Factory func(ctx context.Context, cfg Config) (Backend, error)

// TxBackend is an optional capability: a backend that can run a function inside
// a transaction over the same repositories.
type TxBackend interface {
	WithTx(ctx context.Context, fn func(Backend) error) error
}

// Migrator is an optional capability: a backend with schema migrations.
type Migrator interface {
	Migrate(ctx context.Context) error
}
