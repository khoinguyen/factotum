package cli

import (
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
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

// skillLineHeadroom is the free space every embedded skill keeps under the
// output bound's line limit. With no headroom, the next one-line addition to any
// skill turns an unrelated PR red and the cheapest fix is an arbitrary trim; ten
// lines absorbs a small edit while the bound still fails a skill that keeps
// growing.
const skillLineHeadroom = 10

// skillByteHeadroom is the same idea on the byte axis, which is the one that
// actually truncated: a skill at the ceiling has room for a sentence, not an
// edit, so one added word reds an unrelated PR. Both axes are budgets, not
// ceilings — each rendering must stay skillByteHeadroom bytes under the bound.
const skillByteHeadroom = 1024

// skillFormats are the shapes `ft skill get` can print. Each is measured: json
// escapes and quotes the body and yaml indents it, so the format that fits is
// never proof the others do.
var skillFormats = []string{"text", "json", "yaml"}

// TestEmbeddedSkillsFitOutputBound guards `ft skill get` against the output
// bound silently truncating a skill: when stdout is not a terminal the printed
// window keeps only a head and tail, so every rendering of a skill must stay
// under maxOutputLines (lines) and maxOutputBytes (bytes) with headroom. The
// guard renders each skill through the command's own renderer, so it measures
// what a caller gets in text, json, and yaml alike; the guard fails before
// truncation can reach an agent.
func TestEmbeddedSkillsFitOutputBound(t *testing.T) {
	all, err := skills.All()
	if err != nil {
		t.Fatalf("skills.All() error = %v", err)
	}
	if len(all) == 0 {
		t.Fatal("no embedded skills to check")
	}
	for _, skill := range all {
		doc := skillDoc{Name: skill.Name, Description: skill.Description, Body: skill.Body}
		for _, finding := range skillBoundFindings(doc) {
			t.Error(finding)
		}
	}
}

// TestSkillBoundFindingKeepsLineHeadroom pins the line half of the guard: a
// rendering at the allowed size passes, one line over fails naming the budget,
// the format, and how many lines to cut, and a rendering at the raw output bound
// still fails, so the bound cannot quietly become the budget.
func TestSkillBoundFindingKeepsLineHeadroom(t *testing.T) {
	budget := maxOutputLines - skillLineHeadroom
	rendering := func(lines int) []byte { return []byte(strings.Repeat("line\n", lines)) }
	cases := []struct {
		name     string
		lines    int
		wantFail bool
		cut      string
	}{
		{name: "well under", lines: skillLineHeadroom, wantFail: false},
		{name: "exactly the budget", lines: budget, wantFail: false},
		{name: "one line over the budget", lines: budget + 1, wantFail: true, cut: "cut 1 line"},
		{name: "at the output bound", lines: maxOutputLines, wantFail: true, cut: fmt.Sprintf("cut %d lines", skillLineHeadroom)},
	}
	for _, format := range skillFormats {
		for _, tc := range cases {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				findings := skillRenderFindings("probe", format, rendering(tc.lines))
				if !tc.wantFail {
					if len(findings) != 0 {
						t.Fatalf("skillRenderFindings(%s, %d lines) = %v, want it to fit", format, tc.lines, findings)
					}
					return
				}
				joined := strings.Join(findings, "\n")
				if !strings.Contains(joined, fmt.Sprintf("at or below %d", budget)) || !strings.Contains(joined, "lines of "+format+" output") {
					t.Fatalf("skillRenderFindings(%s, %d lines) = %v, want the %d-line budget and the format named", format, tc.lines, findings, budget)
				}
				if !strings.Contains(joined, tc.cut) {
					t.Fatalf("skillRenderFindings(%s, %d lines) = %v, want %q so the author knows what to cut", format, tc.lines, findings, tc.cut)
				}
			})
		}
	}
}

