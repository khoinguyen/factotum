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

// TestLoadRejectsBothRunSandboxAndBackend pins that sandbox does not rescue a
// stale backend key: when both are set the removed-name rule still wins, with
// the exact actionable error naming sandbox and the offending file.
func TestLoadRejectsBothRunSandboxAndBackend(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
sandbox = "docker"
backend = "local"
harness = "opencode"
`)
	_, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	want := "parse config " + user + ": [run] backend was removed; use sandbox instead"
	if err == nil || err.Error() != want {
		t.Fatalf("Load() with both sandbox and backend error = %v, want %q", err, want)
	}
}

// TestLoadTreatsEmptyRunBackendAsUnset pins that an explicitly empty backend
// value is not a set key: load succeeds, no removal error, and the sandbox
// selection is unaffected.
func TestLoadTreatsEmptyRunBackendAsUnset(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantSandbox string
	}{
		{name: "empty backend alone", body: "[run]\nbackend = \"\"\nharness = \"opencode\"\n"},
		{name: "empty backend with sandbox", body: "[run]\nsandbox = \"docker\"\nbackend = \"\"\nharness = \"opencode\"\n", wantSandbox: "docker"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			user := writeConfig(t, dir, "user.toml", tc.body)
			cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
			if err != nil {
				t.Fatalf("Load() with an empty backend value error = %v, want nil", err)
			}
			if cfg.Run.Sandbox != tc.wantSandbox {
				t.Fatalf("Run.Sandbox = %q, want %q", cfg.Run.Sandbox, tc.wantSandbox)
			}
		})
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

// TestLoadRejectsBothRunSandboxEnvAndBackendEnv pins the same precedence on the
// environment: FACTOTUM_RUN_SANDBOX does not rescue FACTOTUM_RUN_BACKEND, and
// the exact error names the replacement variable.
func TestLoadRejectsBothRunSandboxEnvAndBackendEnv(t *testing.T) {
	_, err := Load(Input{
		UserPath:    filepath.Join(t.TempDir(), "none.toml"),
		ProjectPath: filepath.Join(t.TempDir(), "none.toml"),
		Getenv: func(key string) string {
			switch key {
			case "FACTOTUM_RUN_SANDBOX":
				return "docker"
			case "FACTOTUM_RUN_BACKEND":
				return "local"
			default:
				return ""
			}
		},
	})
	want := "FACTOTUM_RUN_BACKEND was removed; use FACTOTUM_RUN_SANDBOX instead"
	if err == nil || err.Error() != want {
		t.Fatalf("Load() with both sandbox and backend env error = %v, want %q", err, want)
	}
}
