package skills

import (
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/groom"
)

func TestGroomSkillDocumentsProtocol(t *testing.T) {
	skill, err := Get("groom")
	if err != nil {
		t.Fatalf("Get(groom) error = %v", err)
	}
	if strings.TrimSpace(skill.Description) == "" {
		t.Fatal("groom skill has no description")
	}
	for _, want := range []string{
		"team lead", "product owner", "defer", "acceptance criterion",
		"new idea", "new task", "1 task = 1 PR", "DAG",
	} {
		if !strings.Contains(strings.ToLower(skill.Body), strings.ToLower(want)) {
			t.Errorf("groom skill body does not mention %q", want)
		}
	}
}

func TestGroomSkillListsReportSectionsInOrder(t *testing.T) {
	skill, err := Get("groom")
	if err != nil {
		t.Fatalf("Get(groom) error = %v", err)
	}
	last := -1
	for _, section := range groom.ReportSections() {
		idx := strings.Index(skill.Body, section)
		if idx < 0 {
			t.Fatalf("groom skill body does not document report section %q", section)
		}
		if idx <= last {
			t.Fatalf("report section %q is out of order in the groom skill", section)
		}
		last = idx
	}
}
