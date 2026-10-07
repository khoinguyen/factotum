package groom

import (
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
