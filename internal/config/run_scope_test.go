package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestRunProjectScopeOverridesUser pins that the project-scoped [run] table may
// select the harness and sandbox (committable project intent), and that it wins
// over the machine-scoped user file.
func TestRunProjectScopeOverridesUser(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
sandbox = "docker"
harness = "opencode"
`)
	project := writeConfig(t, dir, "project.toml", `
project = "acme"

[run]
sandbox = "openshell"
harness = "pi"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "openshell" {
		t.Fatalf("Run.Sandbox = %q, want openshell (project overrides user)", cfg.Run.Sandbox)
	}
	if cfg.Run.Harness != "pi" {
		t.Fatalf("Run.Harness = %q, want pi (project overrides user)", cfg.Run.Harness)
	}
}

// TestRunProjectScopePartialFallsBackToUser pins per-field precedence: a project
// that sets only the harness falls back to the user file for the sandbox.
func TestRunProjectScopePartialFallsBackToUser(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
sandbox = "docker"
harness = "opencode"
`)
	project := writeConfig(t, dir, "project.toml", `
project = "acme"

[run]
harness = "pi"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "docker" {
		t.Fatalf("Run.Sandbox = %q, want docker (from user)", cfg.Run.Sandbox)
	}
	if cfg.Run.Harness != "pi" {
		t.Fatalf("Run.Harness = %q, want pi (from project)", cfg.Run.Harness)
	}
}

// TestRunProjectScopeRejectsRemovedBackendKey pins the removal inside the
// project-scoped [run] table too, so a committed file cannot keep the old name.
func TestRunProjectScopeRejectsRemovedBackendKey(t *testing.T) {
	dir := t.TempDir()
	project := writeConfig(t, dir, "project.toml", `
project = "acme"

[run]
backend = "docker"
harness = "opencode"
`)
	_, err := Load(Input{UserPath: filepath.Join(dir, "none.toml"), ProjectPath: project, Getenv: emptyEnv})
	if err == nil {
		t.Fatal("Load() with the removed project backend key = nil error, want an error naming sandbox")
	}
	if !strings.Contains(err.Error(), "[run] backend") || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("Load() error = %v, want it to name [run] backend and sandbox", err)
	}
	if !strings.Contains(err.Error(), project) {
		t.Fatalf("Load() error = %v, want it to name the offending file %s", err, project)
	}
}
