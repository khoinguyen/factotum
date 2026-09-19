package cli

import (
	"io"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/render"
	"github.com/khoinguyen/factotum/pkg/store"
)

type CommandFactory func(deps *Deps) *cobra.Command

type Deps struct {
	Config       config.Config
	Backend      store.Backend
	Clock        app.Clock
	IDs          app.IDGen
	Out          io.Writer
	Err          io.Writer
	Getenv       func(string) string
	ActorRef     string
	OutputFormat string
	NoHints      bool

	StoreFactories *registry.Registry[store.Factory]
	Rankers        *registry.Registry[rank.Ranker]
	Renderers      *registry.Registry[render.Renderer]
	Commands       *registry.Registry[CommandFactory]

	Projects  *app.ProjectService
	Tasks     *app.TaskService
	Actors    *app.ActorService
	Artifacts *app.ArtifactService
}

func NewDeps(clock app.Clock, ids app.IDGen, out, errOut io.Writer, getenv func(string) string) *Deps {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}
	deps := &Deps{
		Clock:          clock,
		IDs:            ids,
		Out:            out,
		Err:            errOut,
		Getenv:         getenv,
		StoreFactories: registry.New[store.Factory](),
		Rankers:        rank.Builtins(),
		Renderers:      render.Builtins(),
	}
	deps.Commands = builtinCommands()
	return deps
}

func (d *Deps) Attach(cfg config.Config, backend store.Backend) {
	d.Config = cfg
	d.Backend = backend
	d.Projects = app.NewProjectService(backend, d.Clock, d.IDs)
	d.Tasks = app.NewTaskService(backend, d.Clock, d.IDs)
	d.Actors = app.NewActorService(backend, d.Clock, d.IDs)
	d.Artifacts = app.NewArtifactService(backend, d.Clock, d.IDs)
}
