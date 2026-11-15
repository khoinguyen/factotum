package config

import (
	"path/filepath"
	"testing"
)

// TestLoadRunImageIsMachineScoped pins that the image knob lives in the
// machine-scoped [run] table: a host names the base image it has or built, so a
// committed project file cannot carry it (the project pins harness/sandbox
// intent only).
func TestLoadRunImageIsMachineScoped(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
image = "ghcr.io/acme/pi:1"
`)
	project := writeConfig(t, dir, "project.toml", `
project = "acme"

[run]
image = "ghcr.io/acme/from-project:1"
`)
	cfg, err := Load(Input{UserPath: user, ProjectPath: project, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "ghcr.io/acme/pi:1"; cfg.Run.Image != want {
		t.Fatalf("Run.Image = %q, want %q (machine-scoped)", cfg.Run.Image, want)
	}
}

// TestRunImageEnvOverridesConfig pins that FACTOTUM_RUN_IMAGE supplies the base
// image per run, matching the other run settings' env overlay.
func TestRunImageEnvOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	user := writeConfig(t, dir, "user.toml", `
[run]
image = "ghcr.io/acme/pi:1"
`)
	getenv := func(key string) string {
		if key == "FACTOTUM_RUN_IMAGE" {
			return "ghcr.io/acme/pi:env"
		}
		return ""
	}
	cfg, err := Load(Input{UserPath: user, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: getenv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if want := "ghcr.io/acme/pi:env"; cfg.Run.Image != want {
		t.Fatalf("Run.Image = %q, want %q from FACTOTUM_RUN_IMAGE", cfg.Run.Image, want)
	}
}
