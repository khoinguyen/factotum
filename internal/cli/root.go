package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/version"
)

func builtinCommands() *registry.Registry[CommandFactory] {
	reg := registry.New[CommandFactory]()
	factories := map[string]CommandFactory{
		"actor":     newActorCommand,
		"bug":       newBugCommand,
		"config":    newConfigCommand,
		"doc":       newDocCommand,
		"doctor":    newDoctorCommand,
		"event":     newEventCommand,
		"feedback":  newFeedbackCommand,
		"graph":     newGraphCommand,
		"groom":     newGroomCommand,
		"idea":      newIdeaCommand,
		"init":      newInitCommand,
		"memory":    newMemoryCommand,
		"milestone": newMilestoneCommand,
		"msg":       newMessageCommand,
		"project":   newProjectCommand,
		"prompt":    newPromptCommand,
		"qa":        newQACommand,
		"run":       newRunCommand,
		"serve":     newServeCommand,
		"session":   newSessionCommand,
		"skill":     newSkillCommand,
		"task":      newTaskCommand,
		"up":        newUpCommand,
		"start":     statusShortcut("start", core.StatusInProgress, "Mark a task in progress"),
		"review":    statusShortcut("review", core.StatusReadyForReview, "Mark a task ready for review"),
		"done":      statusShortcut("done", core.StatusDone, "Mark a task done"),
		"reopen":    statusShortcut("reopen", core.StatusTodo, "Return a task to todo"),
		"block":     statusShortcut("block", core.StatusBlocked, "Mark a task blocked"),
		"cancel":    statusShortcut("cancel", core.StatusCancelled, "Cancel a task"),
	}
	for name, factory := range factories {
		if err := reg.Register(name, factory); err != nil {
			panic(err)
		}
	}
	return reg
}

// annotationNoCallerStore marks a command whose work never reads or writes the
// caller's project store, so it must run even when that store is misconfigured
// or absent (for example ft feedback, which targets the sink database).
const annotationNoCallerStore = "ft/no-caller-store"

// needsCallerStore reports whether cmd, or an ancestor, requires the caller's
// project store to be opened before it runs. It defaults to true, so only a
// command that opts out via annotation is exempt.
func needsCallerStore(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Annotations[annotationNoCallerStore] == "true" {
			return false
		}
	}
	return true
}

