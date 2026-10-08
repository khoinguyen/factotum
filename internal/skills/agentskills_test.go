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
