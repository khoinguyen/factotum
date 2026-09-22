package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/doctor"
	"github.com/khoinguyen/factotum/pkg/embed"
	"github.com/khoinguyen/factotum/pkg/embed/transport"
	"github.com/khoinguyen/factotum/pkg/judge"
	"github.com/khoinguyen/factotum/pkg/store"
)

// errDoctorFailed marks a diagnosis with a failing (or, with --strict, warning)
// check, so the process exits non-zero and a script can gate on it.
var errDoctorFailed = errors.New("doctor found problems")

func newDoctorCommand(deps *Deps) *cobra.Command {
	var fix, strict bool
	var projectID string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose optional subsystems (embedder, judge) and recommend fixes",
		Long: "Diagnose the optional subsystems that fail softly: an embedding provider that\n" +
			"cannot be reached, a model that is missing, a vector index left on an old model,\n" +
			"or a judge without a key. Each check reports what it observed and the concrete\n" +
			"fix. Doctor is read-only unless --fix is passed, and it never starts a process or\n" +
			"downloads anything on its own.",
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := string(deps.resolveProject(projectID))
			report, err := deps.diagnose(cmd.Context(), project)
			if err != nil {
				return err
			}
			applied, err := deps.applyDoctorFixes(cmd, report, fix)
			if err != nil {
				return err
			}
			if applied {
				if report, err = deps.diagnose(cmd.Context(), project); err != nil {
					return err
				}
			}
			if err := deps.emitDoctor(report); err != nil {
				return err
			}
			if report.Failed() || (strict && report.Warned()) {
				return fmt.Errorf("%w: %s", errDoctorFailed, doctorSummary(report))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "apply the recommended fixes without prompting")
	cmd.Flags().BoolVar(&strict, "strict", false, "exit non-zero on warnings too")
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "project id for the memory vector index check")
	return cmd
}

// diagnose gathers the configuration and the project's vector state and runs the
// read-only checks. It is also the re-diagnosis after --fix, so it is idempotent.
func (d *Deps) diagnose(ctx context.Context, project string) (doctor.Report, error) {
	in := doctor.Input{
		Embed: doctor.Embed{
			Provider: d.Config.Embed.Provider,
			Known:    embedProviderKnown(d.Getenv, d.Config),
			Endpoint: d.Config.Embed.Options["endpoint"],
			Model:    d.Config.Embed.Options["model"],
			Command:  d.Config.Embed.Options["command"],
			APIKey:   d.Config.Embed.Options["api_key"],
		},
		Judge: doctor.Judge{
			Provider: d.Config.Judge.Provider,
			Known:    judgeProviderKnown(d.Getenv, d.Config),
			KeySet:   d.judgeConfigured(),
		},
		Project: project,
		Probe:   d.doctorProbe(),
	}
	if in.Project != "" && d.Artifacts != nil {
		kind := core.ArtifactMemory
		memories, err := d.Artifacts.List(ctx, store.ArtifactFilter{ProjectID: core.ProjectID(in.Project), Kind: &kind})
		if err != nil {
			return doctor.Report{}, err
		}
		in.MemoryCount = len(memories)
		if d.Vectors != nil && len(memories) > 0 {
			ids := make([]string, 0, len(memories))
			for _, memory := range memories {
				ids = append(ids, string(memory.ID))
			}
			models, err := d.Vectors.Models(ctx, ids)
			if err != nil {
				return doctor.Report{}, err
			}
			in.IndexModels = models
		}
	}
	return doctor.Diagnose(ctx, in), nil
}

// doctorProbe returns the configured prober, or the real transport prober.
func (d *Deps) doctorProbe() doctor.Prober {
	if d.DoctorProbe != nil {
		return d.DoctorProbe
	}
	return transport.NewProbe(nil)
}

// applyDoctorFixes offers each actionable check: with --fix it runs them all, on a
// terminal it asks first, and otherwise it does nothing. It reports whether any fix
// ran, so the caller can re-diagnose.
func (d *Deps) applyDoctorFixes(cmd *cobra.Command, report doctor.Report, fix bool) (bool, error) {
	interactive := d.IsTerminal != nil && d.IsTerminal(d.Out)
	applied := false
	for _, check := range report.Checks {
		if check.Action == nil {
			continue
		}
		if !fix {
			if !interactive {
				continue
			}
			ok, err := d.confirmDoctorFix(cmd, check)
			if err != nil {
				return applied, err
			}
			if !ok {
				continue
			}
		}
		if err := d.applyDoctorFix(cmd.Context(), check); err != nil {
			return applied, err
		}
		applied = true
	}
	return applied, nil
}

