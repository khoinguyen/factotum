package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/agent"
	_ "github.com/khoinguyen/factotum/pkg/agent/command" // register the default provider
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/check"
	checkbuiltins "github.com/khoinguyen/factotum/pkg/check/builtins"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/doctor"
	"github.com/khoinguyen/factotum/pkg/embed"
	_ "github.com/khoinguyen/factotum/pkg/embed/transport" // register the embedding providers
	"github.com/khoinguyen/factotum/pkg/feedback"
	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/harness/opencode"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/docker"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
	"github.com/khoinguyen/factotum/pkg/judge"
	_ "github.com/khoinguyen/factotum/pkg/judge/typesafe" // register the default provider
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/render"
	"github.com/khoinguyen/factotum/pkg/store"
	storesqlite "github.com/khoinguyen/factotum/pkg/store/sqlite"
	"github.com/khoinguyen/factotum/pkg/vector"
	vsqlite "github.com/khoinguyen/factotum/pkg/vector/sqlite"
)

type CommandFactory func(deps *Deps) *cobra.Command

// IsolationBackendFactory builds the ft run isolation backend named in the run
// config. It is a factory, not an instance, so the local backend's unsandboxed
// opt-in is applied from config at selection time. errOut receives backend
// notices (the local backend's unsandboxed warning).
type IsolationBackendFactory func(cfg config.Run, errOut io.Writer) (isolation.IsolationBackend, error)

// HarnessFactory builds the ft run harness named in the run config.
type HarnessFactory func(cfg config.Run) (harness.Harness, error)

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
	// UserConfigPath is the machine-scoped config path this invocation loaded,
	// so a command can resolve another project's store from the same file.
	UserConfigPath string
	// ProjectConfigPath is the project-scoped config path this invocation loaded,
	// so `ft init` writes the pin to the same file it would read.
	ProjectConfigPath string
	// Prompt, when set, replaces the terminal prompter for `ft init`. Tests inject
	// a scripted session; the default reads from stdin and writes prompts to stderr.
	Prompt Prompter
	// GitDetect, when set, replaces the git probe for `ft init`. Tests inject a fake
	// repository; nil runs the real git command.
	GitDetect func(dir string) (gitRepo, bool)
	// Full disables output bounding for one invocation (--full).
	Full bool
	// IsTerminal reports whether a writer is attached to a terminal. It defaults
	// to a real isatty check; tests override it to simulate a human session.
	IsTerminal func(io.Writer) bool
	// IsTerminalReader reports whether a reader (stdin) is attached to a
	// terminal. It defaults to a real isatty check; tests override it to
	// simulate an interactive stdin.
	IsTerminalReader func(io.Reader) bool

	// output is the buffer that replaced stdout while bounding is active.
	output *boundedOutput

	StoreFactories *registry.Registry[store.Factory]
	Rankers        *registry.Registry[rank.Ranker]
	Renderers      *registry.Registry[render.Renderer]
	Checks         *registry.Registry[check.Check]
	Commands       *registry.Registry[CommandFactory]
	// FeedbackTransports resolves feedback sink transports by name; the direct-DB
	// transport is the only built-in today.
	FeedbackTransports *registry.Registry[feedback.Factory]
	// RunBackends and RunHarnesses select the ft run adapters by name. The
	// selection is explicit (config/flags); there is no default backend.
	RunBackends  *registry.Registry[IsolationBackendFactory]
	RunHarnesses *registry.Registry[HarnessFactory]

	Projects  *app.ProjectService
	Tasks     *app.TaskService
	Actors    *app.ActorService
	Artifacts *app.ArtifactService
	// TaskChecks runs advisory checks and caches their results.
	TaskChecks *app.CheckService

	// Judge is the model-backed judgment port. It is Disabled when no API key is
	// configured, so every judge-backed feature falls back to its deterministic path.
	Judge judge.Judge
	// JudgeOverride, when set, replaces the configured judge. Tests inject a fake
	// here so judge-backed command paths run without a network.
	JudgeOverride judge.Judge
	// Agent is the inference port behind `ft prompt`. It is Disabled when no agent
	// CLI is configured, so the command reports that clearly.
	Agent agent.Agent
	// AgentOverride, when set, replaces the configured agent. Tests inject a fake
	// here so prompt-backed command paths run without a network.
	AgentOverride agent.Agent
	// agentErr records a misconfigured agent provider, so `ft prompt` can name the
	// bad provider instead of claiming none is configured.
	agentErr error
	// Embedder is the optional embedding port. It is Disabled when no provider is
	// configured, so vector recall is off by default and memory search stays lexical.
	Embedder embed.Embedder
	// EmbedderOverride, when set, replaces the configured embedder. Tests inject a
	// fake here so vector-backed command paths run without a network.
	EmbedderOverride embed.Embedder
	// Vectors is the vector side index. Nil when no embedding provider is configured.
	Vectors vector.Index
	// VectorsOverride, when set, replaces the built side index. Tests inject an
	// in-memory index here.
	VectorsOverride vector.Index
	// Retriever adds semantic candidates to memory search when the embedder and side
	// index are both configured.
	Retriever *app.VectorRetriever

	// DoctorProbe, when set, replaces the real endpoint/command prober so tests can
	// diagnose a subsystem without touching the network or the PATH. Nil uses the
	// transport prober.
	DoctorProbe doctor.Prober
	// DoctorFixRunner, when set, runs a `ft doctor --fix` command in tests instead
	// of exec.
	DoctorFixRunner func(ctx context.Context, argv []string, out io.Writer) error

	vectorCloser io.Closer
	vectorWarned bool
	// embedErr records a misconfigured embedding provider. It is surfaced at the
	// first vector operation, not on every command, so unrelated commands stay
	// quiet while a vector command still names the misconfiguration.
	embedErr    error
	embedWarned bool
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
		Clock:            clock,
		IDs:              ids,
		Out:              out,
		Err:              errOut,
		Getenv:           getenv,
		IsTerminal:       isTerminalWriter,
		IsTerminalReader: isTerminalReader,
		Judge:            newJudge(getenv, config.Config{}),
		Agent:            agent.Disabled{},
		Embedder:         embed.Disabled{},
		StoreFactories:   registry.New[store.Factory](),
		Rankers:          rank.Builtins(),
		Renderers:        render.Builtins(),
	}
	deps.Commands = builtinCommands()
	deps.FeedbackTransports = feedback.Builtins()
	deps.RunBackends = runBackends(getenv)
	deps.RunHarnesses = runHarnesses()
	return deps
}

