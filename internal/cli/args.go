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

// requireOneOf ensures exactly one of the named flags is set, printing the
// command's help otherwise.
func requireOneOf(cmd *cobra.Command, names ...string) error {
	set := 0
	for _, name := range names {
		if cmd.Flags().Changed(name) {
			set++
		}
	}
	switch set {
	case 1:
		return nil
	case 0:
		return usageError(cmd, "one of %s is required", flagNames(names))
	default:
		return usageError(cmd, "only one of %s may be set", flagNames(names))
	}
}

func flagNames(names []string) string {
	prefixed := make([]string, 0, len(names))
	for _, name := range names {
		prefixed = append(prefixed, "--"+name)
	}
	return strings.Join(prefixed, " or ")
}

func usageError(cmd *cobra.Command, format string, args ...any) error {
	cmd.SetOut(cmd.ErrOrStderr())
	_ = cmd.Help()
	return fmt.Errorf("%w: %s", ErrUsage, fmt.Sprintf(format, args...))
}
