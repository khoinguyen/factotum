package cli

import (
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

func TestSkillCommandLinesExtractsFencedAndInline(t *testing.T) {
	body := "```sh\nft task next\n```\n\nRun `ft memory search terra` and `ft done <task>`.\n"
	got := skillCommandLines(body)
	want := []string{"ft task next", "ft memory search terra", "ft done <task>"}
	if len(got) != len(want) {
		t.Fatalf("skillCommandLines() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("skillCommandLines() = %v, want %v", got, want)
		}
	}
}

func TestStaleSkillReferencesDetectsInlineBacktick(t *testing.T) {
	root := skillDriftRoot(t)
	body := "Use `ft task frobnicate` and `ft memory search --bogus` here.\n"
	stale := staleSkillReferences(root, body)
	joined := strings.Join(stale, "\n")
	if len(stale) != 2 || !strings.Contains(joined, "frobnicate") || !strings.Contains(joined, "--bogus") {
		t.Fatalf("stale = %v, want the inline stale command and flag", stale)
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