// TestSkillBoundFindingChecksByteBudgetPerFormat pins the byte half of the
// guard, which the line count cannot see: the body is printed (text) or
// serialized (json/yaml) whole, so a rendering over the byte budget truncates
// `ft skill get`. Each format is probed directly so the envelope of the other
// shapes cannot mask the axis under test, and the budget — the bound less
// skillByteHeadroom — is what passes, not the raw bound. A failure names the
// overage, so the next author knows how much to cut rather than only that the
// rendering does not fit.
func TestSkillBoundFindingChecksByteBudgetPerFormat(t *testing.T) {
	budget := maxOutputBytes - skillByteHeadroom
	// A one-line rendering of exactly size bytes, so only the byte axis is in
	// play.
	rendering := func(size int) []byte { return []byte(strings.Repeat("x", size-1) + "\n") }
	cases := []struct {
		name     string
		size     int
		wantFail bool
		cut      string
	}{
		{name: "well under", size: budget / 2, wantFail: false},
		{name: "one byte under the budget", size: budget - 1, wantFail: false},
		{name: "exactly the byte budget", size: budget, wantFail: false},
		{name: "one byte over the budget", size: budget + 1, wantFail: true, cut: "cut 1 byte"},
		{name: "at the raw output bound", size: maxOutputBytes, wantFail: true, cut: fmt.Sprintf("cut %d bytes", skillByteHeadroom)},
	}
	for _, format := range skillFormats {
		for _, tc := range cases {
			t.Run(format+"/"+tc.name, func(t *testing.T) {
				findings := skillRenderFindings("probe", format, rendering(tc.size))
				if !tc.wantFail {
					if len(findings) != 0 {
						t.Fatalf("skillRenderFindings(%s, %d bytes) = %v, want it to fit", format, tc.size, findings)
					}
					return
				}
				joined := strings.Join(findings, "\n")
				if !strings.Contains(joined, fmt.Sprintf("at or below %d", budget)) {
					t.Fatalf("skillRenderFindings(%s, %d bytes) = %v, want the %d-byte budget named", format, tc.size, findings, budget)
				}
				if !strings.Contains(joined, format+" output") {
					t.Fatalf("skillRenderFindings(%s, %d bytes) = %v, want the rendered format named", format, tc.size, findings)
				}
				if !strings.Contains(joined, tc.cut) {
					t.Fatalf("skillRenderFindings(%s, %d bytes) = %v, want %q so the author knows what to cut", format, tc.size, findings, tc.cut)
				}
			})
		}
	}
}

// TestSkillBoundFindingsSeesStructuredRenderings pins the regression this guard
// exists for: a rendering that fits can still break the budget once serialized.
// json doubles every newline, yaml indents every line and wraps the body in
// envelope lines, so both are measured rather than inferred from the text.
func TestSkillBoundFindingsSeesStructuredRenderings(t *testing.T) {
	t.Run("byte inflation", func(t *testing.T) {
		// 300 lines of 104 bytes: 31,500 bytes of text, which fits the byte
		// budget with 244 to spare and the line budget with 90 lines to spare.
		// json escapes each newline and quotes the body, yaml indents each
		// line, so both serialize past the budget.
		doc := skillDoc{Name: "probe", Body: strings.Repeat(strings.Repeat("x", 104)+"\n", 300)}
		findings := skillBoundFindings(doc)
		joined := strings.Join(findings, "\n")
		for _, format := range []string{"json", "yaml"} {
			if !strings.Contains(joined, format+" output") {
				t.Fatalf("skillBoundFindings() = %v, want the over-budget %s rendering reported", findings, format)
			}
		}
		if strings.Contains(joined, "text output") {
			t.Fatalf("skillBoundFindings() = %v, want the text rendering that fits not reported", findings)
		}
	})

	t.Run("yaml envelope lines", func(t *testing.T) {
		// A 389-line body fits the text budget (390); yaml adds its three-line
		// envelope and renders 392, two over the budget, while the text count is
		// still fine. A guard that counted only the text would call this doc fit
		// and let the yaml budget erode.
		doc := skillDoc{Name: "probe", Body: strings.Repeat("line\n", maxOutputLines-skillLineHeadroom-1)}
		findings := skillBoundFindings(doc)
		joined := strings.Join(findings, "\n")
		if !strings.Contains(joined, "lines of yaml output") {
			t.Fatalf("skillBoundFindings() = %v, want the yaml envelope over the line budget reported", findings)
		}
		if strings.Contains(joined, "text output") {
			t.Fatalf("skillBoundFindings() = %v, want the text rendering that fits not reported", findings)
		}
	})
}

