package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/feedback"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/version"
)

// defaultFeedbackStore is the project whose store receives feedback when no
// override is given: the Factotum project itself.
const defaultFeedbackStore = "factotum"

func newFeedbackCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "feedback",
		Short: "Report a bug or friction back to the Factotum maintainers",
	}
	cmd.AddCommand(newFeedbackCreateCommand(deps))
	return cmd
}

func newFeedbackCreateCommand(deps *Deps) *cobra.Command {
	var body, flow, repo, projectID, sinkProject, transportName string

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Record feedback into the Factotum database",
		Long: "Record one piece of feedback into the Factotum database, not the caller's\n" +
			"project store, so a bug or friction found while working on any project reaches\n" +
			"the maintainers.\n\n" +
			"Collected: the message, the command or flow involved, the ft version, and the\n" +
			"originating project and repository. Paths under $HOME and token-shaped strings\n" +
			"are redacted before the report is stored. The sink is the project named by\n" +
			"--feedback-store (default factotum), resolved from the machine config.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := requireFlags(cmd, "body"); err != nil {
				return err
			}
			if strings.TrimSpace(body) == "" {
				return usageError(cmd, "--body must not be empty")
			}
			report := feedback.Report{
				Message: body,
				Flow:    flow,
				Version: version.Version,
				Project: string(deps.resolveProject(projectID)),
				Repo:    repo,
			}.Sanitize(deps.Getenv("HOME"))

			task, err := deps.sendFeedback(cmd, sinkProject, transportName, report)
			if err != nil {
				return err
			}
			return deps.emit(task, func() {
				deps.printFields(deps.taskFields(task, f("created", true))...)
			})
		},
	}
	cmd.Flags().StringVarP(&body, "body", "b", "", "the feedback message (required)")
	cmd.Flags().StringVar(&flow, "flow", "", "the command or flow involved, e.g. \"ft task next\"")
	cmd.Flags().StringVarP(&repo, "repo", "r", "", "originating repository within the project")
	cmd.Flags().StringVarP(&projectID, "project", "p", "", "originating project (defaults to the configured project)")
	cmd.Flags().StringVar(&sinkProject, "feedback-store", defaultFeedbackStore, "project whose store receives feedback")
	cmd.Flags().StringVar(&transportName, "transport", feedback.DefaultTransport, "sink transport")
	return cmd
}

// sendFeedback resolves the sink store, opens it, builds the selected transport,
// and delivers the report. The caller's own backend is never used as the sink.
func (d *Deps) sendFeedback(cmd *cobra.Command, sinkProject, transportName string, report feedback.Report) (*core.Task, error) {
	storeCfg, ok, err := config.StoreFor(d.UserConfigPath, sinkProject)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf(
			"no feedback store configured for project %q: add [projects.%s] to the machine config or pass --feedback-store",
			sinkProject, sinkProject)
	}
	factory, err := d.StoreFactories.MustLookup(storeCfg.Backend)
	if err != nil {
		return nil, fmt.Errorf("feedback store %q: %w", sinkProject, err)
	}
	backend, err := factory(cmd.Context(), store.Config{
		Backend: storeCfg.Backend,
		Options: storeCfg.Options,
		Noticef: func(format string, args ...any) {
			_, _ = fmt.Fprintf(d.Err, "ft: "+format+"\n", args...)
		},
	})
	if err != nil {
		return nil, fmt.Errorf("open feedback store %q: %w", sinkProject, err)
	}
	defer func() { _ = backend.Close() }()

	build, err := d.FeedbackTransports.MustLookup(transportName)
	if err != nil {
		return nil, fmt.Errorf("feedback transport: %w", err)
	}
	transport, err := build(feedback.Env{
		Backend: backend,
		Project: core.ProjectID(sinkProject),
		Clock:   d.Clock,
		IDs:     d.IDs,
	})
	if err != nil {
		return nil, err
	}
	return transport.Send(cmd.Context(), report)
}
