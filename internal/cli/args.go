package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/core"
)

// ErrUsage marks a validation error whose help has already been printed. The
// entrypoint exits non-zero after the reason and help have gone to stderr.
var ErrUsage = errors.New("usage error")

// usageFailure carries the human-readable reason for a usage error while still
// matching ErrUsage, so the reason can be printed without the sentinel prefix.
type usageFailure struct{ reason string }

func (u usageFailure) Error() string { return u.reason }

func (u usageFailure) Is(target error) bool { return target == ErrUsage }

// exactArgs validates the argument count and, on mismatch, prints the command's
// help instead of a terse "accepts N arg(s)" message.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == n {
			return nil
		}
		return usageError(cmd, "expected %d argument(s), got %d", n, len(args))
	}
}

// requireFlags ensures the named flags were set, printing the command's help
// when any is missing.
func requireFlags(cmd *cobra.Command, names ...string) error {
	var missing []string
	for _, name := range names {
		if !cmd.Flags().Changed(name) {
			missing = append(missing, "--"+name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return usageError(cmd, "%s required", strings.Join(missing, ", "))
}

func usageError(cmd *cobra.Command, format string, args ...any) error {
	reason := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintln(cmd.ErrOrStderr(), "ft:", reason)
	cmd.SetOut(cmd.ErrOrStderr())
	_ = cmd.Help()
	return usageFailure{reason: reason}
}

// requireProject reports a usage error when no project could be resolved from
// the flag or the configured default.
func requireProject(cmd *cobra.Command, project core.ProjectID) error {
	if project == "" {
		return usageError(cmd, "--project is required, or set a default project")
	}
	return nil
}
