package config

import (
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadReadsRunSandboxKey pins the primary key: the isolation backend is
// selected with `sandbox`, matching the `--sandbox` flag.
func TestLoadReadsRunSandboxKey(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
sandbox = "docker"
harness = "opencode"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "docker" {
		t.Fatalf("Run.Sandbox = %q, want docker", cfg.Run.Sandbox)
	}
}

// TestLoadRejectsRemovedRunBackendKey pins the end of the rename: the old
// `backend` key is gone, so a config that still sets it fails with an error
// naming the replacement instead of being silently accepted.
func TestLoadRejectsRemovedRunBackendKey(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
backend = "local"
harness = "opencode"
`)
	_, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err == nil {
		t.Fatal("Load() with the removed backend key = nil error, want an error naming sandbox")
	}
	if !strings.Contains(err.Error(), "[run] backend") || !strings.Contains(err.Error(), "sandbox") {
		t.Fatalf("Load() error = %v, want it to name [run] backend and sandbox", err)
	}
	if !strings.Contains(err.Error(), user) {
		t.Fatalf("Load() error = %v, want it to name the offending file %s", err, user)
	}
}

// TestLoadRejectsRemovedRunBackendEnv pins the same removal on the environment:
// FACTOTUM_RUN_BACKEND no longer selects anything, and the error names
// FACTOTUM_RUN_SANDBOX.
func TestLoadRejectsRemovedRunBackendEnv(t *testing.T) {
	_, err := Load(Input{
		UserPath:    filepath.Join(t.TempDir(), "none.toml"),
		ProjectPath: filepath.Join(t.TempDir(), "none.toml"),
		Getenv: func(key string) string {
			switch key {
			case "FACTOTUM_RUN_BACKEND":
				return "local"
			case "FACTOTUM_RUN_HARNESS":
				return "opencode"
			default:
				return ""
			}
		},
	})
	if err == nil {
		t.Fatal("Load() with FACTOTUM_RUN_BACKEND = nil error, want an error naming FACTOTUM_RUN_SANDBOX")
	}
	if !strings.Contains(err.Error(), "FACTOTUM_RUN_BACKEND") || !strings.Contains(err.Error(), "FACTOTUM_RUN_SANDBOX") {
		t.Fatalf("Load() error = %v, want it to name FACTOTUM_RUN_BACKEND and FACTOTUM_RUN_SANDBOX", err)
	}
}
