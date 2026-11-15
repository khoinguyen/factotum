package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
)

// commandGroups is the audited set of command groups that take no action of
// their own: every one of them only holds subcommands. A bare or misspelled
// invocation must be a usage error, never a silent success. The root `ft` is
// deliberately absent: `ft` on its own prints help and exits 0.
var commandGroups = [][]string{
	{"actor"},
	{"bug"},
	{"config"},
	{"doc"},
	{"event"},
	{"feedback"},
	{"graph"},
	{"idea"},
	{"memory"},
	{"milestone"},
	{"msg"},
	{"msg", "agent"},
	{"project"},
	{"project", "repo"},
	{"qa"},
	{"session"},
	{"skill"},
	{"task"},
	{"task", "dep"},
	{"task", "note"},
}

// usageReason returns the "ft: ..." reason line a usage error writes to stderr.
func usageReason(t *testing.T, stderr string) string {
	t.Helper()
	for _, line := range strings.Split(stderr, "\n") {
		if strings.HasPrefix(line, "ft: ") {
			return line
		}
	}
	t.Fatalf("stderr carries no usage reason:\n%s", stderr)
	return ""
}

// advertisedSubcommands returns the subcommand names the group's help shows,
// excluding hidden commands such as the `ft msg agent` receiver protocol.
func advertisedSubcommands(t *testing.T, path []string) []string {
	t.Helper()
	group := findCommand(t, path)
	var names []string
	for _, sub := range group.Commands() {
		if sub.IsAvailableCommand() {
			names = append(names, sub.Name())
		}
	}
	if len(names) == 0 {
		t.Fatalf("ft %s advertises no subcommands", strings.Join(path, " "))
	}
	return names
}

func findCommand(t *testing.T, path []string) *cobra.Command {
	t.Helper()
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, func(string) string { return "" })
	cmd := NewRoot(deps)
	for i, name := range path {
		next := findSubcommand(cmd, name)
		if next == nil {
			t.Fatalf("ft %s: no subcommand %q", strings.Join(path[:i], " "), name)
		}
		cmd = next
	}
	return cmd
}

func findSubcommand(cmd *cobra.Command, name string) *cobra.Command {
	for _, sub := range cmd.Commands() {
		if sub.Name() == name {
			return sub
		}
	}
	return nil
}

// TestCommandGroupsRejectMissingSubcommand pins the group behaviour: invoking
// a group with no subcommand used to print its help to stdout and exit 0, so a
// script whose subcommand variable was empty believed the work happened. It must
// be a usage error (exit 2) naming the expected subcommands, with the help on
// stderr and nothing on stdout.
func TestCommandGroupsRejectMissingSubcommand(t *testing.T) {
	for _, group := range commandGroups {
		t.Run(strings.Join(group, " "), func(t *testing.T) {
			r := newRunner(t)
			stdout, stderr, err := r.runSplitErr(group...)

			if got := ExitCode(err); got != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2\nstderr:\n%s", err, got, stderr)
			}
			reason := usageReason(t, stderr)
			if !strings.HasPrefix(reason, "ft: expected a subcommand") {
				t.Fatalf("reason = %q, want it to name the missing subcommand", reason)
			}
			for _, name := range advertisedSubcommands(t, group) {
				if !strings.Contains(reason, name) {
					t.Errorf("reason %q does not name subcommand %q", reason, name)
				}
			}
			if !strings.Contains(stderr, "Usage:") || !strings.Contains(stderr, "ft "+strings.Join(group, " ")) {
				t.Fatalf("help must stay on stderr:\n%s", stderr)
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("stdout must be empty, stdout =\n%s", stdout)
			}
		})
	}
}

// TestGroupReasonHidesHiddenSubcommands pins the flip side of "names every
// advertised subcommand": a hidden child is not advertised. `ft msg` holds the
// hidden `agent` receiver protocol, so its reason must not mention it.
func TestGroupReasonHidesHiddenSubcommands(t *testing.T) {
	r := newRunner(t)
	_, stderr, err := r.runSplitErr("msg")
	if got := ExitCode(err); got != 2 {
		t.Fatalf("ExitCode(%v) = %d, want 2\nstderr:\n%s", err, got, stderr)
	}
	reason := usageReason(t, stderr)
	if strings.Contains(reason, "agent") {
		t.Errorf("reason advertises the hidden agent group: %q", reason)
	}
	for _, name := range []string{"get", "send"} {
		if !strings.Contains(reason, name) {
			t.Errorf("reason %q does not name the visible subcommand %q", reason, name)
		}
	}
}

