package cli

import (
	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/core"
)

// newSessionCommand records the lifecycle of a chief session: when it started
// and, on reincarnation, when and why it retired (naming its successor). The
// events carry the session id and the acting actor, so the sessions dashboard
// can derive a session's start, stop, and the work it drove.
func newSessionCommand(deps *Deps) *cobra.Command {
	cmd := &cobra.Command{Use: "session", Short: "Record chief session lifecycle"}

	var projectID, startSID string
	start := &cobra.Command{
		Use:   "start",
		Short: "Start a chief session (mints an id when none is given)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := deps.resolveProject(projectID)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			// start mints a fresh id unless one is given explicitly. It must
			// NOT reuse an inherited FACTOTUM_SESSION: loop-agent.sh launches a
			// reincarnated chief from a shell that already carries the outgoing
			// session, and every reincarnation must get a NEW id.
			id := startSID
			if id == "" {
				id = deps.Sessions.NewID()
			}
			if _, err := deps.Sessions.Start(cmd.Context(), project, id); err != nil {
				return err
			}
			return deps.emit(sessionResult{SessionID: id, Project: project, Started: true}, func() {
				deps.printFields(f("session_id", id), f("started", true), f("project", string(project)))
			})
		},
	}
	start.Flags().StringVarP(&projectID, "project", "p", "", "project id")
	start.Flags().StringVar(&startSID, "session", "", "session id (default: mint a new one)")

	var retireSID, successor string
	retire := &cobra.Command{
		Use:   "retire",
		Short: "Retire a chief session, naming its successor",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			project := deps.resolveProject(projectID)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			id := retireSID
			if id == "" {
				id = deps.Session
			}
			if _, err := deps.Sessions.Retire(cmd.Context(), project, id, successor); err != nil {
				return err
			}
			return deps.emit(sessionResult{SessionID: id, Project: project, Retired: true, Successor: successor}, func() {
				fields := []field{f("session_id", id), f("retired", true)}
				if successor != "" {
					fields = append(fields, f("successor", successor))
				}
				fields = append(fields, f("project", string(project)))
				deps.printFields(fields...)
			})
		},
	}
	retire.Flags().StringVarP(&projectID, "project", "p", "", "project id")
	retire.Flags().StringVar(&retireSID, "session", "", "session id (default: FACTOTUM_SESSION)")
	retire.Flags().StringVar(&successor, "successor", "", "the session id that replaces this one")

	var ackProject string
	ack := &cobra.Command{
		Use:   "ack",
		Short: "Acknowledge the session this ft invocation carries",
		Long: "Record that the session this ft invocation carries reached it. The id comes from the\n" +
			"environment (`FACTOTUM_SESSION`), never from an argument, so the acknowledgement proves the\n" +
			"session id was picked up rather than that the prompt named it. `ft groom --unattended`\n" +
			"launches its session with its own id and requires this acknowledgement: without it the run\n" +
			"cannot tell a session that created nothing from a child ft that attributed nothing to it,\n" +
			"and fails instead of passing silently.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if deps.Session == "" {
				return usageError(cmd, "session ack needs a session: set FACTOTUM_SESSION (or run it from a launched session)")
			}
			project := deps.resolveProject(ackProject)
			if err := requireProject(cmd, project); err != nil {
				return err
			}
			if _, err := deps.Sessions.Ack(cmd.Context(), project); err != nil {
				return err
			}
			return deps.emit(sessionResult{SessionID: deps.Session, Project: project, Acked: true}, func() {
				deps.printFields(f("session_id", deps.Session), f("acked", true), f("project", string(project)))
			})
		},
	}
	ack.Flags().StringVarP(&ackProject, "project", "p", "", "project id")

	cmd.AddCommand(start, retire, ack)
	return requireSubcommand(cmd)
}

type sessionResult struct {
	SessionID string         `json:"session_id" yaml:"session_id"`
	Project   core.ProjectID `json:"project" yaml:"project"`
	Started   bool           `json:"started,omitempty" yaml:"started,omitempty"`
	Retired   bool           `json:"retired,omitempty" yaml:"retired,omitempty"`
	Acked     bool           `json:"acked,omitempty" yaml:"acked,omitempty"`
	Successor string         `json:"successor,omitempty" yaml:"successor,omitempty"`
}