// runBackends registers the built-in isolation backends. The local backend is
// registered but only ever constructible with the configured opt-in, so it can
// never run unsandboxed by default. The OpenShell backend is deny-by-default and
// non-root; it needs a reachable gateway and the openshell CLI, and refuses a
// run it cannot prepare. The docker backend runs a per-task container with only
// the resolved workspace mounted; it needs the docker CLI and a daemon.
func runBackends(getenv func(string) string) *registry.Registry[IsolationBackendFactory] {
	reg := registry.New[IsolationBackendFactory]()
	registerRunBackend(reg, local.Name, func(cfg config.Run, errOut io.Writer) (isolation.IsolationBackend, error) {
		return local.New(local.Options{AllowHost: cfg.AllowHost, Warn: errOut}), nil
	})
	registerRunBackend(reg, openshell.Name, func(cfg config.Run, errOut io.Writer) (isolation.IsolationBackend, error) {
		warnMissingProjectPolicy(errOut, cfg.PolicyPath)
		return openshell.New(openshellBackendOptions(cfg, getenv)), nil
	})
	registerRunBackend(reg, docker.Name, func(cfg config.Run, errOut io.Writer) (isolation.IsolationBackend, error) {
		return docker.New(docker.Options{Credentials: envCredentialResolver{getenv: getenv}}), nil
	})
	return reg
}

// warnMissingProjectPolicy surfaces a clear message when the OpenShell backend
// has no project policy override at the resolved path, so a deny-all run is not
// silent.
func warnMissingProjectPolicy(errOut io.Writer, policyPath string) {
	if errOut == nil || policyPath == "" {
		return
	}
	if _, err := os.Stat(policyPath); err == nil || !errors.Is(err, os.ErrNotExist) {
		return
	}
	_, _ = fmt.Fprintf(errOut, "ft: warning: openshell: no project policy at %s; egress is deny-all\n", policyPath)
}

// openshellBackendOptions translates the run config into OpenShell backend
// options: the project policy override path (whose allow_hosts widen egress) and
// a host-environment credential resolver. It is the single mapping point, so a
// run never wires empty Options and both reach the backend.
func openshellBackendOptions(cfg config.Run, getenv func(string) string) openshell.Options {
	return openshell.Options{
		OverridePath: cfg.PolicyPath,
		Credentials:  envCredentialResolver{getenv: getenv},
	}
}

// envCredentialResolver resolves a harness credential to its secret value from
// the host environment, keyed by the credential's EnvVar. It is shared by the
// isolating backends: OpenShell hands the value to the gateway as provider
// material (the sandbox sees a placeholder), and docker injects it per-exec
// through the docker client's environment. Neither writes the value into a
// container's stored environment.
type envCredentialResolver struct {
	getenv func(string) string
}

