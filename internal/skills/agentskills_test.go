package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// agentSkill reads a committed agent skill (.agents/skills/...) relative to the
// repository root, located from this test file so it does not depend on the
// test's working directory.
func agentSkill(t *testing.T, rel string) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read agent skill %s: %v", rel, err)
	}
	return strings.ToLower(string(data))
}

// TestReviewerSkillWaitsForBuilderHandoff guards the recurring premature-review
// race: the reviewer must idle until the builder sends the handoff and must not
// check the branch or ping the builder before it. The chief's kickoff only wires
// the pair in; the builder's cmux-msg.sh message is the handoff.
func TestReviewerSkillWaitsForBuilderHandoff(t *testing.T) {
	body := agentSkill(t, ".agents/skills/single-task-reviewer/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name the builder's message as the handoff", "wait for the builder's handoff"},
		{"reject the chief's kickoff as the handoff", "not the handoff"},
		{"forbid checking the branch before the handoff", "do not check out the branch"},
		{"forbid pinging the builder before the handoff", "do not ping the builder"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("reviewer skill must %s (missing %q); premature review recurs otherwise", req.why, req.text)
		}
	}
}

// TestChiefSkillNamesBuilderAsHandoffSource guards the reviewer-wiring line: the
// handoff comes from the builder, not from the chief's kickoff.
func TestChiefSkillNamesBuilderAsHandoffSource(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	for _, text := range []string{"handoff comes from", "not the handoff"} {
		if !strings.Contains(body, text) {
			t.Errorf("chief skill reviewer-wiring must say %q (the builder sends the handoff)", text)
		}
	}
}

// TestArchitectureReviewerSkillReviewsDocsNotCode guards the architecture
// reviewer's charter: it reviews a feature's documents (spec, plan, tech design)
// with a project-wide, zoomed-out view and never drops to line-level code. It
// must report inconsistency, contradiction, overcomplication, and weak
// architectural design, judge cross-cutting impact, surface tech-debt/refactor
// items, hand back a tech-design verdict, and report to the chief.
func TestArchitectureReviewerSkillReviewsDocsNotCode(t *testing.T) {
	body := agentSkill(t, ".agents/skills/architecture-reviewer/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name the documents it reviews", "spec, plan, tech design"},
		{"hold the project-wide, zoomed-out view", "cross-cutting impact"},
		{"report inconsistency", "inconsistency"},
		{"report contradiction", "contradiction"},
		{"report overcomplication", "overcomplication"},
		{"report weak architectural design", "weak architectural design"},
		{"surface tech-debt/refactor items", "tech-debt"},
		{"hand back a tech-design verdict", "tech-design verdict"},
		{"refuse to review line-level code", "not line-level code"},
		{"report to the chief over cmux", "cmux-msg.sh"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("architecture-reviewer skill must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestArchitectureReviewerSkillWiresTheGroomingGate guards the grooming wiring:
// dispatched on a session's docs, the reviewer records its findings on the
// origin item and records one verdict with `ft groom review`, whose needs-rework
// verdict blocks the feature from build.
func TestArchitectureReviewerSkillWiresTheGroomingGate(t *testing.T) {
	body := agentSkill(t, ".agents/skills/architecture-reviewer/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name the grooming review dispatch", "grooming review"},
		{"read the session's docs", "ft groom show"},
		{"record findings on the origin item", "origin item"},
		{"record one verdict with ft groom review", "ft groom review"},
		{"name the blocking verdict", "needs-rework"},
		{"state the build gate", "blocks the feature"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("architecture-reviewer skill must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestChiefSkillDispatchesArchitectureReviewAfterGroom guards the loop wiring:
// after a grooming session produces feature docs, the chief dispatches the
// architecture reviewer on them and a failing verdict blocks the build.
func TestChiefSkillDispatchesArchitectureReviewAfterGroom(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"dispatch the architecture reviewer after grooming", "after grooming"},
		{"name the reviewer role", "architecture-reviewer"},
		{"record the verdict with ft groom review", "ft groom review"},
		{"block the build on a failing verdict", "blocks the feature from build"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill must %s (missing %q)", req.why, req.text)
		}
	}
}
