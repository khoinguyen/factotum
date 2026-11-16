package skills

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
// the pair in; the builder's ft msg handoff is what starts the review.
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

// TestLoopSkillsMessageOverFtMsg guards the channel migration and the robust
// addressing form: the builder, reviewer, and chief message each other over
// ft msg using the canonical actor:<role> address (a bare role resolves
// fuzzily across actor/run/task and can misroute or drop, t-fpd56yebqt). A
// regression to hand-crafted cmux targets, or to a bare role address, is a
// failure.
func TestLoopSkillsMessageOverFtMsg(t *testing.T) {
	cases := []struct {
		skill string
		texts []string
	}{
		{".agents/skills/single-task-builder/SKILL.md", []string{
			"ft msg send actor:reviewer-<task-id>", "ft msg send actor:chief", "actor:builder-<task-id>",
		}},
		{".agents/skills/single-task-reviewer/SKILL.md", []string{
			"ft msg send actor:builder-<task-id>", "ft msg send actor:chief", "actor:reviewer-<task-id>",
		}},
		{".agents/skills/chief/SKILL.md", []string{
			"ft msg send actor:builder-", "ft msg send actor:reviewer-", "loop-agent.sh",
		}},
		{".agents/skills/architecture-reviewer/SKILL.md", []string{
			"ft msg send actor:chief", "actor:architecture-reviewer-<session>",
		}},
	}
	for _, tc := range cases {
		body := agentSkill(t, tc.skill)
		for _, text := range tc.texts {
			if !strings.Contains(body, text) {
				t.Errorf("%s must reference %q (loop agents talk over ft msg addressed actor:<role>)", tc.skill, text)
			}
		}
	}
}

// TestLoopSkillsHaveNoCmuxFallback guards the retirement of the cmux paste
// transport: a real loop-agent pair round-tripped over ft msg (t-wp27miss7i),
// so the wrapper is gone and no agent instruction may still point at it. A dead
// receiver must fail loudly through `ft msg runs --live`, not type into a cmux
// terminal where a misroute is silent. Both shipped instruction trees are
// checked: .agents/skills (the loop roles) and internal/skills/content (served
// by `ft skill`).
func TestLoopSkillsHaveNoCmuxFallback(t *testing.T) {
	root := repoRoot(t)
	for _, gone := range []string{
		".agents/skills/chief/scripts/cmux-msg.sh",
		".agents/skills/chief/scripts/cmux-msg_test.sh",
	} {
		if _, err := os.Stat(filepath.Join(root, gone)); !os.IsNotExist(err) {
			t.Errorf("%s must be deleted: ft msg is the only channel and a paste fallback hides a dead receiver", gone)
		}
	}
	for _, dir := range []string{
		filepath.Join(".agents", "skills"),
		filepath.Join("internal", "skills", "content"),
	} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(strings.ToLower(string(data)), "cmux-msg") {
				rel, relErr := filepath.Rel(root, path)
				if relErr != nil {
					rel = path
				}
				t.Errorf("%s still references cmux-msg; peers talk over ft msg only", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	// Every reporting role must say what to do when the channel is dead.
	for _, skill := range []string{
		".agents/skills/chief/SKILL.md",
		".agents/skills/single-task-builder/SKILL.md",
		".agents/skills/single-task-reviewer/SKILL.md",
		".agents/skills/single-task-qa/SKILL.md",
		".agents/skills/grooming-agent/SKILL.md",
		".agents/skills/architecture-reviewer/SKILL.md",
	} {
		if body := agentSkill(t, skill); !strings.Contains(body, "no cmux fallback") {
			t.Errorf("%s must say there is no cmux fallback when the peer is unreachable", skill)
		}
	}
}

// TestChiefSkillNeverPollsMsgInbox guards the messaging discipline: the chief
// must not poll `ft msg inbox` in a loop. The ft msg receiver delivers messages
// to the agent as they arrive; a long blocking inbox poll holds the endpoint and
// delays that delivery (observed twice), so the chief sends, then stops and
// waits for the receiver to wake it.
func TestChiefSkillNeverPollsMsgInbox(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"forbid polling the inbox in a loop", "never poll `ft msg inbox` in a loop"},
		{"say the receiver delivers messages as they arrive", "delivers each message to you as it arrives"},
		{"state the blocking poll delays delivery", "delays that delivery"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill must %s (missing %q); polling the inbox stalls receiver delivery otherwise", req.why, req.text)
		}
	}
}