func (r envCredentialResolver) Resolve(_ context.Context, c isolation.Credential) (string, error) {
	if r.getenv == nil {
		return "", fmt.Errorf("run: no host environment to resolve credential %s", c.EnvVar)
	}
	value := r.getenv(c.EnvVar)
	if value == "" {
		return "", fmt.Errorf("run: credential %s is not set in the host environment", c.EnvVar)
	}
	return value, nil
}

// runHarnesses registers the built-in harnesses. The OpenCode harness targets
// the host `opencode` binary, which the dev-only local backend runs in place.
// The configured provider and credential variable are forwarded so an isolating
// backend can attach the credential as a provider placeholder.
func runHarnesses() *registry.Registry[HarnessFactory] {
	reg := registry.New[HarnessFactory]()
	registerRunHarness(reg, opencode.Name, func(cfg config.Run) (harness.Harness, error) {
		return opencode.New(opencode.Options{
			Model:            cfg.Model,
			Args:             cfg.Args,
			Provider:         cfg.Provider,
			CredentialEnvVar: cfg.CredentialEnvVar,
		}), nil
	})
	return reg
}

func registerRunBackend(reg *registry.Registry[IsolationBackendFactory], name string, factory IsolationBackendFactory) {
	if err := reg.Register(name, factory); err != nil {
		panic(err)
	}
}

