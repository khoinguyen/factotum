package cli

import (
	"github.com/spf13/cobra"
)

// configView is the effective, non-secret configuration `ft config get` reports.
// Secrets (the serve token) and machine-local paths (store options) are omitted.
type configView struct {
	Project      string `json:"project" yaml:"project"`
	DefaultActor string `json:"default_actor,omitempty" yaml:"default_actor,omitempty"`
	NoHints      bool   `json:"no_hints" yaml:"no_hints"`
	TechStack    string `json:"tech_stack,omitempty" yaml:"tech_stack,omitempty"`
	StoreBackend string `json:"store.backend" yaml:"store.backend"`
	RunSandbox   string `json:"run.sandbox,omitempty" yaml:"run.sandbox,omitempty"`
	RunHarness   string `json:"run.harness,omitempty" yaml:"run.harness,omitempty"`
}

// fields renders the view as ordered `key: value` lines, the text shape and the
// key set `ft config get <key>` accepts.
func (v configView) fields() []field {
	return []field{
		f("project", v.Project),
		f("default_actor", v.DefaultActor),
		f("no_hints", v.NoHints),
		f("tech_stack", v.TechStack),
		f("store.backend", v.StoreBackend),
		f("run.sandbox", v.RunSandbox),
		f("run.harness", v.RunHarness),
	}
}

func newConfigCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "config", Short: "Inspect the resolved configuration"}

	get := &cobra.Command{
		Use:   "get [key]",
		Short: "Show the effective configuration resolved for this directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			view := deps.configView()
			if len(args) == 1 {
				key := args[0]
				for _, entry := range view.fields() {
					if entry.Key == key {
						return deps.emit(map[string]string{key: entry.Value}, func() { deps.printFields(entry) })
					}
				}
				return usageError(cmd, "unknown config key %q", key)
			}
			return deps.emit(view, func() { deps.printFields(view.fields()...) })
		},
	}

	cmd.AddCommand(get)
	return cmd
}

func (d *Deps) configView() configView {
	cfg := d.Config
	return configView{
		Project:      cfg.Project,
		DefaultActor: cfg.DefaultActor,
		NoHints:      cfg.NoHints,
		TechStack:    cfg.TechStack,
		StoreBackend: cfg.Store.Backend,
		RunSandbox:   cfg.Run.Sandbox,
		RunHarness:   cfg.Run.Harness,
	}
}