// TestCommandGroupsRejectUnknownSubcommand covers the other half of the
// fall-through: a misspelled subcommand was treated as a group invocation and
// exited 0.
func TestCommandGroupsRejectUnknownSubcommand(t *testing.T) {
	for _, group := range commandGroups {
		t.Run(strings.Join(group, " "), func(t *testing.T) {
			r := newRunner(t)
			stdout, stderr, err := r.runSplitErr(append(append([]string{}, group...), "bogus")...)

			if got := ExitCode(err); got != 2 {
				t.Fatalf("ExitCode(%v) = %d, want 2\nstderr:\n%s", err, got, stderr)
			}
			reason := usageReason(t, stderr)
			if !strings.Contains(reason, `unknown subcommand "bogus"`) {
				t.Fatalf("reason = %q, want it to name the unknown subcommand", reason)
			}
			for _, name := range advertisedSubcommands(t, group) {
				if !strings.Contains(reason, name) {
					t.Errorf("reason %q does not name subcommand %q", reason, name)
				}
			}
			if strings.TrimSpace(stdout) != "" {
				t.Fatalf("stdout must be empty, stdout =\n%s", stdout)
			}
		})
	}
}

// TestCommandGroupsHelpExitsZero keeps the escape hatch: -h/--help prints help
// to stdout and exits 0, even though a bare invocation is now an error.
func TestCommandGroupsHelpExitsZero(t *testing.T) {
	for _, group := range commandGroups {
		t.Run(strings.Join(group, " "), func(t *testing.T) {
			r := newRunner(t)
			stdout, stderr, err := r.runSplitErr(append(append([]string{}, group...), "--help")...)

			if err != nil {
				t.Fatalf("--help returned %v, want nil", err)
			}
			if got := ExitCode(err); got != 0 {
				t.Fatalf("ExitCode(%v) = %d, want 0", err, got)
			}
			if !strings.Contains(stdout, "Usage:") || !strings.Contains(stdout, "Available Commands:") {
				t.Fatalf("help must go to stdout:\n%s", stdout)
			}
			if strings.TrimSpace(stderr) != "" {
				t.Fatalf("stderr must be empty on --help, stderr =\n%s", stderr)
			}
		})
	}
}

// TestRootBareInvocationPrintsHelp pins the deliberate exception: `ft` with no
// arguments is a request for help, not a mistake, so it stays exit 0.
func TestRootBareInvocationPrintsHelp(t *testing.T) {
	r := newRunner(t)
	stdout, stderr, err := r.runSplitErr()
	if err != nil || ExitCode(err) != 0 {
		t.Fatalf("bare ft returned %v (exit %d), want nil", err, ExitCode(err))
	}
	if !strings.Contains(stdout, "Available Commands:") {
		t.Fatalf("bare ft must print help to stdout:\n%s", stdout)
	}
	if strings.TrimSpace(stderr) != "" {
		t.Fatalf("stderr must be empty, stderr =\n%s", stderr)
	}
}

// TestEveryCommandGroupHandlesBareInvocation keeps the audit from rotting: a
// command group added later must either be runnable on purpose (like `ft up`)
// or take the shared missing-subcommand guard.
func TestEveryCommandGroupHandlesBareInvocation(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, func(string) string { return "" })
	root := NewRoot(deps)
	var walk func(cmd *cobra.Command, path []string)
	walk = func(cmd *cobra.Command, path []string) {
		for _, sub := range cmd.Commands() {
			subPath := append(append([]string{}, path...), sub.Name())
			if len(sub.Commands()) == 0 {
				continue
			}
			if sub.Run == nil && sub.RunE == nil {
				t.Errorf("ft %s is a command group with no RunE; give it requireSubcommand", strings.Join(subPath, " "))
			}
			walk(sub, subPath)
		}
	}
	walk(root, nil)
}
