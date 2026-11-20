package skills

import (
	"errors"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

func TestAllListsBuiltinSkills(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	if len(all) == 0 {
		t.Fatal("All() returned no skills")
	}
	found := false
	for _, skill := range all {
		if skill.Name != DefaultName {
			continue
		}
		found = true
		if strings.TrimSpace(skill.Description) == "" {
			t.Fatal("default skill has no description")
		}
		if strings.TrimSpace(skill.Body) == "" {
			t.Fatal("default skill has no body")
		}
	}
	if !found {
		t.Fatalf("All() = %v, want a %q skill", all, DefaultName)
	}
}

func TestGetReturnsMarkdownBody(t *testing.T) {
	skill, err := Get(DefaultName)
	if err != nil {
		t.Fatalf("Get(%q) error = %v", DefaultName, err)
	}
	if !strings.Contains(skill.Body, "#") {
		t.Fatalf("skill body is not markdown:\n%s", skill.Body)
	}
}

func TestGetUnknownSkill(t *testing.T) {
	if _, err := Get("nope"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get(nope) error = %v, want ErrNotFound", err)
	}
}

// TestFTSkillStatesWorkspaceXOR pins the `ft run` workspace rule: a task's
// declared repo, else all the project's repos - an exclusive or, never the
// union. Vague wording ("the task's repositories") once implied the union,
// misleading an agent into expecting an undeclared repo.
func TestFTSkillStatesWorkspaceXOR(t *testing.T) {
	skill, err := Get("ft")
	if err != nil {
		t.Fatalf("Get(ft) error = %v", err)
	}
	for _, want := range []string{"**declared** repo", "all the project's repos", "never a union"} {
		if !strings.Contains(skill.Body, want) {
			t.Errorf("ft skill does not state the XOR workspace rule: missing %q", want)
		}
	}
}

// TestFTSkillStatesLiveRunMeansClaiming pins the rule the channel check rests
// on: a run in `ft msg runs --live` is one renewing its lease by claiming, so a
// receiver that stops claiming drops out and says why in one stall line. Without
// it a chief reads an absent peer as idle instead of stalled and keeps waiting
// on a handoff that can never land. It also pins the one absence that is the
// host's rather than the receiver's: OpenCode V2 unloads an idle location and
// takes the receiver with it, so a chief must read the queued handoff as queued
// and look at `ft msg runs` for the last seen time instead of relaunching a peer
// whose terminal is still up.
func TestFTSkillStatesLiveRunMeansClaiming(t *testing.T) {
	skill, err := Get("ft")
	if err != nil {
		t.Fatalf("Get(ft) error = %v", err)
	}
	for _, want := range []string{"only a receiver that is actually claiming renews its lease", "stall line", "treat a missing peer as stalled, not idle", "read its log for the reason", "unloads a location's services", "queued, not delivered"} {
		if !strings.Contains(skill.Body, want) {
			t.Errorf("ft skill does not state the live-lease rule: missing %q", want)
		}
	}
}
