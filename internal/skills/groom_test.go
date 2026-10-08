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

// TestGroomSkillCompletionLeavesAgentReadyTasks pins the completion contract:
// a session is not done until the work it produces is groomed and assigned, so
// it lands in the agent bucket. Session 1 deferred everything and left the work
// human-next, which this rule prevents.
func TestGroomSkillCompletionLeavesAgentReadyTasks(t *testing.T) {
	skill, err := Get("groom")
	if err != nil {
		t.Fatalf("Get(groom) error = %v", err)
	}
	body := strings.ToLower(skill.Body)
	for _, want := range []string{"completed session", "groomed", "assigned", "agent-ready"} {
		if !strings.Contains(body, want) {
			t.Errorf("groom skill body does not mention %q", want)
		}
	}
}

// TestGroomSkillNamesDurablePrompt pins that the session is launched from the
// committed repo data file, not an ephemeral temp path.
func TestGroomSkillNamesDurablePrompt(t *testing.T) {
	skill, err := Get("groom")
	if err != nil {
		t.Fatalf("Get(groom) error = %v", err)
	}
	if !strings.Contains(skill.Body, groom.PromptPath) {
		t.Errorf("groom skill does not reference the durable session prompt %q", groom.PromptPath)
	}
}

// TestGroomSkillDocumentsKickoffAndOutputs pins the `ft groom` surface: the
// skill names the command, the kickoff it injects, and the two durable outputs
// it captures under the session path.
func TestGroomSkillDocumentsKickoffAndOutputs(t *testing.T) {
	skill, err := Get("groom")
	if err != nil {
		t.Fatalf("Get(groom) error = %v", err)
	}
	body := strings.ToLower(skill.Body)
	for _, want := range []string{"ft groom", "kickoff", "grooming-sessions", "report.md", "deferred-questions.md"} {
		if !strings.Contains(body, strings.ToLower(want)) {
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

// TestGroomSkillDocumentsFeatureDocs pins the feature-document contract: the
// skill names the three templates, their file names, and each section set in
// order, so a session and its architecture review share one contract.
func TestGroomSkillDocumentsFeatureDocs(t *testing.T) {
	skill, err := Get("groom")
	if err != nil {
		t.Fatalf("Get(groom) error = %v", err)
	}
	body := strings.ToLower(skill.Body)
	for _, want := range []string{"spec.md", "plan.md", "tech-design.md", "feature documents", "architecture review"} {
		if !strings.Contains(body, want) {
			t.Errorf("groom skill body does not mention %q", want)
		}
	}
	for name, sections := range map[string][]string{
		"spec":        groom.SpecSections(),
		"plan":        groom.PlanSections(),
		"tech design": groom.TechDesignSections(),
	} {
		last := -1
		for _, section := range sections {
			idx := strings.Index(skill.Body, section)
			if idx < 0 {
				t.Fatalf("groom skill body does not document %s section %q", name, section)
			}
			if idx <= last {
				t.Fatalf("%s section %q is out of order in the groom skill", name, section)
			}
			last = idx
		}
	}
}
