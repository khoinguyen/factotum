package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// ErrUsage marks a validation error whose help has already been printed. The
// entrypoint exits non-zero without echoing a terse error line.
var ErrUsage = errors.New("usage error")

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
	cmd.SetOut(cmd.ErrOrStderr())
	_ = cmd.Help()
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))
}