func NewRoot(deps *Deps) *cobra.Command {
	var configPath, userConfigPath, storeBackend, actorRef, output string
	var storeOpts []string
	var noHints, full bool

	root := &cobra.Command{
		Use:           "ft",
		Short:         "Manage projects and the human/agent task graph",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			projectPath := configPath
			if projectPath == "" {
				projectPath = config.DefaultPath
			}
			userPath := userConfigPath
			if userPath == "" {
				userPath = config.UserPath(deps.Getenv)
			}
			cfg, err := config.Load(config.Input{
				UserPath:    userPath,
				ProjectPath: projectPath,
				Getenv:      deps.Getenv,
			})
			if err != nil {
				return err
			}
			deps.UserConfigPath = userPath
			deps.ProjectConfigPath = projectPath
			if storeBackend != "" {
				cfg.Store.Backend = storeBackend
			}
			if cfg.Store.Options == nil {
				cfg.Store.Options = map[string]string{}
			}
			for _, opt := range storeOpts {
				key, value, ok := strings.Cut(opt, "=")
				if !ok {
					return fmt.Errorf("invalid --store-opt %q, want key=value", opt)
				}
				cfg.Store.Options[key] = value
			}
			// Caller precedence: --actor > FACTOTUM_CALLER > FACTOTUM_DEFAULT_ACTOR >
			// project/user default_actor. FACTOTUM_DEFAULT_ACTOR is already folded
			// into cfg.DefaultActor; FACTOTUM_CALLER stays separate until it can be
			// checked against the store's actors (bindCaller, after the store opens).
			// Each is normalized so the canonical actor:<id> address and the bare id
			// name the same actor, and an unresolvable reference is rejected here
			// rather than attributed verbatim. bindCaller checks both an --actor and
			// a configured default against the store's actors, so a typo in either
			// never becomes an attribution or a reminder target.
			switch {
			case actorRef != "":
				ref, err := core.ParseActorRef(actorRef)
				if err != nil {
					return err
				}
				deps.ActorRef = string(ref)
				deps.ActorFlag = true
			case cfg.Caller != "":
				ref, err := core.ParseActorRef(cfg.Caller)
				if err != nil {
					return err
				}
				deps.Caller = string(ref)
			case deps.ActorRef == "" && cfg.DefaultActor != "":
				ref, err := core.ParseActorRef(cfg.DefaultActor)
				if err != nil {
					return err
				}
				deps.ActorRef = string(ref)
			}
			// FACTOTUM_SESSION is exported by loop-agent.sh so every ft call an
			// agent makes is attributed to the chief session it inherited, and
			// by a groom run so the tasks a session creates are its own.
			if session := deps.Getenv(harnesspkg.EnvSession); session != "" {
				deps.Session = session
			}
			deps.OutputFormat = output
			switch output {
			case "", "text", "json", "yaml":
			default:
				return fmt.Errorf("unknown output format %q, want text, json, or yaml", output)
			}
			deps.Full = full
			if err := deps.beginBounding(); err != nil {
				return err
			}
			if noHints {
				cfg.NoHints = true
			}
			deps.NoHints = cfg.NoHints
			if err := cfg.Validate(); err != nil {
				return err
			}

			// A flag value the command can reject is checked before any store is
			// opened, so an invocation that can never run leaves no database, lock,
			// migration, or vector side index behind.
			if err := deps.runFlagChecks(cmd); err != nil {
				return err
			}
			if !needsCallerStore(cmd) {
				deps.Attach(cfg, nil)
				deps.bindCaller(cmd)
				return nil
			}
			factory, err := deps.StoreFactories.MustLookup(cfg.Store.Backend)
			if err != nil {
				return err
			}
			backend, err := factory(cmd.Context(), store.Config{
				Backend: cfg.Store.Backend,
				Options: cfg.Store.Options,
				Noticef: func(format string, args ...any) {
					_, _ = fmt.Fprintf(deps.Err, "ft: "+format+"\n", args...)
				},
			})
			if err != nil {
				return err
			}
			deps.Attach(cfg, backend)
			deps.bindCaller(cmd)
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			if err := deps.FlushOutput(); err != nil {
				return err
			}
			return deps.Close()
		},
	}

	root.PersistentFlags().StringVarP(&configPath, "config", "c", "", "project config file (default .factotum/config.toml)")
	root.PersistentFlags().StringVar(&userConfigPath, "user-config", "", "machine config file (default $HOME/.factotum/config.toml)")
	root.PersistentFlags().StringVar(&storeBackend, "store", "", "storage backend (overrides config)")
	root.PersistentFlags().StringArrayVar(&storeOpts, "store-opt", nil, "backend option key=value (repeatable)")
	root.PersistentFlags().StringVar(&actorRef, "actor", "", "actor attributed to mutations (id, actor:<id>, or name)")
	root.PersistentFlags().StringVarP(&output, "output", "o", "text", "output format: text, json, or yaml")
	root.PersistentFlags().BoolVar(&noHints, "no-hints", false, "suppress next-step command suggestions")
	root.PersistentFlags().BoolVar(&full, "full", false, "print full output, even when it is large")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
		// version never reads the caller's store, so it must not open (and thus
		// migrate) one: a stray branch build reporting its version must leave a
		// shared database untouched.
		Annotations: map[string]string{annotationNoCallerStore: "true"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			deps.printf("%s\n", version.Version)
			return nil
		},
	})

	for _, name := range deps.Commands.Names() {
		factory, _ := deps.Commands.Lookup(name)
		root.AddCommand(factory(deps))
	}
	return root
}

// bindCaller resolves the detected caller against the opened store's actors,
// then records the resulting actor and session on the command context so every
// event this invocation appends is attributed. An unregistered caller is dropped
// with a warning, never auto-created, and never fails the command. An explicit
// --actor or configured default actor that names no Actor record takes the same
// treatment, so a typo can neither stamp events nor become the auto-QA reminder
// target. A loop role is the one exception: it is a mailbox a session registers
// as a run, not a record, so it stays attributed — under each path's own role
// grammar (app.LoopRole for --actor, app.LoopDefaultRole for a configured
// default, which the loop also exports for the grooming and architecture roles).
// A command that opens no caller store cannot check registration, so it stays
// quiet. A dropped reference is recorded in ActorDropped, so a command that
// installs a message receiver (ft run) can say it will launch without one.
func (d *Deps) bindCaller(cmd *cobra.Command) {
	if d.Actors != nil {
		switch {
		case d.Caller != "":
			actor, err := d.Actors.Resolve(cmd.Context(), d.Caller)
			switch {
			case err == nil:
				d.ActorRef = string(actor.ID)
			case errors.Is(err, core.ErrNotFound):
				d.warnf("caller %q is not a registered actor; this invocation is unattributed", d.Caller)
				d.ActorDropped = d.Caller
				d.ActorRef = ""
			default:
				d.warnf("could not resolve caller %q (%v); this invocation is unattributed", d.Caller, err)
				d.ActorDropped = d.Caller
				d.ActorRef = ""
			}
		case d.ActorRef != "":
			d.bindActor(cmd.Context())
		default:
			d.warnCallerHint()
		}
	}
	cmd.SetContext(app.WithCaller(cmd.Context(), core.ActorID(d.ActorRef), d.Session))
}

