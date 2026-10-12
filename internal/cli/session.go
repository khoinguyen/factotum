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
			id := startSID
			if id == "" {
				id = deps.Session
			}
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
	start.Flags().StringVar(&startSID, "session", "", "session id (default: mint one)")

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
				deps.printFields(f("session_id", id), f("retired", true), f("successor", successor), f("project", string(project)))
			})
		},
	}
	retire.Flags().StringVarP(&projectID, "project", "p", "", "project id")
	retire.Flags().StringVar(&retireSID, "session", "", "session id (default: FACTOTUM_SESSION)")
	retire.Flags().StringVar(&successor, "successor", "", "the session id that replaces this one")

	cmd.AddCommand(start, retire)
	return cmd
}

type sessionResult struct {
	SessionID string         `json:"session_id" yaml:"session_id"`
	Project   core.ProjectID `json:"project" yaml:"project"`
	Started   bool           `json:"started,omitempty" yaml:"started,omitempty"`
	Retired   bool           `json:"retired,omitempty" yaml:"retired,omitempty"`
	Successor string         `json:"successor,omitempty" yaml:"successor,omitempty"`
}
