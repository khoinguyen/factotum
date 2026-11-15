package cli

import (
	"fmt"
	"io"
	"io/fs"
	"os"
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

// TestDocsReferenceRealCommands extends the drift guard from the skill entry
// points to docs/. A design doc is what the implementation is reviewed against,
// so a command it advertises that does not exist sends readers and agents at a
// verb that fails, and nothing else catches it: the skills checker only walks
// .agents/skills. The walk is the same cobra check; a fenced block marked
// `design` is exempt because it records verbs that were considered but not
// built, and only that block is exempt.
func TestDocsReferenceRealCommands(t *testing.T) {
	root := skillDriftRoot(t)
	dir := filepath.Join(repoRoot(t), "docs")
	findings, parsed, err := docsFindings(root, dir)
	if err != nil {
		t.Fatalf("docsFindings(%s) error = %v", dir, err)
	}
	if parsed < 50 {
		// The docs carry well over a hundred invocations today; a collapse means
		// the walk or the parser broke and the check would pass on nothing.
		t.Fatalf("docs parsed only %d ft invocations; the checker is not seeing the docs", parsed)
	}
	for _, finding := range findings {
		t.Error(finding)
	}
}

// TestDocsFindingsFailsOnUnknownCommand plants a doc that advertises a verb the
// command tree does not have and requires the docs check to report it. The guard
// is only worth having if a bogus command turns it red.
func TestDocsFindingsFailsOnUnknownCommand(t *testing.T) {
	root := skillDriftRoot(t)
	dir := filepath.Join(t.TempDir(), "docs")
	writeDocUnder(t, dir, filepath.Join("msg", "bogus.md"), "```sh\n$ ft msg retry t-1\n```\n")
	findings, parsed, err := docsFindings(root, dir)
	if err != nil {
		t.Fatalf("docsFindings() error = %v", err)
	}
	if parsed != 1 {
		t.Fatalf("parsed = %d, want the planted invocation", parsed)
	}
	joined := strings.Join(findings, "\n")
	if !strings.Contains(joined, "docs/msg/bogus.md") || !strings.Contains(joined, `unknown command "retry"`) {
		t.Fatalf("findings = %v, want the planted unknown command under its doc path", findings)
	}
}

// TestDocFindingsScopesTheDesignExemption pins the one way a doc may name an
// unbuilt command: an explicit `design` fence. The exemption is block-scoped, so
// the same verb in a plain fence or in prose is still drift.
func TestDocFindingsScopesTheDesignExemption(t *testing.T) {
	root := skillDriftRoot(t)
	cases := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "plain fence is checked",
			body: "```sh\nft msg retry t-1\n```\n",
			want: []string{`unknown command "retry"`},
		},
		{
			name: "design fence is exempt",
			body: "```design\nft msg retry t-1\n```\n",
		},
		{
			name: "design fence does not exempt the next block",
			body: "```design\nft msg retry t-1\n```\n\n```sh\nft msg prune\n```\n",
			want: []string{`unknown command "prune"`},
		},
		{
			name: "prose is never exempt",
			body: "An operator can `ft msg retry` a failed message.\n",
			want: []string{`unknown command "retry"`},
		},
		{
			name: "an unclosed fence fails closed and is reported",
			body: "```design\nft msg retry t-1\n",
			want: []string{`unknown command "retry"`, "unterminated code fence"},
		},
		{
			name: "an unclosed fence after a design block still fails",
			body: "```design\nft msg retry t-1\n\n```sh\nft msg frobnicate t-1\n```\n",
			want: []string{"unterminated code fence"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := docFindings(root, "docs/x.md", tc.body)
			joined := strings.Join(findings, "\n")
			if len(tc.want) == 0 {
				if len(findings) != 0 {
					t.Fatalf("findings = %v, want none", findings)
				}
				return
			}
			for _, want := range tc.want {
				if !strings.Contains(joined, want) {
					t.Fatalf("findings = %v, want %q", findings, want)
				}
			}
			if len(findings) != len(tc.want) {
				t.Fatalf("findings = %v, want exactly %v", findings, tc.want)
			}
		})
	}
}

