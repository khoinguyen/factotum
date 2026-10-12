package skills

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestShippedSkillsNameNoFactotumMiseTasks pins that no shipped skill — embedded
// usage skills or committed agent skills (all of .agents/skills/*) — hardcodes
// factotum's own mise tasks. ft is a general factory, so a skill that names
// `mise run install` or `mise run build`/`build-web` reads as factotum's
// repository and breaks in every other project. Tool-install and build guidance
// must be generic or read from config, never a concrete task name.
func TestShippedSkillsNameNoFactotumMiseTasks(t *testing.T) {
	forbidden := []string{"mise run install", "mise run build"}

	all, err := All()
	if err != nil {
		t.Fatalf("All() error = %v", err)
	}
	for _, skill := range all {
		body := strings.ToLower(skill.Body)
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("embedded skill %q hardcodes %q; keep tool-install/build guidance generic or config-driven",
					skill.Name, bad)
			}
		}
	}

	agents, err := filepath.Glob(filepath.Join(repoRoot(t), ".agents", "skills", "*", "SKILL.md"))
	if err != nil {
		t.Fatalf("glob agent skills: %v", err)
	}
	if len(agents) == 0 {
		t.Fatal("no agent skills found under .agents/skills")
	}
	for _, path := range agents {
		rel, err := filepath.Rel(repoRoot(t), path)
		if err != nil {
			t.Fatalf("rel path %s: %v", path, err)
		}
		body := agentSkill(t, rel)
		for _, bad := range forbidden {
			if strings.Contains(body, bad) {
				t.Errorf("agent skill %s hardcodes %q; keep tool-install/build guidance generic or config-driven",
					rel, bad)
			}
		}
	}
}

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
