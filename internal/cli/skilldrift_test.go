package cli

import (
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/skills"
	"github.com/khoinguyen/factotum/pkg/app"
)

func skillDriftRoot(t *testing.T) *cobra.Command {
	t.Helper()
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	return NewRoot(deps)
}

func TestEmbeddedSkillsReferenceRealCommandsAndFlags(t *testing.T) {
	root := skillDriftRoot(t)
	all, err := skills.All()
	if err != nil {
		t.Fatalf("skills.All() error = %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no embedded skills to check")
	}
	for _, skill := range all {
		if parsed := len(skillCommandLines(skill.Body)); parsed < 5 {
			t.Errorf("skill %q parsed only %d ft invocations; the checker is not seeing the skill", skill.Name, parsed)
		}
		for _, stale := range staleSkillReferences(root, skill.Body) {
			t.Errorf("skill %q: %s", skill.Name, stale)
		}
	}
}

func TestStaleSkillReferencesDetectsBadCommandAndFlag(t *testing.T) {
	root := skillDriftRoot(t)
	body := "```sh\nft task frobnicate\nft memory search --bogus\nft task next --limit 1\n```\n"
	stale := staleSkillReferences(root, body)
	if len(stale) != 2 {
		t.Fatalf("stale = %v, want two findings", stale)
	}
	if !strings.Contains(strings.Join(stale, "\n"), "frobnicate") {
		t.Fatalf("stale = %v, want the unknown subcommand reported", stale)
	}
	if !strings.Contains(strings.Join(stale, "\n"), "--bogus") {
		t.Fatalf("stale = %v, want the unknown flag reported", stale)
	}
}

// staleSkillReferences returns one message per command or flag referenced by an
// `ft ...` invocation in body that no longer exists in root.
func staleSkillReferences(root *cobra.Command, body string) []string {
	var stale []string
	for _, line := range skillCommandLines(body) {
		fields := strings.Fields(line)
		cmd := root
		descending := true
		for _, token := range fields[1:] {
			if strings.HasPrefix(token, "-") {
				name := token
				if eq := strings.IndexByte(name, '='); eq >= 0 {
					name = name[:eq]
				}
				if !hasFlag(cmd, name) {
					stale = append(stale, fmt.Sprintf("%q: unknown flag %q", line, name))
				}
				continue
			}
			if !descending {
				continue
			}
			if child := findChild(cmd, token); child != nil {
				cmd = child
				continue
			}
			// A plain identifier where a subcommand is expected is stale; a
			// placeholder (<task>), argument (field=value), or the first
			// positional argument ends subcommand descent.
			if isIdentifier(token) && len(cmd.Commands()) > 0 {
				stale = append(stale, fmt.Sprintf("%q: unknown command %q", line, token))
			}
			descending = false
		}
	}
	return stale
}

// isIdentifier reports whether token is a bare subcommand-looking word, as
// opposed to a placeholder or an argument value.
func isIdentifier(token string) bool {
	if token == "" {
		return false
	}
	for _, r := range token {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			continue
		}
		return false
	}
	return token[0] >= 'a' && token[0] <= 'z'
}

// skillCommandLines extracts the `ft ...` invocations from a skill: fenced code
// block lines and inline code spans that begin with ft.
func skillCommandLines(body string) []string {
	var out []string
	inFence := false
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			if command, ok := asFTCommand(line); ok {
				out = append(out, command)
			}
			continue
		}
		for _, span := range inlineCode(line) {
			if command, ok := asFTCommand(span); ok {
				out = append(out, command)
			}
		}
	}
	return out
}

func asFTCommand(line string) (string, bool) {
	if comment := strings.Index(line, " #"); comment >= 0 {
		line = strings.TrimSpace(line[:comment])
	}
	if line == "ft" || strings.HasPrefix(line, "ft ") {
		return line, true
	}
	return "", false
}

func inlineCode(line string) []string {
	var out []string
	for {
		start := strings.IndexByte(line, '`')
		if start < 0 {
			return out
		}
		rest := line[start+1:]
		end := strings.IndexByte(rest, '`')
		if end < 0 {
			return out
		}
		out = append(out, rest[:end])
		line = rest[end+1:]
	}
}

func findChild(cmd *cobra.Command, name string) *cobra.Command {
	for _, child := range cmd.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func hasFlag(cmd *cobra.Command, name string) bool {
	if name == "-h" || name == "--help" {
		return true
	}
	flags := cmd.Flags()
	inherited := cmd.InheritedFlags()
	switch {
	case strings.HasPrefix(name, "--"):
		long := name[2:]
		return flags.Lookup(long) != nil || inherited.Lookup(long) != nil
	case strings.HasPrefix(name, "-"):
		short := name[1:]
		return flags.ShorthandLookup(short) != nil || inherited.ShorthandLookup(short) != nil
	default:
		return false
	}
}
