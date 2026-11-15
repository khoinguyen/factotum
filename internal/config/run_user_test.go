package config

import (
	"path/filepath"
	"testing"
)

// TestLoadRunUserIsMachineScoped pins that the container identity knob lives in
// the machine-scoped [run] table: which uid a host's containers run as is a
// host property, so a committed project file cannot carry it.
func TestLoadRunUserIsMachineScoped(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
user = "501:20"
`)
	project := writeConfig(t, dir, "project.toml", `
project = "acme"

[run]
user = "0"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "501:20"; cfg.Run.User != want {
		t.Fatalf("Run.User = %q, want %q (machine-scoped)", cfg.Run.User, want)
	}
}

// TestRunUserEnvOverridesConfig pins that FACTOTUM_RUN_USER supplies the
// container identity per run, matching the other run settings' env overlay.
func TestRunUserEnvOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
user = "501:20"
`)
	getenv := func(key string) string {
		if key == "FACTOTUM_RUN_USER" {
			return "1000:1000"
		}
		return ""
	}
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: getenv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "1000:1000"; cfg.Run.User != want {
		t.Fatalf("Run.User = %q, want %q from FACTOTUM_RUN_USER", cfg.Run.User, want)
	}
}
