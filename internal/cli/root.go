package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/version"
)

func builtinCommands() *registry.Registry[CommandFactory] {
	reg := registry.New[CommandFactory]()
	factories := map[string]CommandFactory{
		"actor":     newActorCommand,
		"doc":       newDocCommand,
		"event":     newEventCommand,
		"graph":     newGraphCommand,
		"milestone": newMilestoneCommand,
		"project":   newProjectCommand,
		"task":      newTaskCommand,
	}
	for name, factory := range factories {
		if err := reg.Register(name, factory); err != nil {
			panic(err)
		}
	}
	return reg
}

func NewRoot(deps *Deps) *cobra.Command {
	var configPath, userConfigPath, storeBackend, actorRef, output string
	var storeOpts []string
	var noHints bool

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
			if actorRef != "" {
				deps.ActorRef = actorRef
			} else if deps.ActorRef == "" {
				deps.ActorRef = cfg.DefaultActor
			}
			deps.OutputJSON = output == "json"
			if noHints {
				cfg.NoHints = true
			}
			deps.NoHints = cfg.NoHints
			if err := cfg.Validate(); err != nil {
				return err
			}

			factory, err := deps.StoreFactories.MustLookup(cfg.Store.Backend)
			if err != nil {
				return err
			}
			backend, err := factory(cmd.Context(), store.Config{Backend: cfg.Store.Backend, Options: cfg.Store.Options})
			if err != nil {
				return err
			}
			deps.Attach(cfg, backend)
			return nil
		},
		PersistentPostRunE: func(cmd *cobra.Command, _ []string) error {
			if deps.Backend != nil {
				return deps.Backend.Close()
			}
			return nil
		},
	}

	root.PersistentFlags().StringVarP(&configPath, "config", "c", "", "project config file (default .factotum/config.toml)")
	root.PersistentFlags().StringVar(&userConfigPath, "user-config", "", "machine config file (default $HOME/.factotum/config.toml)")
	root.PersistentFlags().StringVar(&storeBackend, "store", "", "storage backend (overrides config)")
	root.PersistentFlags().StringArrayVar(&storeOpts, "store-opt", nil, "backend option key=value (repeatable)")
	root.PersistentFlags().StringVar(&actorRef, "actor", "", "actor attributed to mutations (id or name)")
	root.PersistentFlags().StringVarP(&output, "output", "o", "text", "output format: text or json")
	root.PersistentFlags().BoolVar(&noHints, "no-hints", false, "suppress next-step command suggestions")

	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print the version",
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

func (d *Deps) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(d.Out, format, args...)
}

// printTable writes a space-aligned table (header + rows) using tab stops.
func (d *Deps) printTable(header []string, rows [][]string) {
	writer := tabwriter.NewWriter(d.Out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, strings.Join(header, "\t"))
	for _, row := range rows {
		_, _ = fmt.Fprintln(writer, strings.Join(row, "\t"))
	}
	_ = writer.Flush()
}

func (d *Deps) printJSON(value any) error {
	encoder := json.NewEncoder(d.Out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func (d *Deps) emit(value any, text func(), hints ...hint) error {
	if d.OutputJSON {
		return d.printJSON(value)
	}
	text()
	d.suggest(hints...)
	return nil
}