// agentSkillRaw reads a committed agent skill without lowercasing it, for tests
// that evaluate a documented shell pattern rather than prose.
func agentSkillRaw(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read agent skill %s: %v", rel, err)
	}
	return string(data)
}

// documentedPgrepPatterns returns every `pgrep -f "<pattern>"` argument the
// chief skill documents, so a test can hold each one to the wrapper's real
// launch argv.
func documentedPgrepPatterns(body string) []string {
	var patterns []string
	for _, m := range regexp.MustCompile(`pgrep -f "([^"]+)"`).FindAllStringSubmatch(body, -1) {
		patterns = append(patterns, m[1])
	}
	return patterns
}

// loopAgentArgv synthesizes the command line loop-agent.sh execs, with kickoff
// standing in for the prompt. It derives the arguments from the script's exec
// line so a wrapper change that invalidates the documented kill pattern fails
// the guard instead of stranding an agent.
func loopAgentArgv(t *testing.T, kickoff string) string {
	t.Helper()
	script := agentSkillRaw(t, ".agents/skills/chief/scripts/loop-agent.sh")
	m := regexp.MustCompile(`(?m)^exec (.+)$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("loop-agent.sh must have an exec line")
	}
	var argv []string
	for _, token := range strings.Fields(m[1]) {
		switch token = strings.Trim(token, `"`); {
		case token == "$opencode_bin":
			argv = append(argv, "opencode")
		case token == "$prompt":
			argv = append(argv, kickoff)
		case strings.HasPrefix(token, "--"):
			argv = append(argv, token)
		default:
			t.Fatalf("loop-agent.sh exec token %q is not understood by this test; teach loopAgentArgv about it so the documented kill pattern stays checked against the real launch", token)
		}
	}
	return strings.Join(argv, " ")
}

// TestChiefSkillKillPatternMatchesLoopAgentLaunch guards the pair-retire kill
// (t-n7mx7bob3m): the documented `pgrep -f` pattern must match the process
// loop-agent.sh actually starts. The pre-fix pattern assumed `opencode
// --prompt`, but the wrapper execs `opencode --standalone --prompt ...`, so it
// matched nothing and the agent survived a retire — the workspace close then
// refused with "Workspace has a running process". Every documented pattern is
// evaluated, so a second stale copy cannot hide.
func TestChiefSkillKillPatternMatchesLoopAgentLaunch(t *testing.T) {
	body := agentSkillRaw(t, ".agents/skills/chief/SKILL.md")
	patterns := documentedPgrepPatterns(body)
	if len(patterns) == 0 {
		t.Fatal("chief skill must document the pair-retire pgrep pattern")
	}
	const task = "t-n7mx7bob3m"
	// The role-tagged task id lives in the kickoff prompt, the only part of the
	// wrapper's argv that identifies the pair; this is the launch the skill
	// documents for a builder.
	kickoff := "load the single-task-builder skill; you are builder-" + task + "; work in /tmp/ft-" + task
	argv := loopAgentArgv(t, kickoff)
	for _, pattern := range patterns {
		re := regexp.MustCompile(strings.NewReplacer("<task-id>", task, "<t>", task).Replace(pattern))
		if !re.MatchString(argv) {
			t.Errorf("chief skill documents pgrep -f %q, which matches nothing on the wrapper's launch %q; a retire would leave the agent alive", pattern, argv)
		}
	}
}

// TestChiefSkillNamesTheRefusedCloseAsAMissedKill guards the diagnostic the
// pair-retire failure had to teach: a workspace close that still refuses with
// "Workspace has a running process" means the kill missed its process, so the
// chief re-runs the kill instead of assuming the agents are gone.
func TestChiefSkillNamesTheRefusedCloseAsAMissedKill(t *testing.T) {
	body := strings.Join(strings.Fields(agentSkill(t, ".agents/skills/chief/SKILL.md")), " ")
	for _, want := range []string{"workspace has a running process", "means a kill was missed"} {
		if !strings.Contains(body, want) {
			t.Errorf("chief skill must contain %q (a refused close is the signal a kill was missed)", want)
		}
	}
}