// TestSkillRenderingsMatchSkillGet pins that the guard measures the real
// command: each rendering it checks is byte-identical to what `ft skill get`
// prints for that format, so a change to the command cannot slip past a guard
// that renders something else.
func TestSkillRenderingsMatchSkillGet(t *testing.T) {
	r := newRunner(t)
	skill, err := skills.Get(skills.DefaultName)
	if err != nil {
		t.Fatalf("skills.Get(%q) error = %v", skills.DefaultName, err)
	}
	doc := skillDoc{Name: skill.Name, Description: skill.Description, Body: skill.Body}
	for _, format := range skillFormats {
		t.Run(format, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderSkillDoc(&buf, format, doc); err != nil {
				t.Fatalf("renderSkillDoc(%s) error = %v", format, err)
			}
			args := []string{"skill", "get", skills.DefaultName, "--full"}
			if format != "text" {
				args = append(args, "-o", format)
			}
			if got := r.run(args...); got != buf.String() {
				t.Fatalf("skill get %s = %d bytes, guard renders %d; the guard is not measuring the command", format, len(got), buf.Len())
			}
		})
	}
}

// skillBoundFindings reports every way a skill breaks the output bound in any
// format `ft skill get` prints, or nil when every rendering fits with the
// required headroom. Both limits come from the runtime's own constants, so the
// guard cannot drift from the bound it protects, and every rendering is measured
// on both axes: json escapes the body and doubles each newline, yaml indents it
// and wraps it in envelope lines, so a doc that fits as text can still truncate
// as json - which is the bug this guard exists for.
func skillBoundFindings(doc skillDoc) []string {
	var findings []string
	for _, format := range skillFormats {
		var buf bytes.Buffer
		if err := renderSkillDoc(&buf, format, doc); err != nil {
			findings = append(findings, fmt.Sprintf("skill %q does not render as %s output: %v", doc.Name, format, err))
			continue
		}
		findings = append(findings, skillRenderFindings(doc.Name, format, buf.Bytes())...)
	}
	return findings
}

// skillRenderFindings reports how one rendering of a skill breaks the output
// budget in lines or bytes, naming the format so the trim lands on the shape
// that is over, and the overage so the author knows how much to cut. Both limits
// are budgets, not ceilings: the raw bound truncates the first byte or line past
// it, so a rendering at the bound has no room for an edit and one added word
// reds an unrelated PR.
func skillRenderFindings(name, format string, rendered []byte) []string {
	var findings []string
	if lines, budget := countLines(rendered), maxOutputLines-skillLineHeadroom; lines > budget {
		findings = append(findings, fmt.Sprintf("skill %q renders as %d lines of %s output; keep it at or below %d (the %d-line output bound less %d lines of headroom) so that format is not truncated: %s",
			name, lines, format, budget, maxOutputLines, skillLineHeadroom, overage(lines-budget, "line")))
	}
	if size, budget := len(rendered), maxOutputBytes-skillByteHeadroom; size > budget {
		findings = append(findings, fmt.Sprintf("skill %q renders as %d bytes of %s output; keep it at or below %d (the %d-byte output bound less %d bytes of headroom) so `ft skill get %s` is never truncated in that format: %s",
			name, size, format, budget, maxOutputBytes, skillByteHeadroom, name, overage(size-budget, "byte")))
	}
	return findings
}

