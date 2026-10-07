package config

import (
	"path/filepath"
	"testing"
)

// TestLoadReadsRunSandboxKey pins the renamed primary key: the isolation backend
// is selected with `sandbox`, matching the `--sandbox` flag.
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

// TestLoadReadsDeprecatedRunBackendKey pins the one-release compatibility path:
// the old `backend` key still selects the sandbox.
func TestLoadReadsDeprecatedRunBackendKey(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
backend = "local"
harness = "opencode"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "local" {
		t.Fatalf("Run.Sandbox = %q, want local from the deprecated backend key", cfg.Run.Sandbox)
	}
}

// TestRunSandboxKeyWinsOverDeprecatedBackendKey pins precedence when both keys
// are present: the new key wins.
func TestRunSandboxKeyWinsOverDeprecatedBackendKey(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
sandbox = "docker"
backend = "local"
harness = "opencode"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "docker" {
		t.Fatalf("Run.Sandbox = %q, want docker (the new key wins)", cfg.Run.Sandbox)
	}
}

// TestRunSandboxEnvWinsOverDeprecatedBackendEnv pins that FACTOTUM_RUN_SANDBOX
// takes precedence over the deprecated FACTOTUM_RUN_BACKEND.
func TestRunSandboxEnvWinsOverDeprecatedBackendEnv(t *testing.T) {
	cfg, err := Load(Input{
		UserPath:    filepath.Join(t.TempDir(), "none.toml"),
		ProjectPath: filepath.Join(t.TempDir(), "none.toml"),
		Getenv: func(key string) string {
			switch key {
			case "FACTOTUM_RUN_SANDBOX":
				return "openshell"
			case "FACTOTUM_RUN_BACKEND":
				return "local"
			case "FACTOTUM_RUN_HARNESS":
				return "opencode"
			default:
				return ""
			}
		},
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "openshell" {
		t.Fatalf("Run.Sandbox = %q, want openshell (the new env wins)", cfg.Run.Sandbox)
	}
}

// TestRunDeprecatedBackendEnvStillWorks pins that FACTOTUM_RUN_BACKEND still
// selects the sandbox for one release.
func TestRunDeprecatedBackendEnvStillWorks(t *testing.T) {
	cfg, err := Load(Input{
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
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "local" {
		t.Fatalf("Run.Sandbox = %q, want local from the deprecated env", cfg.Run.Sandbox)
	}
}
