package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/judge"
	_ "github.com/khoinguyen/factotum/pkg/judge/typesafe" // register the default provider
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

	// Judge is the model-backed judgment port. It is Disabled when no API key is
	// configured, so every judge-backed feature falls back to its deterministic path.
	Judge judge.Judge
	// JudgeOverride, when set, replaces the configured judge. Tests inject a fake
	// here so judge-backed command paths run without a network.
	JudgeOverride judge.Judge
	// When resolves natural-language date phrases. Nil until Attach, and its judge is
	// Disabled without a key, so the deterministic formats still work.
	When *app.WhenService
	// Intent reads closed-set task fields from a natural-language phrase.
	Intent *app.IntentService
	// Rerank reorders a lexical artifact shortlist by meaning.
	Rerank *app.RerankService
	// Duplicate advises when a new task looks like an existing one.
	Duplicate *app.DuplicateService
	// Reference finds the task a note refers to in prose.
	Reference *app.ReferenceService
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
		Judge:          newJudge(getenv, config.Config{}),
		StoreFactories: registry.New[store.Factory](),
		Rankers:        rank.Builtins(),
		Renderers:      render.Builtins(),
	}
	deps.Commands = builtinCommands()
	return deps
}

// newJudge builds the judge for the configured provider. The provider is chosen by
// name; pkg/judge owns the registry and each provider (typesafe is the default) owns
// its options and environment. An unknown provider or a build error disables the
// judge, so callers fall back.
func newJudge(getenv func(string) string, cfg config.Config) judge.Judge {
	provider := cfg.Judge.Provider
	if provider == "" {
		provider = "typesafe"
	}
	built, err := judge.New(provider, getenv, cfg.Judge.Options)
	if err != nil || built == nil {
		return judge.Disabled{}
	}
	return built
}

func (d *Deps) Attach(cfg config.Config, backend store.Backend) {
	d.Config = cfg
	d.Backend = backend
	if d.JudgeOverride != nil {
		d.Judge = d.JudgeOverride
	} else {
		d.Judge = newJudge(d.Getenv, cfg)
	}
	d.When = app.NewWhenService(d.Judge)
	d.Intent = app.NewIntentService(d.Judge)
	d.Rerank = app.NewRerankService(d.Judge)
	d.Duplicate = app.NewDuplicateService(d.Judge)
	d.Reference = app.NewReferenceService(d.Judge)
	d.Projects = app.NewProjectService(backend, d.Clock, d.IDs)
	d.Tasks = app.NewTaskService(backend, d.Clock, d.IDs)
	d.Actors = app.NewActorService(backend, d.Clock, d.IDs)
	d.Artifacts = app.NewArtifactService(backend, d.Clock, d.IDs)
}

// errWhenFormat names the accepted time forms for a usage error.
var errWhenFormat = errors.New("want RFC3339, YYYY-MM-DD, +<duration>, or a natural-language date")

// parseWhen resolves a time phrase: the deterministic formats first, then the judge
// for natural language. With no judge, only the deterministic forms are accepted.
func (d *Deps) parseWhen(ctx context.Context, value string, now time.Time) (time.Time, error) {
	if when, err := parseNotBefore(value, now); err == nil {
		return when, nil
	}
	if d.When == nil {
		return time.Time{}, errWhenFormat
	}
	when, err := d.When.Parse(ctx, value, now)
	if errors.Is(err, judge.ErrUnavailable) {
		return time.Time{}, errWhenFormat
	}
	return when, err
}

// rerankArtifacts reorders a lexical shortlist by meaning. With no judge it reports
// that --rerank needs a key, rather than silently returning lexical order.
// maybeRerank reorders a lexical shortlist by meaning when a judge is configured and
// rerank is not disabled. Search must never fail because the judge is absent or slow,
// so it falls back to the lexical order; a real failure is reported as a warning.
func (d *Deps) maybeRerank(cmd *cobra.Command, query string, artifacts []*core.Artifact, disabled bool) []*core.Artifact {
	if disabled || len(artifacts) < 2 || d.Rerank == nil {
		return artifacts
	}
	ordered, err := d.Rerank.Rerank(cmd.Context(), query, artifacts)
	if err == nil {
		return ordered
	}
	if !errors.Is(err, judge.ErrUnavailable) {
		_, _ = fmt.Fprintf(d.Err, "ft: warning: rerank unavailable (%v); using lexical order\n", err)
	}
	return artifacts
}