// overage words how much a rendering is over its budget, pluralized so a
// one-unit overage does not read as "1 bytes", so the finding always states the
// exact cut the next author has to make.
func overage(amount int, unit string) string {
	if amount == 1 {
		return "cut 1 " + unit
	}
	return fmt.Sprintf("cut %d %ss", amount, unit)
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

// TestRepoDocsReferenceRealCommands extends the drift guard from the skill entry
// points to the repository's own markdown: every file under docs/ and the
// top-level *.md files. A design doc is what the implementation is reviewed
// against, and the README now carries the run/agent/isolation detail the ft
// skill used to, so a command either advertises that does not exist sends readers
// and agents at a verb that fails, and nothing else catches it: the skills
// checker only walks .agents/skills. The walk is the same cobra check; a fenced
// block marked `design` is exempt because it records verbs that were considered
// but not built, and only that block is exempt.
func TestRepoDocsReferenceRealCommands(t *testing.T) {
	root := skillDriftRoot(t)
	repo := repoRoot(t)
	files, err := repoDocFiles(repo)
	if err != nil {
		t.Fatalf("repoDocFiles(%s) error = %v", repo, err)
	}
	// The top level is the reason the walk widened: if README.md drops out of the
	// target list, the guard would still pass on docs/ alone and the README detail
	// would drift silently again.
	for _, name := range []string{"README.md", "AGENTS.md"} {
		if !containsPath(files, filepath.Join(repo, name)) {
			t.Errorf("repoDocFiles() = %v, want %s in the walk", files, name)
		}
	}
	findings, parsed, err := repoDocFindings(root, repo)
	if err != nil {
		t.Fatalf("repoDocFindings(%s) error = %v", repo, err)
	}
	if parsed < 50 {
		// The docs carry well over a hundred invocations today; a collapse means
		// the walk or the parser broke and the check would pass on nothing.
		t.Fatalf("repo markdown parsed only %d ft invocations; the checker is not seeing it", parsed)
	}
	for _, finding := range findings {
		t.Error(finding)
	}
	// The README must carry real invocations of its own: a green walk over a
	// README the parser cannot read would say nothing about the file this guard
	// was widened for.
	if _, readmeParsed := docFindings(root, "README.md", readFile(t, filepath.Join(repo, "README.md"))); readmeParsed == 0 {
		t.Error("README.md parsed no ft invocations; the walk sees the file but not its commands")
	}
}

// TestRepoDocFilesCoversDocsAndTopLevelMarkdown pins the walk's target list: every
// markdown file under docs/ and the top-level *.md files. Other markdown below the
// top level is out of scope, so the walk stays about the docs a reader lands on.
func TestRepoDocFilesCoversDocsAndTopLevelMarkdown(t *testing.T) {
	repo := t.TempDir()
	for _, rel := range []string{
		"README.md",
		"AGENTS.md",
		"SECURITY.md",
		filepath.Join("docs", "a.md"),
		filepath.Join("docs", "msg", "b.md"),
		filepath.Join("web", "README.md"),
	} {
		writeDocUnder(t, repo, rel, "nothing to see\n")
	}
	files, err := repoDocFiles(repo)
	if err != nil {
		t.Fatalf("repoDocFiles() error = %v", err)
	}
	got := make([]string, 0, len(files))
	for _, path := range files {
		rel, err := filepath.Rel(repo, path)
		if err != nil {
			t.Fatalf("rel path %s: %v", path, err)
		}
		got = append(got, rel)
	}
	want := []string{
		"AGENTS.md",
		"README.md",
		"SECURITY.md",
		filepath.Join("docs", "a.md"),
		filepath.Join("docs", "msg", "b.md"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("repoDocFiles() = %v, want %v", got, want)
	}
}

// TestRepoDocFindingsChecksTopLevelMarkdown plants drift in the top-level markdown
// and requires the walk to report it: the same rules hold there as under docs/, so
// a bogus verb in a fence or an inline span turns the guard red, the `design`
// exemption covers only its own block, and code that names ft without an
// invocation stays a blind spot.
func TestRepoDocFindingsChecksTopLevelMarkdown(t *testing.T) {
	root := skillDriftRoot(t)
	cases := []struct {
		name string
		rel  string
		body string
		want []string
	}{
		{
			name: "readme fence",
			rel:  "README.md",
			body: "```sh\nft msg retry t-1\n```\n",
			want: []string{`unknown command "retry"`},
		},
		{
			name: "readme inline span",
			rel:  "README.md",
			body: "Run `ft msg retry t-1` on a stuck message.\n",
			want: []string{`unknown command "retry"`},
		},
		{
			name: "agents indented block",
			rel:  "AGENTS.md",
			body: "    ft task frobnicate t-1\n",
			want: []string{`unknown command "frobnicate"`},
		},
		{
			name: "readme stale flag",
			rel:  "README.md",
			body: "```sh\nft task next --bogus\n```\n",
			want: []string{`unknown flag "--bogus"`},
		},
		{
			name: "readme design fence is exempt",
			rel:  "README.md",
			body: "```design\nft msg retry t-1\n```\n",
		},
		{
			name: "readme design fence exempts only its own block",
			rel:  "README.md",
			body: "```design\nft msg retry t-1\n```\n\n```sh\nft msg prune\n```\n",
			want: []string{`unknown command "prune"`},
		},
		{
			name: "readme code naming ft without an invocation is a blind spot",
			rel:  "README.md",
			body: "```sh\ntool ft task next\n```\n",
			want: []string{"parsed none"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := t.TempDir()
			// A repo always has docs/; the walk treats a missing one as a wrong
			// root rather than silently narrowing to the top level.
			writeDocUnder(t, filepath.Join(repo, "docs"), "index.md", "No commands here.\n")
			writeDocUnder(t, repo, tc.rel, tc.body)
			findings, _, err := repoDocFindings(root, repo)
			if err != nil {
				t.Fatalf("repoDocFindings() error = %v", err)
			}
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
			if !strings.Contains(joined, tc.rel) {
				t.Fatalf("findings = %v, want the top-level path %q named", findings, tc.rel)
			}
		})
	}
}

// TestRepoDocFindingsFailsOnUnknownCommand plants a doc that advertises a verb the
// command tree does not have and requires the docs check to report it. The guard
// is only worth having if a bogus command turns it red.
func TestRepoDocFindingsFailsOnUnknownCommand(t *testing.T) {
	root := skillDriftRoot(t)
	repo := t.TempDir()
	writeDocUnder(t, filepath.Join(repo, "docs"), filepath.Join("msg", "bogus.md"), "```sh\n$ ft msg retry t-1\n```\n")
	findings, parsed, err := repoDocFindings(root, repo)
	if err != nil {
		t.Fatalf("repoDocFindings() error = %v", err)
	}
	if parsed != 1 {
		t.Fatalf("parsed = %d, want the planted invocation", parsed)
	}
	joined := strings.Join(findings, "\n")
	if !strings.Contains(joined, filepath.Join("docs", "msg", "bogus.md")) || !strings.Contains(joined, `unknown command "retry"`) {
		t.Fatalf("findings = %v, want the planted unknown command under its doc path", findings)
	}
}

// TestDocFindingsChecksEveryFenceForm plants a doc in each way markdown can show
// code and requires the walk to report the bogus verb inside it. A form the walk
// does not read is a place a stale command hides and passes, so the guard is only
// as strong as the forms it understands.
func TestDocFindingsChecksEveryFenceForm(t *testing.T) {
	root := skillDriftRoot(t)
	cases := []struct {
		name string
		body string
	}{
		{name: "backtick fence", body: "```sh\nft msg retry t-1\n```\n"},
		{name: "tilde fence", body: "~~~sh\nft msg retry t-1\n~~~\n"},
		{name: "indented backtick fence", body: "    ```sh\n    ft msg retry t-1\n    ```\n"},
		{name: "indented tilde fence", body: "    ~~~sh\n    ft msg retry t-1\n    ~~~\n"},
		{name: "indented code block", body: "    ft msg retry t-1\n"},
		{name: "tab-indented code block", body: "\tft msg retry t-1\n"},
		{name: "second command on an indented line", body: "    echo x; ft msg retry t-1\n"},
		{name: "wrapped inline span", body: "Prose `ft msg\nretry t-1` here.\n"},
	}
	repo := t.TempDir()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writeDocUnder(t, filepath.Join(repo, "docs"), filepath.Join("msg", "bogus.md"), tc.body)
			findings, parsed, err := repoDocFindings(root, repo)
			if err != nil {
				t.Fatalf("repoDocFindings() error = %v", err)
			}
			if parsed != 1 {
				t.Fatalf("parsed = %d, want the planted invocation", parsed)
			}
			joined := strings.Join(findings, "\n")
			if !strings.Contains(joined, "docs/msg/bogus.md") || !strings.Contains(joined, `unknown command "retry"`) {
				t.Fatalf("findings = %v, want the planted unknown command under its doc path", findings)
			}
		})
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
			name: "the design exemption does not carry into an unclosed block",
			body: "```design\nft msg retry t-1\n```\n\n```sh\nft msg frobnicate t-1\n",
			want: []string{`unknown command "frobnicate"`, "unterminated code fence"},
		},
		{
			name: "tilde design fence is exempt",
			body: "~~~design\nft msg retry t-1\n~~~\n",
		},
		{
			name: "tilde design fence does not exempt the next block",
			body: "~~~design\nft msg retry t-1\n~~~\n\n~~~sh\nft msg prune\n~~~\n",
			want: []string{`unknown command "prune"`},
		},
		{
			name: "an unclosed tilde fence fails closed and is reported",
			body: "~~~design\nft msg retry t-1\n",
			want: []string{`unknown command "retry"`, "unterminated code fence"},
		},
		{
			name: "a fence closes only on its own marker",
			body: "~~~sh\n```\nft msg retry t-1\n```\n~~~\n",
			want: []string{`unknown command "retry"`},
		},
		{
			name: "a shorter fence does not close a longer one",
			body: "````sh\n```\nft msg retry t-1\n```\n````\n",
			want: []string{`unknown command "retry"`},
		},
		{
			name: "an indented code block is checked",
			body: "    ft msg retry t-1\n",
			want: []string{`unknown command "retry"`},
		},
		{
			name: "indented prose that mentions ft is not read as a command",
			body: "- a step\n\n    the ft binary lives on PATH.\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings, _ := docFindings(root, "docs/x.md", tc.body)
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
	snippets := skillCodeSnippets(body)
	return codeFindings(root, rel, snippets, ftCommandLines(snippets), "skill")
}