func registerRunHarness(reg *registry.Registry[HarnessFactory], name string, factory HarnessFactory) {
	if err := reg.Register(name, factory); err != nil {
		panic(err)
	}
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

// newAgent builds the inference agent for the configured provider. The default
// (command) provider needs a configured agent CLI; with none it is Disabled, so
// `ft prompt` reports that no agent is configured. An unknown provider or a build
// error is returned so the command can name the misconfiguration.
func newAgent(getenv func(string) string, cfg config.Config) (agent.Agent, error) {
	provider := cfg.Agent.Provider
	if provider == "" {
		provider = "command"
	}
	built, err := agent.New(provider, getenv, cfg.Agent.Options)
	if err != nil {
		return agent.Disabled{}, err
	}
	if built == nil {
		return agent.Disabled{}, nil
	}
	return built, nil
}

// Attach records the loaded config and, when backend is non-nil, the services
// bound to the caller's store. A command that does not touch the caller's store
// passes a nil backend, so it runs even when that store is misconfigured; its
// Projects/Tasks/... services stay nil and must not be called.
func (d *Deps) Attach(cfg config.Config, backend store.Backend) {
	d.Config = cfg
	d.Backend = backend
	if d.JudgeOverride != nil {
		d.Judge = d.JudgeOverride
	} else {
		d.Judge = newJudge(d.Getenv, cfg)
	}
	if d.AgentOverride != nil {
		d.Agent = d.AgentOverride
		d.agentErr = nil
	} else {
		d.Agent, d.agentErr = newAgent(d.Getenv, cfg)
	}
	d.When = app.NewWhenService(d.Judge)
	d.Intent = app.NewIntentService(d.Judge)
	d.Rerank = app.NewRerankService(d.Judge)
	d.Duplicate = app.NewDuplicateService(d.Judge)
	d.Reference = app.NewReferenceService(d.Judge)
	d.Checks = check.NewRegistry()
	checkbuiltins.RegisterAll(d.Checks, d.Judge)
	if backend != nil {
		d.Projects = app.NewProjectService(backend, d.Clock, d.IDs)
		d.Tasks = app.NewTaskService(backend, d.Clock, d.IDs)
		d.Actors = app.NewActorService(backend, d.Clock, d.IDs)
		d.Artifacts = app.NewArtifactService(backend, d.Clock, d.IDs)
		d.TaskChecks = app.NewCheckService(backend, d.Clock, d.IDs, d.Checks)
	}
	if d.EmbedderOverride != nil {
		d.Embedder = d.EmbedderOverride
	} else {
		d.Embedder = d.newEmbed(cfg)
	}
	if d.VectorsOverride != nil {
		d.Vectors = d.VectorsOverride
	} else {
		d.Vectors = d.openVectors(cfg)
	}
	d.Retriever = app.NewVectorRetriever(d.Embedder, d.Vectors, cfg.Embed.Options["model"])
}

// Close releases the vector side index and the storage backend.
func (d *Deps) Close() error {
	if d.vectorCloser != nil {
		_ = d.vectorCloser.Close()
		d.vectorCloser = nil
	}
	if d.Backend != nil {
		return d.Backend.Close()
	}
	return nil
}

// beginBounding wraps stdout in a bounded buffer when the output policy says so.
// Interactive sessions and --full/FACTOTUM_MAX_OUTPUT opt-outs leave stdout
// untouched, so their output stays byte-identical.
func (d *Deps) beginBounding() error {
	interactive := d.IsTerminal != nil && (d.IsTerminal(d.Out) || d.IsTerminal(d.Err))
	limit, bound, err := outputPolicy(d.Full, d.Getenv("FACTOTUM_MAX_OUTPUT"), interactive)
	if err != nil {
		return err
	}
	if !bound {
		return nil
	}
	d.output = newBoundedOutput(d.Out, d.OutputFormat, limit)
	d.Out = d.output
	return nil
}

// FlushOutput writes buffered stdout, truncating it when it exceeds the limit.
// It is idempotent, so both the success and error paths may call it.
func (d *Deps) FlushOutput() error {
	if d.output == nil {
		return nil
	}
	return d.output.Flush()
}

// newEmbed builds the embedder for the configured provider. A configured provider
// that cannot be used (unknown name, or command with no command) disables vector
// recall and records the reason, so warnEmbedMisconfig can surface it at the point
// of use instead of on every command.
func (d *Deps) newEmbed(cfg config.Config) embed.Embedder {
	if cfg.Embed.Provider == "" {
		return embed.Disabled{}
	}
	built, err := embed.New(cfg.Embed.Provider, d.Getenv, cfg.Embed.Options)
	if err != nil {
		d.embedErr = fmt.Errorf("embed provider %q is unknown (%w); vector recall disabled", cfg.Embed.Provider, err)
		return embed.Disabled{}
	}
	if !embedderUsable(built) {
		d.embedErr = fmt.Errorf("embed provider %q is not usable (check endpoint/command); vector recall disabled", cfg.Embed.Provider)
		return embed.Disabled{}
	}
	return built
}

// warnEmbedMisconfig reports a misconfigured embedding provider once, at the first
// vector operation. An unrelated command (ft version, ft task list) never touches
// the vector path, so it stays quiet.
func (d *Deps) warnEmbedMisconfig() {
	if d.embedErr == nil || d.embedWarned {
		return
	}
	d.embedWarned = true
	d.warnf("%v", d.embedErr)
}

// embedderUsable reports whether an embedder is configured and not the no-op
// Disabled implementation.
func embedderUsable(e embed.Embedder) bool {
	if e == nil {
		return false
	}
	_, disabled := e.(embed.Disabled)
	return !disabled
}

// openVectors opens the vector side index when vector recall is usable: an embedder
// is configured and a model is set. The index is a SQLite file beside the store
// file; without a store path (the memory backend) it is in-process and does not
// persist. Otherwise the index is nil, so vector recall stays off and no side-index
// file is created.
func (d *Deps) openVectors(cfg config.Config) vector.Index {
	if !embedderUsable(d.Embedder) || cfg.Embed.Options["model"] == "" {
		return nil
	}
	path := cfg.Store.Options["path"]
	if path == "" && cfg.Store.Backend == "sqlite" {
		path = storesqlite.DefaultPath
	}
	if path == "" || path == ":memory:" {
		return vector.NewMemory()
	}
	index, err := vsqlite.Open(path + ".vectors.db")
	if err != nil {
		d.warnf("vector index unavailable (%v); using lexical search", err)
		return nil
	}
	d.vectorCloser = index
	return index
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
// so it falls back to the lexical order; a real failure is reported as a warning. An
// empty query is never reranked: it lists everything in scope.
func (d *Deps) maybeRerank(cmd *cobra.Command, query string, artifacts []*core.Artifact, disabled bool) []*core.Artifact {
	if disabled || len(artifacts) < 2 || d.Rerank == nil || len(store.LexicalTerms(query)) == 0 {
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

// maybeRerankTasks reorders a lexical task shortlist by meaning when a judge is
// configured and rerank is not disabled. Like maybeRerank, it never fails a
// search: it falls back to lexical order and warns on a real failure, and it never
// reranks an empty query (which lists everything in scope).
func (d *Deps) maybeRerankTasks(cmd *cobra.Command, query string, tasks []*core.Task, disabled bool) []*core.Task {
	if disabled || len(tasks) < 2 || d.Rerank == nil || len(store.LexicalTerms(query)) == 0 {
		return tasks
	}
	ordered, err := d.Rerank.RerankTasks(cmd.Context(), query, tasks)
	if err == nil {
		return ordered
	}
	if !errors.Is(err, judge.ErrUnavailable) {
		_, _ = fmt.Fprintf(d.Err, "ft: warning: rerank unavailable (%v); using lexical order\n", err)
	}
	return tasks
}
