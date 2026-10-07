package groom

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReportTemplateSectionsInOrder(t *testing.T) {
	got := sectionTitles(ReportTemplate())
	want := ReportSections()
	if !slices.Equal(got, want) {
		t.Fatalf("report template sections = %v, want %v", got, want)
	}
}

func TestDeferredQuestionsTemplateSectionsInOrder(t *testing.T) {
	got := sectionTitles(DeferredQuestionsTemplate())
	want := DeferredQuestionsSections()
	if !slices.Equal(got, want) {
		t.Fatalf("deferred-questions template sections = %v, want %v", got, want)
	}
}

func TestTemplatesAreMarkdown(t *testing.T) {
	for name, body := range map[string]string{
		"report":             ReportTemplate(),
		"deferred questions": DeferredQuestionsTemplate(),
	} {
		if strings.TrimSpace(body) == "" {
			t.Fatalf("%s template is empty", name)
		}
		if !strings.HasPrefix(body, "#") {
			t.Fatalf("%s template is not markdown:\n%s", name, body)
		}
	}
}

// TestSessionPromptIsDurableRepoData guards the committed grooming-session
// prompt: it must live at PromptPath in the repo, be markdown, and carry the
// protocol so a session launched from it behaves. The prompt is data, so this
// test reads the file from the tree rather than an embedded copy.
func TestSessionPromptIsDurableRepoData(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", PromptPath))
	if err != nil {
		t.Fatalf("read durable session prompt %q: %v", PromptPath, err)
	}
	body := string(data)
	if strings.TrimSpace(body) == "" {
		t.Fatal("session prompt is empty")
	}
	if !strings.HasPrefix(strings.TrimSpace(body), "#") {
		t.Fatalf("session prompt is not markdown:\n%s", body)
	}
	lower := strings.ToLower(body)
	for _, want := range []string{
		"team lead", "product owner", "defer", "groomed", "assigned", "agent-ready",
	} {
		if !strings.Contains(lower, want) {
			t.Errorf("session prompt does not mention %q", want)
		}
	}
}

// TestSessionPromptReportTemplateMatchesVersioned pins the report template that
// prompt.md embeds to the versioned templates/report.md, so a session launched
// from the prompt cannot drift from the contract the report is checked against.
func TestSessionPromptReportTemplateMatchesVersioned(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", PromptPath))
	if err != nil {
		t.Fatalf("read durable session prompt %q: %v", PromptPath, err)
	}
	inline := fencedBlock(t, string(data), "# Grooming report")
	if got, want := strings.TrimSpace(inline), strings.TrimSpace(ReportTemplate()); got != want {
		t.Fatalf("prompt report template drifted from the versioned template:\n--- prompt ---\n%s\n--- versioned ---\n%s", got, want)
	}
}

// fencedBlock returns the first triple-backtick fenced block in body whose first
// non-empty line begins with firstLine.
func fencedBlock(t *testing.T, body, firstLine string) string {
	t.Helper()
	lines := strings.Split(body, "\n")
	for i := 0; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) != "```" {
			continue
		}
		j := i + 1
		for j < len(lines) && strings.TrimSpace(lines[j]) == "" {
			j++
		}
		if j >= len(lines) || !strings.HasPrefix(lines[j], firstLine) {
			continue
		}
		for k := j; k < len(lines); k++ {
			if strings.TrimSpace(lines[k]) == "```" {
				return strings.Join(lines[j:k], "\n")
			}
		}
	}
	t.Fatalf("no fenced block starting with %q", firstLine)
	return ""
}

func TestSessionPathsAreDeterministicUnderDataDir(t *testing.T) {
	dir := SessionDir("/data", "groom-1")
	if want := filepath.Join("/data", "grooming-sessions", "groom-1"); dir != want {
		t.Fatalf("SessionDir = %q, want %q", dir, want)
	}
	if got, want := ReportPath("/data", "groom-1"), filepath.Join(dir, "report.md"); got != want {
		t.Fatalf("ReportPath = %q, want %q", got, want)
	}
	if got, want := DeferredQuestionsPath("/data", "groom-1"), filepath.Join(dir, "deferred-questions.md"); got != want {
		t.Fatalf("DeferredQuestionsPath = %q, want %q", got, want)
	}
}

// TestKickoffNamesScopeAndOutputs pins the acceptance contract: the kickoff the
// session prompt carries names every scoped item and the absolute output paths,
// so the harness never has to guess where to write.
func TestKickoffNamesScopeAndOutputs(t *testing.T) {
	report := filepath.Join("data", "grooming-sessions", "groom-1", "report.md")
	deferred := filepath.Join("data", "grooming-sessions", "groom-1", "deferred-questions.md")
	kick := Kickoff("factotum", []ScopeItem{
		{ID: "t-1", Kind: "task", Title: "Add widget"},
		{ID: "t-2", Kind: "idea", Title: "Maybe cache"},
	}, report, deferred)

	for _, want := range []string{"factotum", "t-1", "Add widget", "t-2", "Maybe cache", report, deferred} {
		if !strings.Contains(kick, want) {
			t.Errorf("kickoff does not name %q:\n%s", want, kick)
		}
	}
}

// TestKickoffStatesSectionContract pins the templates' section contract into the
// kickoff, in order, so the harness writes files whose headings match the
// versioned templates.
func TestKickoffStatesSectionContract(t *testing.T) {
	kick := Kickoff("p", nil, "/r", "/d")
	sections := append(append([]string{}, ReportSections()...), DeferredQuestionsSections()...)
	last := -1
	for _, section := range sections {
		idx := strings.Index(kick, section)
		if idx < 0 {
			t.Fatalf("kickoff does not state section %q:\n%s", section, kick)
		}
		if idx <= last {
			t.Fatalf("kickoff section %q is out of order:\n%s", section, kick)
		}
		last = idx
	}
}

// sectionTitles returns the level-2 section titles of a markdown document, in
// order.
func sectionTitles(body string) []string {
	var out []string
	for _, raw := range strings.Split(body, "\n") {
		line := strings.TrimRight(raw, " \t")
		if strings.HasPrefix(line, "## ") {
			out = append(out, strings.TrimSpace(strings.TrimPrefix(line, "## ")))
		}
	}
	return out
}