// docFindings returns the drift findings for one repository doc and how many ft
// invocations the walk parsed. Docs are held to the command tree like a skill,
// with one deliberate exception: a fenced block whose info string marks it
// `design` records verbs the design considered but the CLI does not implement
// yet, so that block is exempt. The exemption is explicit and block-scoped, so
// prose and every plain block still fail on a verb that does not exist. A fence
// left open is reported: it would otherwise move commands in or out of a block
// without the reader or the check knowing.
func docFindings(root *cobra.Command, rel, body string) ([]string, int) {
	snippets, unterminated := scanCode(body, isDesignFence)
	lines := ftCommandLines(snippets)
	findings := codeFindings(root, rel, snippets, lines, "doc")
	if unterminated {
		findings = append(findings, fmt.Sprintf("%s: unterminated code fence; commands after it are not reliably in a block", rel))
	}
	return findings, len(lines)
}

// codeFindings reports the stale references in the invocations of one file, or a
// blind-spot finding when the snippets they came from name ft but yield no
// invocation. The caller passes both so the snippets are parsed once.
func codeFindings(root *cobra.Command, rel string, snippets, lines []string, kind string) []string {
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

// repoDocFiles returns the markdown the drift guard walks: every file under
// docs/ and the top-level *.md files. The README and AGENTS.md carry the run,
// agent and isolation detail the ft skill used to, so they drift the same way a
// design doc does and are held to the same check. Markdown nested elsewhere is
// out of scope: it is neither the docs a reader lands on nor the front page.
func repoDocFiles(repoRoot string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(repoRoot, "docs"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	tops, err := filepath.Glob(filepath.Join(repoRoot, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob top-level markdown: %w", err)
	}
	files = append(files, tops...)
	// A stable order keeps findings deterministic however the walk enumerates.
	sort.Strings(files)
	return files, nil
}

// repoDocFindings walks the repository markdown the drift guard covers and
// returns the findings plus how many ft invocations the walk parsed, so a caller
// can prove the checker sees the markdown instead of passing because it found
// nothing. Paths are reported relative to repoRoot, so a message reads like
// docs/msg/design.md or README.md. Both halves of the walk share docFindings, so
// a command reads the same wherever it is written.
func repoDocFindings(root *cobra.Command, repoRoot string) ([]string, int, error) {
	files, err := repoDocFiles(repoRoot)
	if err != nil {
		return nil, 0, err
	}
	var findings []string
	parsed := 0
	for _, path := range files {
		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil, 0, fmt.Errorf("rel path %s: %w", path, err)
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", path, err)
		}
		docFound, docParsed := docFindings(root, rel, string(body))
		findings = append(findings, docFound...)
		parsed += docParsed
	}
	return findings, parsed, nil
}

// containsPath reports whether files holds path, so a test can assert a walker's
// target list without caring about its order.
func containsPath(files []string, path string) bool {
	for _, file := range files {
		if file == path {
			return true
		}
	}
	return false
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