// TestStaleSkillReferencesReadsSynopsisBrackets guards the shared checker against
// reporting a doc's synopsis notation as drift: docs write optional flags in
// brackets and alternatives with a pipe, so `[-b body | -]` must not read as an
// unknown `-]` flag.
func TestStaleSkillReferencesReadsSynopsisBrackets(t *testing.T) {
	root := skillDriftRoot(t)
	body := "```\n" +
		"ft msg send <address|ticket-id> [-b body | -] [--from <actor>] [--link kind=url]\n" +
		"ft msg inbox [--state queued|delivered|read|failed] [--limit N]\n" +
		"```\n"
	if stale := staleSkillReferences(root, body); len(stale) != 0 {
		t.Fatalf("stale = %v, want the synopsis notation read, not reported", stale)
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
	return codeFindings(root, rel, skillCodeSnippets(body), "skill")
}

// docFindings returns the drift findings for one repository doc. Docs are held to
// the command tree like a skill, with one deliberate exception: a fenced block
// whose info string marks it `design` records verbs the design considered but the
// CLI does not implement yet, so that block is exempt. The exemption is explicit
// and block-scoped, so prose and every plain block still fail on a verb that does
// not exist. A fence left open is reported: it would otherwise move commands in or
// out of a block without the reader or the check knowing.
func docFindings(root *cobra.Command, rel, body string) []string {
	findings := codeFindings(root, rel, codeSnippets(body, isDesignFence), "doc")
	if unterminatedFence(body) {
		findings = append(findings, fmt.Sprintf("%s: unterminated code fence; commands after it are not reliably in a block", rel))
	}
	return findings
}

// codeFindings reports the stale references in the code snippets of one file, or
// a blind-spot finding when the snippets name ft but yield no invocation.
func codeFindings(root *cobra.Command, rel string, snippets []string, kind string) []string {
	lines := ftCommandLines(snippets)
	if len(lines) == 0 {
		for _, snippet := range snippets {
			if ftMentionPattern.MatchString(snippet) {
				return []string{fmt.Sprintf("%s names ft commands but parsed none; the checker is not seeing the %s", rel, kind)}
			}
		}
		return nil
	}
	findings := make([]string, 0)
	for _, stale := range staleReferences(root, lines) {
		findings = append(findings, fmt.Sprintf("%s: %s", rel, stale))
	}
	return findings
}

// isDesignFence reports whether a fence's info string marks an unbuilt design
// sketch. Marking the fence is the doc's explicit claim that the block is not the
// shipped surface; a plain fence is checked.
func isDesignFence(info string) bool {
	for _, field := range strings.Fields(info) {
		if field == "design" {
			return true
		}
	}
	return false
}

// docsFindings walks every markdown file under dir and returns the drift findings
// plus how many ft invocations the walk parsed, so a caller can prove the checker
// sees the docs instead of passing because it found nothing. Paths are reported
// relative to dir's parent, so a message reads like docs/msg/design.md.
func docsFindings(root *cobra.Command, dir string) ([]string, int, error) {
	var findings []string
	parsed := 0
	err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		rel, err := filepath.Rel(filepath.Dir(dir), path)
		if err != nil {
			return fmt.Errorf("rel path %s: %w", path, err)
		}
		snippets := codeSnippets(string(body), isDesignFence)
		parsed += len(ftCommandLines(snippets))
		findings = append(findings, docFindings(root, rel, string(body))...)
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	return findings, parsed, nil
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

// writeDocUnder plants a markdown file under dir, creating the parent directory,
// so a test can point the docs drift check at a fixture instead of the repo docs.
func writeDocUnder(t *testing.T, dir, rel, body string) {
	t.Helper()
	path := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}