// confirmDoctorFix asks before applying a fix. The prompt goes to stderr so stdout
// stays the report.
func (d *Deps) confirmDoctorFix(cmd *cobra.Command, check doctor.Check) (bool, error) {
	_, _ = fmt.Fprintf(d.Err, "ft: %s: %s\napply fix? [y/N] ", check.Name, check.Action.Description)
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func (d *Deps) applyDoctorFix(ctx context.Context, check doctor.Check) error {
	switch check.Action.Kind {
	case doctor.ActionCommand:
		run := d.DoctorFixRunner
		if run == nil {
			run = runDoctorCommand
		}
		_, _ = fmt.Fprintf(d.Err, "ft: %s: running %s\n", check.Name, strings.Join(check.Action.Argv, " "))
		if err := run(ctx, check.Action.Argv, d.Err); err != nil {
			return fmt.Errorf("fix %s: %w", check.Name, err)
		}
		return nil
	case doctor.ActionReindex:
		return d.reindexDoctor(ctx, check.Action.Project)
	default:
		return fmt.Errorf("fix %s: unknown action %q", check.Name, check.Action.Kind)
	}
}

// runDoctorCommand runs a provider fix (for example `ollama pull`) directly, with
// no shell, so a config-derived model name cannot inject arguments.
func runDoctorCommand(ctx context.Context, argv []string, w io.Writer) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty fix command")
	}
	process := exec.CommandContext(ctx, argv[0], argv[1:]...)
	process.Stdout = w
	process.Stderr = w
	return process.Run()
}

// reindexDoctor re-embeds a project's memory, the fix for a model change or an
// unindexed project.
func (d *Deps) reindexDoctor(ctx context.Context, project string) error {
	if d.Retriever == nil || !d.Retriever.Enabled() {
		return fmt.Errorf("reindex %s: no usable embedding provider", project)
	}
	kind := core.ArtifactMemory
	memories, err := d.Artifacts.List(ctx, store.ArtifactFilter{ProjectID: core.ProjectID(project), Kind: &kind})
	if err != nil {
		return err
	}
	count, err := d.Retriever.Reindex(ctx, memories)
	if err != nil {
		return fmt.Errorf("reindex %s: %w", project, err)
	}
	_, _ = fmt.Fprintf(d.Err, "ft: reindexed %d memories for %s\n", count, project)
	return nil
}

func (d *Deps) emitDoctor(report doctor.Report) error {
	text := func() {
		for _, check := range report.Checks {
			d.printf("%s: %s - %s\n", check.Name, check.Status, check.Summary)
			if check.Recommendation != "" {
				d.printf("  recommend: %s\n", check.Recommendation)
			}
			if check.Action != nil {
				d.printf("  fix: %s\n", doctorActionCommand(check.Action))
			}
		}
		d.printf("overall: %s\n", report.Overall)
	}
	return d.emit(report, text, d.doctorHints(report)...)
}

// doctorHints offers --fix when the report still has an unapplied action.
func (d *Deps) doctorHints(report doctor.Report) []hint {
	for _, check := range report.Checks {
		if check.Action != nil {
			return []hint{{Command: "ft doctor --fix", About: "apply the recommended fixes"}}
		}
	}
	return nil
}

// doctorActionCommand renders the command a fix would run.
func doctorActionCommand(action *doctor.Action) string {
	if action.Kind == doctor.ActionReindex {
		return fmt.Sprintf("ft memory reindex -p %s", action.Project)
	}
	return strings.Join(action.Argv, " ")
}

func doctorSummary(report doctor.Report) string {
	failed, warned := 0, 0
	for _, check := range report.Checks {
		switch check.Status {
		case doctor.StatusFail:
			failed++
		case doctor.StatusWarn:
			warned++
		}
	}
	return fmt.Sprintf("%d failed, %d warned", failed, warned)
}

// embedProviderKnown reports whether the configured embed provider is registered.
// An empty provider is treated as known: "not configured" is a separate check.
func embedProviderKnown(getenv func(string) string, cfg config.Config) bool {
	if cfg.Embed.Provider == "" {
		return true
	}
	_, err := embed.New(cfg.Embed.Provider, getenv, cfg.Embed.Options)
	return err == nil
}

// judgeProviderKnown reports whether the configured judge provider is registered.
func judgeProviderKnown(getenv func(string) string, cfg config.Config) bool {
	provider := cfg.Judge.Provider
	if provider == "" {
		provider = "typesafe"
	}
	_, err := judge.New(provider, getenv, cfg.Judge.Options)
	return err == nil
}

// judgeConfigured reports whether the built judge has a backend and is not the
// no-op Disabled, which is what "a key is set" amounts to.
func (d *Deps) judgeConfigured() bool {
	if d.Judge == nil {
		return false
	}
	_, disabled := d.Judge.(judge.Disabled)
	return !disabled
}
