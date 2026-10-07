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
