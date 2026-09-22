package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/skills"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/judge"
)

func TestSkillReferenceTokensIncludeCommandsFlagsAndProse(t *testing.T) {
	root := skillDriftRoot(t)
	body := "Use `ft task get <task> --fields checks`.\n" +
		"Then run ft task frobnicate to continue.\n" +
		"Pass `--no-rerank` for the lexical order.\n"
	got := skillReferenceTokens(root, body)
	for _, want := range []string{"task", "get", "--fields", "frobnicate", "--no-rerank"} {
		if !containsToken(got, want) {
			t.Fatalf("skillReferenceTokens() = %v, want %q", got, want)
		}
	}
	for _, unwanted := range []string{"checks", "<task>", "continue"} {
		if containsToken(got, unwanted) {
			t.Fatalf("skillReferenceTokens() = %v, must not include %q", got, unwanted)
		}
	}
}

func TestCommandInventoryListsCommandsAndFlags(t *testing.T) {
	inv := commandInventory(skillDriftRoot(t))
	for _, want := range []string{"ft task", "ft task next", "ft skill lint"} {
		if !containsToken(inv.Commands, want) {
			t.Fatalf("inventory commands = %v, want %q", inv.Commands, want)
		}
	}
	for _, want := range []string{"--force", "--fields", "--check", "-n"} {
		if !containsToken(inv.Flags, want) {
			t.Fatalf("inventory flags = %v, want %q", inv.Flags, want)
		}
	}
}

func TestRunSkillLintDeterministicReportsStaleReference(t *testing.T) {
	root := skillDriftRoot(t)
	report, err := runSkillLint(context.Background(), nil, root, []skills.Skill{
		{Name: "ft", Body: "```sh\nft task frobnicate\n```\n"},
	}, false)
	if err != nil {
		t.Fatalf("runSkillLint() error = %v", err)
	}
	if len(report.Findings) != 1 || !strings.Contains(report.Findings[0], "frobnicate") {
		t.Fatalf("findings = %v, want the stale subcommand", report.Findings)
	}
	if len(report.Flags) != 0 {
		t.Fatalf("flags = %v, want none without --semantic", report.Flags)
	}
}

func TestRunSkillLintSemanticFlagsRemovedCommandInProse(t *testing.T) {
	root := skillDriftRoot(t)
	report, err := runSkillLint(context.Background(), inventoryJudge{}, root, []skills.Skill{
		{Name: "ft", Body: "Then run ft task frobnicate to continue.\n"},
	}, true)
	if err != nil {
		t.Fatalf("runSkillLint() error = %v", err)
	}
	if len(report.Flags) != 1 || report.Flags[0].Token != "frobnicate" || report.Flags[0].Skill != "ft" {
		t.Fatalf("flags = %+v, want frobnicate flagged", report.Flags)
	}
	if report.Flags[0].Probability >= 0.5 {
		t.Fatalf("probability = %v, want below the floor", report.Flags[0].Probability)
	}
}

func TestRunSkillLintSemanticWithoutJudgeStillReportsDeterministic(t *testing.T) {
	root := skillDriftRoot(t)
	report, err := runSkillLint(context.Background(), judge.Disabled{}, root, []skills.Skill{
		{Name: "ft", Body: "```sh\nft task frobnicate\n```\n"},
	}, true)
	if err != nil {
		t.Fatalf("runSkillLint() error = %v", err)
	}
	if !report.Unavailable {
		t.Fatal("report.Unavailable = false, want true without a judge")
	}
	if len(report.Findings) != 1 || !strings.Contains(report.Findings[0], "frobnicate") {
		t.Fatalf("findings = %v, want the deterministic reference to still run", report.Findings)
	}
	if len(report.Flags) != 0 {
		t.Fatalf("flags = %v, want none when the judge is unavailable", report.Flags)
	}
}

func TestRunSkillLintValidSkillIsClean(t *testing.T) {
	root := skillDriftRoot(t)
	report, err := runSkillLint(context.Background(), inventoryJudge{}, root, []skills.Skill{
		{Name: "ft", Body: "Run `ft task next` and pass `--no-rerank`.\n"},
	}, true)
	if err != nil {
		t.Fatalf("runSkillLint() error = %v", err)
	}
	if len(report.Findings) != 0 || len(report.Flags) != 0 || report.Unavailable {
		t.Fatalf("report = %+v, want a clean result", report)
	}
}

func TestSkillLintCommandOnEmbeddedSkills(t *testing.T) {
	if out := newRunner(t).run("skill", "lint"); strings.TrimSpace(out) != "" {
		t.Fatalf("skill lint on valid embedded skills printed findings:\n%s", out)
	}
}

func TestRunSkillLintSemanticIsCleanOnEmbeddedSkills(t *testing.T) {
	root := skillDriftRoot(t)
	all, err := skills.All()
	if err != nil {
		t.Fatalf("skills.All() error = %v", err)
	}
	report, err := runSkillLint(context.Background(), inventoryJudge{}, root, all, true)
	if err != nil {
		t.Fatalf("runSkillLint() error = %v", err)
	}
	if len(report.Findings) != 0 || len(report.Flags) != 0 || report.Unavailable {
		t.Fatalf("embedded skills flagged: findings=%v flags=%+v", report.Findings, report.Flags)
	}
}

func TestSkillLintSemanticCommandNamesUnavailableJudge(t *testing.T) {
	r := newRunner(t)
	_, stderr := r.runSplit("skill", "lint", "--semantic")
	if !strings.Contains(strings.ToLower(stderr), "unavailable") {
		t.Fatalf("stderr = %q, want the semantic-unavailable message", stderr)
	}
}

func TestSkillLintSemanticCommandExitsNonZeroOnDrift(t *testing.T) {
	r := newRunner(t)
	r.judge = allStaleJudge{}
	if err := r.runErr("skill", "lint", "--semantic"); !errors.Is(err, errSkillDrift) {
		t.Fatalf("error = %v, want errSkillDrift", err)
	}
}

// inventoryJudge treats a token as real when the code-built inventory knows it,
// so a test proves every candidate token in a valid skill is a real command or
// flag without a network.
type inventoryJudge struct{}

func (inventoryJudge) Ask(_ context.Context, req judge.Request) (judge.Response, error) {
	inventory, _ := req.State.(map[string]any)["inventory"].(app.SkillInventory)
	known := map[string]bool{}
	for _, command := range inventory.Commands {
		fields := strings.Fields(command)
		known[fields[len(fields)-1]] = true
	}
	for _, flag := range inventory.Flags {
		known[flag] = true
	}
	answers := make(map[string]judge.Answer, len(req.Questions))
	for token := range req.Questions {
		probability := 0.05
		if known[token] {
			probability = 0.95
		}
		answers[token] = judge.Answer{Probability: probability, Confidence: 0.9}
	}
	return judge.Response{Answers: answers}, nil
}

// allStaleJudge flags every candidate token, so a test can drive the command's
// non-zero exit without depending on the embedded skill's contents.
type allStaleJudge struct{}

func (allStaleJudge) Ask(_ context.Context, req judge.Request) (judge.Response, error) {
	answers := make(map[string]judge.Answer, len(req.Questions))
	for token := range req.Questions {
		answers[token] = judge.Answer{Probability: 0.0, Confidence: 1.0}
	}
	return judge.Response{Answers: answers}, nil
}

func containsToken(tokens []string, want string) bool {
	for _, token := range tokens {
		if token == want {
			return true
		}
	}
	return false
}
