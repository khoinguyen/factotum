package cli

import "github.com/spf13/cobra"

// newIdeaCommand is the user-facing idea surface: a PO captures and manages
// ideas here, over the same TicketService storage the task tree uses. It is the
// generic capture surface specialized to the idea kind (refined with `promote`).
func newIdeaCommand(deps *Deps) *cobra.Command {
	return newCaptureCommand(deps, ideaCapture)
}
