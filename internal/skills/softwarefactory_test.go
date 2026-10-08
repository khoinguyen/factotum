package skills

import (
	"strings"
	"testing"
)

// TestSoftwareFactorySkillIsEmbedded pins that the factory recipe is an embedded
// skill served by `ft skill get software-factory`: the manual end-to-end recipe
// an agent is pointed at with "load the software-factory skill and start working".
func TestSoftwareFactorySkillIsEmbedded(t *testing.T) {
	skill, err := Get("software-factory")
	if err != nil {
		t.Fatalf("Get(software-factory) error = %v", err)
	}
	if strings.TrimSpace(skill.Description) == "" {
		t.Fatal("software-factory skill has no description")
	}
	if !strings.Contains(skill.Body, "#") {
		t.Fatalf("software-factory body is not markdown:\n%s", skill.Body)
	}
}

// TestSoftwareFactorySkillTeachesEndToEndLoop pins the recipe's shape: it lists
// the whole loop, in order, from capture to rollout. The ordered markers are the
// numbered phase headings, so a reordering or a dropped phase fails.
func TestSoftwareFactorySkillTeachesEndToEndLoop(t *testing.T) {
	skill, err := Get("software-factory")
	if err != nil {
		t.Fatalf("Get(software-factory) error = %v", err)
	}
	phases := []string{
		"1. **Capture**",
		"2. **Groom**",
		"3. **Architecture review**",
		"4. **Breakdown**",
		"5. **Build**",
		"6. **Review**",
		"7. **QA**",
		"8. **Rollout**",
	}
	last := -1
	for _, phase := range phases {
		idx := strings.Index(skill.Body, phase)
		if idx < 0 {
			t.Fatalf("software-factory body does not list phase %q", phase)
		}
		if idx <= last {
			t.Fatalf("phase %q is out of order in the software-factory body", phase)
		}
		last = idx
	}
}

// TestSoftwareFactorySkillTiesExistingSkills pins that the recipe delegates to
// the existing role skills rather than restating them: groom, the architecture
// reviewer, the chief loop's builder/reviewer, and QA.
func TestSoftwareFactorySkillTiesExistingSkills(t *testing.T) {
	skill, err := Get("software-factory")
	if err != nil {
		t.Fatalf("Get(software-factory) error = %v", err)
	}
	body := strings.ToLower(skill.Body)
	for _, want := range []string{
		"groom", "architecture-reviewer", "chief",
		"single-task-builder", "single-task-reviewer", "single-task-qa",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("software-factory body does not tie in the %q skill", want)
		}
	}
}

// TestSoftwareFactorySkillUsesExactFTCommands pins the exact commands a harness
// runs for the loop, so the recipe stays a runnable transcript rather than prose.
func TestSoftwareFactorySkillUsesExactFTCommands(t *testing.T) {
	skill, err := Get("software-factory")
	if err != nil {
		t.Fatalf("Get(software-factory) error = %v", err)
	}
	for _, want := range []string{
		"ft idea create", "ft bug create", "ft groom", "ft groom review",
		"ft task next", "ft task create", "ft task note create",
		"ft graph render", "ft task done", "ft milestone create",
	} {
		if !strings.Contains(skill.Body, want) {
			t.Errorf("software-factory body does not use the command %q", want)
		}
	}
}

// TestSoftwareFactorySkillIsProjectAgnostic pins that the recipe names no
// concrete project: every command takes a project placeholder (or a default),
// never a hardcoded id, so it works for any project.
func TestSoftwareFactorySkillIsProjectAgnostic(t *testing.T) {
	skill, err := Get("software-factory")
	if err != nil {
		t.Fatalf("Get(software-factory) error = %v", err)
	}
	for _, forbidden := range []string{"-p factotum", "--project factotum"} {
		if strings.Contains(skill.Body, forbidden) {
			t.Errorf("software-factory body hardcodes a project (%q); keep it project-agnostic", forbidden)
		}
	}
	if !strings.Contains(skill.Body, "<project>") {
		t.Error("software-factory body does not show the <project> placeholder")
	}
}