// TestChiefSkillDesignReviewDispatchGetsItsOwnWorktree guards the architecture
// reviewer's dispatch: like every loop agent it runs in its own worktree, so the
// bullet must create one before the wrapper is launched (a missing worktree
// leaves loop-agent.sh with no directory to run in).
func TestChiefSkillDesignReviewDispatchGetsItsOwnWorktree(t *testing.T) {
	body := strings.Join(strings.Fields(agentSkill(t, ".agents/skills/chief/SKILL.md")), " ")
	const start = "**design review"
	i := strings.Index(body, start)
	if i < 0 {
		t.Fatalf("chief skill must document %q dispatch", start)
	}
	section := body[i:]
	if next := strings.Index(section[1:], "**any new team-member role"); next >= 0 {
		section = section[:next+1]
	}
	for _, want := range []string{"git worktree add", "/tmp/arch-<session>", "loop-agent.sh architecture-reviewer-"} {
		if !strings.Contains(section, want) {
			t.Errorf("design-review dispatch must contain %q (the architecture reviewer needs its own worktree before the wrapper launches)", want)
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

// TestChiefSkillRequiresAnOwnDirectoryPerAgent guards the launch-directory rule
// (t-ezt7hvgtkx): OpenCode attaches a TUI started in a directory that already
// hosts one as a tab of that instance, inheriting its env, so the new role's
// FACTOTUM_* never reach the receiver and a plain launch there is silently
// unreachable. The skill must send every agent — and the chief — to its own
// directory, and must say that loop-agent.sh reports such a directory without
// blocking the launch (a --standalone launch keeps its own env; refusing would
// strand a working agent).
func TestChiefSkillRequiresAnOwnDirectoryPerAgent(t *testing.T) {
	body := strings.Join(strings.Fields(agentSkill(t, ".agents/skills/chief/SKILL.md")), " ")
	required := []struct {
		why  string
		text string
	}{
		{"give the chief its own directory", "the chief keeps its own directory"},
		{"state that a launch in a hosted directory attaches as a tab", "attaches to the running one as a tab"},
		{"name the inherited env as the failure", "inherits *that* instance's env"},
		{"say the wrapper notes, and does not block, a hosted directory", "prints a note when the directory is"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill must %s (missing %q); the agent is otherwise silently unreachable over ft msg", req.why, req.text)
		}
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
		{"report the verdict to the chief over ft msg", "ft msg send actor:chief"},
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

// TestArchitectureReviewerSkillMakesTechDebtStanding guards the standing
// tech-debt pass: every review, not just a convenient one, names each refactor
// or architecture-hardening item with a rationale and a proposed refactor, and
// says so explicitly when there is none. The chief files the tasks; the
// reviewer only recommends.
func TestArchitectureReviewerSkillMakesTechDebtStanding(t *testing.T) {
	body := agentSkill(t, ".agents/skills/architecture-reviewer/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"run the tech-debt pass every review", "standing tech-debt"},
		{"apply it every time", "every review"},
		{"name each item's rationale", "rationale"},
		{"name each item's proposed refactor", "proposed refactor"},
		{"require an explicit none found", "none found"},
		{"hand the items to the chief to file", "chief files"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("architecture-reviewer skill must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestChiefSkillFilesTechDebtTasksEveryCycle pins the chief's half of the
// standing step: when the architecture review surfaces tech-debt items, the
// chief turns each into a task carrying the rationale and proposed refactor,
// rather than letting the list go stale in the review.
func TestChiefSkillFilesTechDebtTasksEveryCycle(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name the tech-debt step", "tech-debt"},
		{"make it standing", "every cycle"},
		{"file each item as a task", "ft task create"},
		{"carry the rationale and proposed refactor into the task", "proposed refactor"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestChiefSkillHandsTheTechDebtPassToTheReviewer pins the division of labour
// the #402 review flagged: the architecture reviewer runs the standing tech-debt
// pass and the chief files what it recommends. The chief must not be told to
// carry out the pass itself, and it must not refile an item the groom session's
// own report already filed as a task.
func TestChiefSkillHandsTheTechDebtPassToTheReviewer(t *testing.T) {
	body := strings.Join(strings.Fields(agentSkill(t, ".agents/skills/chief/SKILL.md")), " ")
	if strings.Contains(body, "carry out the pass") {
		t.Error("chief skill invites the chief to carry out the tech-debt pass itself; the architecture reviewer runs it")
	}
	required := []struct {
		why  string
		text string
	}{
		{"have the reviewer run the pass", "architecture reviewer runs"},
		{"say the chief files the result", "you file the result"},
		{"file only debt no task already carries", "no task carries"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestGroomingAgentSkillReadsPromptAndLinksCaptures guards the grooming-agent
// charter: it reads a prompt doc artifact, captures one or more ideas/bugs from
// it, links each capture back to the artifact as its origin, and reports back to
// the chief over ft msg. It also pins the link-back contract: origin_artifact is
// canonical and is what the web prompt page and toast read, so the skill must
// never fall back to a note doc-link.
func TestGroomingAgentSkillReadsPromptAndLinksCaptures(t *testing.T) {
	body := agentSkill(t, ".agents/skills/grooming-agent/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name itself", "grooming-agent"},
		{"read the prompt artifact", "ft doc get"},
		{"capture ideas", "ft idea create"},
		{"capture bugs", "ft bug create"},
		{"link the capture back to the artifact", "--origin-artifact"},
		{"name origin_artifact as the canonical link-back", "canonical link-back"},
		{"name the read surface the origin drives", "/api/notifications"},
		{"forbid a note doc-link substitute", "not a substitute"},
		{"report to the chief over ft msg", "ft msg send actor:chief"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("grooming-agent skill must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestChiefSkillDispatchesGroomingAgent guards the loop wiring: the chief can
// spawn the grooming-agent role on a captured prompt artifact with a short
// kickoff, in its own worktree/workspace.
func TestChiefSkillDispatchesGroomingAgent(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name the grooming-agent role", "grooming-agent"},
		{"name the prompt artifact it consumes", "prompt artifact"},
		{"dispatch it through the loop wrapper", "loop-agent.sh grooming-agent"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill must %s (missing %q)", req.why, req.text)
		}
	}
}

// TestChiefSkillDispatchesArchitectureReviewThroughLoopWrapper guards the
// residual from the #325 review: the architecture reviewer must be launched
// through the same loop wrapper as builders/reviewers/QA, in its own worktree,
// so it registers as its own actor (architecture-reviewer-<session>) with an ft
// msg receiver. A bare `opencode --prompt` inherits the chief's default actor,
// leaving the role unaddressable and the role grammar unused.
func TestChiefSkillDispatchesArchitectureReviewThroughLoopWrapper(t *testing.T) {
	body := agentSkill(t, ".agents/skills/chief/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"launch it through the loop wrapper", "loop-agent.sh architecture-reviewer-"},
		{"give it its own worktree", "own worktree"},
		{"keep the kickoff to one short line", "short kickoff"},
		{"name the address it is reachable at", "actor:architecture-reviewer-<session>"},
		{"say a bare opencode prompt would inherit the chief's actor", "bare `opencode --prompt`"},
	}
	for _, req := range required {
		if !strings.Contains(body, req.text) {
			t.Errorf("chief skill must %s (missing %q)", req.why, req.text)
		}
	}
	if strings.Contains(body, `opencode --prompt "load the architecture-reviewer`) {
		t.Errorf("chief skill still dispatches the architecture reviewer with a bare `opencode --prompt`; it has no receiver and inherits the chief's default actor")
	}
}

// TestArchitectureReviewerSkillIsAddressableOverFtMsg guards the reviewer's
// side of that dispatch: launched through the wrapper it is a first-class ft msg
// peer, so it must message the chief as actor:architecture-reviewer-<session>
// over ft msg and report a dead channel instead of pasting into cmux.
func TestArchitectureReviewerSkillIsAddressableOverFtMsg(t *testing.T) {
	body := agentSkill(t, ".agents/skills/architecture-reviewer/SKILL.md")
	required := []struct {
		why  string
		text string
	}{
		{"name the wrapper that gives it the receiver", "loop-agent.sh"},
		{"name its own role as the sender", "actor:architecture-reviewer-<session>"},
		{"message the chief over ft msg", "ft msg send actor:chief"},
		{"report a dead channel instead of pasting", "no cmux fallback"},
		{"stop after sending instead of polling", "stop and wait"},
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
