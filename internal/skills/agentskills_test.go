package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// repoRoot locates the repository root from this test file so tests do not
// depend on the working directory.
func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// agentSkill reads a committed agent skill (.agents/skills/...) relative to the
// repository root.
func agentSkill(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
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

// TestLoopSkillsMessageOverFtMsg guards the channel migration: the builder,
// reviewer, and chief message each other over ft msg by role, and keep the cmux
// wrapper only as a documented rollout fallback. A regression to hand-crafted
// cmux targets is exactly the failure this task exists to fix.
func TestLoopSkillsMessageOverFtMsg(t *testing.T) {
	cases := []struct {
		skill string
		texts []string
	}{
		{".agents/skills/single-task-builder/SKILL.md", []string{
			"ft msg send reviewer-", "ft msg send chief", "actor:builder-<task-id>", "cmux-msg.sh",
		}},
		{".agents/skills/single-task-reviewer/SKILL.md", []string{
			"ft msg send builder-", "ft msg send chief", "actor:reviewer-<task-id>", "cmux-msg.sh",
		}},
		{".agents/skills/chief/SKILL.md", []string{
			"ft msg send builder-", "ft msg send reviewer-", "loop-agent.sh", "cmux-msg.sh",
		}},
	}
	for _, tc := range cases {
		body := agentSkill(t, tc.skill)
		for _, text := range tc.texts {
			if !strings.Contains(body, text) {
				t.Errorf("%s must reference %q (loop agents talk over ft msg, cmux only as fallback)", tc.skill, text)
			}
		}
	}
}

// TestChiefSkillLaunchesAgentsWithReceiver guards the launch contract: loop
// agents are started through loop-agent.sh, which stages the receiver and
// exports the role env, so they load the ft msg receiver instead of an
// unreachable bare OpenCode session.
func TestChiefSkillLaunchesAgentsWithReceiver(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	for _, text := range []string{"loop-agent.sh builder-", "loop-agent.sh reviewer-"} {
		if !strings.Contains(body, text) {
			t.Errorf("chief skill must launch loop agents via %q", text)
		}
	}
}

// TestChiefSkillAnchorsQADispatchToCwd guards the same bug class fixed for
// pair() in t-sca2nign2h: a bare `cmux new-workspace` inherits the FOCUSED
// surface's cwd, so a relative loop-agent.sh path can 404 when another repo is
// focused, leaving a created-but-dead QA workspace. The QA dispatch example must
// pass an explicit --cwd (or an absolute loop-agent path).
func TestChiefSkillAnchorsQADispatchToCwd(t *testing.T) {
	body := strings.Join(strings.Fields(agentSkill(t, ".agents/skills/chief/SKILL.md")), " ")
	const start = "cmux new-workspace --name qa-<t>"
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("chief skill must document the QA dispatch as %q", start)
	}
	rest := body[i:]
	end := strings.Index(rest, "--command")
	if end < 0 {
		t.Fatalf("QA dispatch %q must pass --command", start)
	}
	if opts := rest[:end]; !strings.Contains(opts, "--cwd") {
		t.Errorf("QA dispatch %q must pass an explicit --cwd (a bare new-workspace inherits the focused surface's cwd); got options %q", start, opts)
	}
}

// TestChiefSkillReincarnationTearsDownRetiredChief guards the reincarnation
// teardown: killing only the outgoing chief's PID leaves two orphans — the
// retired session's ft msg receiver (reparented to PID 1, still long-polling
// actor:chief, so it steals the successor's messages) and its cmux surface (its
// login shell lingers). The self-update section must instruct the successor to
// clean up both, through the tested reincarnate-teardown.sh helper.
func TestChiefSkillReincarnationTearsDownRetiredChief(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name the retired receiver", "ft msg agent claim"},
		{"scope the kill to the chief actor", "--actor chief"},
		{"call out the reparent to pid 1", "pid 1"},
		{"name the retired chief's surface", "retired chief's surface"},
		{"close it with close-surface", "close-surface"},
		{"document the last-surface fallback", "last surface"},
		{"invoke the tested teardown helper", "reincarnate-teardown.sh"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill reincarnation teardown must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestRoleSkillsUseProjectVerifyCommand pins that the role skills verify with
// the project's own command instead of hardcoding one project's: a skill that
// hardcodes `mise run ci` silently breaks for every other project. It must
// name `ft config get verify` as the way to read the project's recorded command.
func TestRoleSkillsUseProjectVerifyCommand(t *testing.T) {
	cases := []struct {
		skill     string
		forbidden []string
	}{
		{".agents/skills/single-task-builder/SKILL.md", []string{"mise run ci"}},
		{".agents/skills/single-task-reviewer/SKILL.md", []string{"mise run ci"}},
		{".agents/skills/single-task-qa/SKILL.md", []string{"mise run ci", "mise run build"}},
		{".agents/skills/chief/SKILL.md", []string{"mise run ci"}},
	}
	for _, tc := range cases {
		body := agentSkill(t, tc.skill)
		for _, bad := range tc.forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("%s hardcodes %q; run the project's verify command instead (ft config get verify)", tc.skill, bad)
			}
		}
		if !strings.Contains(body, "ft config get verify") {
			t.Errorf("%s must reference `ft config get verify` to run the project's verification command", tc.skill)
		}
	}
}

// TestQASkillBuildsThenVerifies guards the verify-vs-build wording: QA must
// build the artifact under test (keeping its path) and then verify with the
// project's verification command. The pre-fix wording built the artifact
// "using the project's verification command", conflating verify with build and
// dropping the artifact path.
func TestQASkillBuildsThenVerifies(t *testing.T) {
	const skill = ".agents/skills/single-task-qa/SKILL.md"
	body := strings.Join(strings.Fields(agentSkill(t, skill)), " ")
	if strings.Contains(body, "using the project's verification command") {
		t.Errorf("%s conflates build with verify: it builds the artifact 'using the project's verification command'", skill)
	}
	for _, want := range []string{"artifact path", "ft config get verify", "discover it from"} {
		if !strings.Contains(body, want) {
			t.Errorf("%s must contain %q (build the artifact, keep its path, then verify)", skill, want)
		}
	}
}

// TestVerifySkillsCarryDocsFallback guards the discovery fallback: any skill that
// names `ft config get verify` must also say what to do when the project records
// none — discover the command from the project's own docs. A skill that only
// says `ft config get verify` strands a project that never set it.
func TestVerifySkillsCarryDocsFallback(t *testing.T) {
	check := func(name, body string) {
		body = strings.Join(strings.Fields(body), " ")
		if !strings.Contains(body, "ft config get verify") {
			return
		}
		if !strings.Contains(body, "discover it from") {
			t.Errorf("%s names `ft config get verify` but never says to discover the command from the project's docs when unset", name)
		}
	}
	all, err := All()
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	for _, s := range all {
		check("embedded skill "+s.Name, s.Body)
	}
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
		check(rel, agentSkill(t, rel))
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