// warnCallerHint surfaces a best-effort guess at the agent CLI this invocation
// runs under, without ever attributing it. It runs only when no identity was
// named at all: an explicit --actor, FACTOTUM_CALLER, or configured default
// keeps its own path (including the drop warning for an unregistered one), so a
// guess can never contradict a user-supplied identity. The warning says the
// guess is unverified and the invocation stays unattributed, and nothing is
// recorded - no event, note author, assignee, or reminder target carries it.
func (d *Deps) warnCallerHint() {
	if d.CallerHint == nil {
		return
	}
	hint := d.CallerHint()
	if hint == nil || hint.Agent == "" {
		return
	}
	evidence := hint.Evidence
	if hint.Terminal {
		evidence += "; stdin is a terminal"
	}
	d.warnf("probable agent caller %q (unverified heuristic: %s); this invocation stays unattributed - set FACTOTUM_CALLER or --actor to name the actor", hint.Agent, evidence)
}

// bindActor resolves the actor this invocation names — an explicit --actor or a
// configured default — against the store's actors, dropping it with a warning
// when it names no record and is not a role its own path trusts.
func (d *Deps) bindActor(ctx context.Context) {
	source, role := "default actor", app.LoopDefaultRole
	if d.ActorFlag {
		source, role = "actor", app.LoopRole
	}
	actor, err := d.Actors.Resolve(ctx, d.ActorRef)
	switch {
	case err == nil:
		d.ActorRef = string(actor.ID)
	case !errors.Is(err, core.ErrNotFound):
		d.warnf("could not resolve %s %q (%v); this invocation is unattributed", source, d.ActorRef, err)
		d.ActorDropped = d.ActorRef
		d.ActorRef = ""
	default:
		if _, ok := role(d.ActorRef); !ok {
			d.warnf("%s %q is not a registered actor; this invocation is unattributed", source, d.ActorRef)
			d.ActorDropped = d.ActorRef
			d.ActorRef = ""
		}
	}
}

// resolveActorRef resolves a user-supplied actor reference by the identity rule
// every actor-valued flag follows: a bare id or name, the canonical
// actor:<id> address, and a colon-bearing name all reach the same Actor record,
// and an unregistered id that names a loop role resolves as the agent identity
// it acts as. A malformed or doubled address is rejected before it is looked up;
// a lookalike that is neither a record nor a role stays an unknown actor.
func (d *Deps) resolveActorRef(ctx context.Context, ref string) (*core.Actor, error) {
	id, err := core.ParseActorRef(ref)
	if err != nil {
		return nil, err
	}
	actor, err := d.Actors.Resolve(ctx, string(id))
	if err == nil || !errors.Is(err, core.ErrNotFound) {
		return actor, err
	}
	if identity, ok := app.RoleIdentity(id); ok {
		return &identity, nil
	}
	return nil, err
}

func (d *Deps) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.Out, format, args...)
}

// resolveProject returns flag when set, else the configured default project,
// or "" when neither is available.
func (d *Deps) resolveProject(flag string) core.ProjectID {
	if flag != "" {
		return core.ProjectID(flag)
	}
	return core.ProjectID(d.Config.Project)
}

// printTable writes a space-aligned table (header + rows) using tab stops. The
// output is buffered because tabwriter writes once per cell: without it a large
// table turns into hundreds of thousands of small writes, one syscall each.
func (d *Deps) printTable(header []string, rows [][]string) {
	buffered := bufio.NewWriter(d.Out)
	writer := tabwriter.NewWriter(buffered, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, strings.Join(header, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(writer, strings.Join(row, "\t"))
	}
	_ = writer.Flush()
	_ = buffered.Flush()
}

func (d *Deps) printJSON(value any) error { return writeJSON(d.Out, value) }

func (d *Deps) printYAML(value any) error { return writeYAML(d.Out, value) }

// writeJSON writes value as indented json. It is a free function so a command
// whose output the byte guard measures can render into a buffer and get the
// same bytes a caller receives.
func writeJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeYAML(w io.Writer, value any) error {
	encoder := yaml.NewEncoder(w)
	encoder.SetIndent(2)
	defer func() { _ = encoder.Close() }()
	return encoder.Encode(value)
}

func (d *Deps) structured() bool {
	return d.OutputFormat == "json" || d.OutputFormat == "yaml"
}

func (d *Deps) emit(value any, text func(), hints ...hint) error {
	switch d.OutputFormat {
	case "json":
		return d.printJSON(value)
	case "yaml":
		return d.printYAML(value)
	default:
		text()
		d.suggest(hints...)
		return nil
	}
}
