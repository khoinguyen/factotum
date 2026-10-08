package cli

import "github.com/spf13/cobra"

// newBugCommand is the user-facing bug surface: a human captures and manages
// defects here, over the same TicketService storage the task tree uses. It is
// the generic capture surface specialized to the bug kind (refined with
// `triage`, the defect analogue of grooming).
func newBugCommand(deps *Deps) *cobra.Command {
	return newCaptureCommand(deps, bugCapture)
}
