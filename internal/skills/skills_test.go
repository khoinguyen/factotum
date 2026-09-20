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
