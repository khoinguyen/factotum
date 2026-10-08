package skills

import (
	"strings"
	"testing"
)

// TestEmbeddedSkillsNameNoConcreteProject pins that every embedded usage skill
// stays project-agnostic: ft builds other software, so a skill must take the
// <project> placeholder (or the configured default), never a concrete id. A
// hardcoded project would silently point a second project's commands at the
// wrong store.
func TestEmbeddedSkillsNameNoConcreteProject(t *testing.T) {
	all, err := All()
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	forbidden := []struct {
		why  string
		text string
	}{
		{"hardcodes the factotum project id as a project flag", "-p factotum"},
		{"hardcodes the factotum project id as a project flag", "--project factotum"},
		{"names a concrete project in prose", "Factotum project"},
		{"assumes the reader is in the factotum repo", "here under `.agents/skills/"},
	}
	for _, skill := range all {
		for _, bad := range forbidden {
			if strings.Contains(skill.Body, bad.text) {
				t.Errorf("embedded skill %q %s (%q); keep it project-agnostic", skill.Name, bad.why, bad.text)
			}
		}
	}
}
