package cli

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
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

// TestEmbeddedSkillsFitOutputBound guards `ft skill get` against the output
// bound silently truncating a skill: when stdout is not a terminal the printed
// window keeps only a head and tail, so a piped skill must stay under
// maxOutputLines with headroom. The guard fails at the bound, before truncation
// can reach an agent.
func TestEmbeddedSkillsFitOutputBound(t *testing.T) {
	all, err := skills.All()
	if err != nil {
		t.Fatalf("skills.All() error = %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no embedded skills to check")
	}
	for _, skill := range all {
		if lines := countLines([]byte(skill.Body)); lines >= maxOutputLines {
			t.Errorf("skill %q is %d lines; keep it below the %d-line output bound or `ft skill get %s` is truncated",
				skill.Name, lines, maxOutputLines, skill.Name)
		}
	}
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

// TestAgentSkillsReferenceRealCommandsAndFlags extends the drift guard from
// the embedded skills to the committed role skills (.agents/skills/*/SKILL.md).
// Those skills drive unattended agents, so a command they name that does not
// exist fails the run it was meant to serve, and nobody is watching to correct
// it. The checker is the same cobra walk the embedded skills use.
func TestAgentSkillsReferenceRealCommandsAndFlags(t *testing.T) {
	root := skillDriftRoot(t)
	paths, err := filepath.Glob(filepath.Join(repoRoot(t), ".agents", "skills", "*", "SKILL.md"))
	if err != nil {
		t.Fatalf("glob agent skills: %v", err)
	}
	if len(paths) == 0 {
		t.Fatal("no agent skills found under .agents/skills")
	}
	for _, path := range paths {
		rel, err := filepath.Rel(repoRoot(t), path)
		if err != nil {
			t.Fatalf("rel path %s: %v", path, err)
		}
		for _, finding := range roleSkillFindings(root, rel, readFile(t, path)) {
			t.Error(finding)
		}
	}
}

// ftMentionPattern matches the ft binary as a standalone word in a skill's
// code, so the drift guard notices a skill whose commands the parser cannot see.
var ftMentionPattern = regexp.MustCompile(`\bft\b`)

// roleSkillFindings returns the drift findings for one role skill: a stale
// reference for every `ft ...` invocation the command tree does not know, or a
// blind-spot finding when the skill's code names ft but yields no invocation for
// the checker to inspect. A skill whose code never names ft (typesafe-ai) has
// nothing to drift.
func roleSkillFindings(root *cobra.Command, rel, body string) []string {
	if len(skillCommandLines(body)) == 0 {
		for _, snippet := range skillCodeSnippets(body) {
			if ftMentionPattern.MatchString(snippet) {
				return []string{fmt.Sprintf("%s names ft commands but parsed none; the checker is not seeing the skill", rel)}
			}
		}
		return nil
	}
	findings := make([]string, 0)
	for _, stale := range staleSkillReferences(root, body) {
		findings = append(findings, fmt.Sprintf("%s: %s", rel, stale))
	}
	return findings
}

func TestRoleSkillFindingsCoversTranscriptAndBlindSpot(t *testing.T) {
	root := skillDriftRoot(t)
	cases := []struct {
		name      string
		body      string
		want      []string
		wantEmpty bool
	}{
		{
			name:      "prose mention only",
			body:      "The ft binary lives on PATH.\n",
			wantEmpty: true,
		},
		{
			name:      "transcript prompt parses",
			body:      "```sh\n$ ft msg runs --live\n```\n",
			wantEmpty: true,
		},
		{
			name:      "inline transcript prompt parses",
			body:      "Run `$ ft task next`.\n",
			wantEmpty: true,
		},
		{
			name: "stale transcript command",
			body: "```sh\n$ ft task frobnicate\n```\n",
			want: []string{"unknown command"},
		},
		{
			name: "code naming ft without an invocation is a blind spot",
			body: "```sh\ntool ft task next\n```\n",
			want: []string{"parsed none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := roleSkillFindings(root, "SKILL.md", tc.body)
			if tc.wantEmpty {
				if len(findings) != 0 {
					t.Fatalf("findings = %v, want none", findings)
				}
				return
			}
			joined := strings.Join(findings, "\n")
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("findings = %v, want %q", findings, want)
				}
			}
		})
	}
}

func TestSkillCommandLinesExtractsFencedAndInline(t *testing.T) {
	body := "```sh\nft task next\n$ ft msg runs --live\nft\ttask list\n```\n\nRun `ft memory search terra` and `ft done <task>`.\n"
	got := skillCommandLines(body)
	want := []string{"ft task next", "ft msg runs --live", "ft\ttask list", "ft memory search terra", "ft done <task>"}
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
