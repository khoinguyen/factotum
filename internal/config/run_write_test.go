package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteRunDefaultsCreatesUserConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "user.toml")

	if err := WriteRunDefaults(path, false, "", RunDefaults{Sandbox: "local", Harness: "opencode"}); err != nil {
		t.Fatalf("WriteRunDefaults() error = %v", err)
	}
	got := readFile(t, path)
	for _, want := range []string{"[run]", `sandbox = "local"`, `harness = "opencode"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("user config missing %q:\n%s", want, got)
		}
	}
	cfg, err := Load(Input{UserPath: path, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "local" || cfg.Run.Harness != "opencode" {
		t.Fatalf("Run = %+v, want local/opencode from the written defaults", cfg.Run)
	}
}

func TestWriteRunDefaultsPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	body := "# Machine-scoped Factotum config.\ndefault_project = \"acme\"\n\n[run]\nallow_host = true\n\n[embed]\nprovider = \"ollama\"\n"
	path := writeConfig(t, dir, "user.toml", body)

	if err := WriteRunDefaults(path, false, "", RunDefaults{Sandbox: "docker", Harness: "opencode"}); err != nil {
		t.Fatalf("WriteRunDefaults() error = %v", err)
	}
	got := readFile(t, path)
	for _, want := range []string{"# Machine-scoped Factotum config.", `default_project = "acme"`, "allow_host = true", `provider = "ollama"`, `sandbox = "docker"`, `harness = "opencode"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("preserved config missing %q:\n%s", want, got)
		}
	}
	cfg, err := Load(Input{UserPath: path, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Run.Sandbox != "docker" || !cfg.Run.AllowHost {
		t.Fatalf("Run = %+v, want docker with allow_host preserved", cfg.Run)
	}
}

func TestWriteRunDefaultsUpdatesExistingKey(t *testing.T) {
	dir := t.TempDir()
	path := writeConfig(t, dir, "user.toml", "[run]\nsandbox = \"local\"\nharness = \"opencode\"\n")

	if err := WriteRunDefaults(path, false, "", RunDefaults{Sandbox: "docker"}); err != nil {
		t.Fatalf("WriteRunDefaults() error = %v", err)
	}
	got := readFile(t, path)
	if strings.Count(got, "sandbox =") != 1 || !strings.Contains(got, `sandbox = "docker"`) {
		t.Fatalf("sandbox key not updated exactly once:\n%s", got)
	}
	if !strings.Contains(got, `harness = "opencode"`) {
		t.Fatalf("harness key was lost:\n%s", got)
	}
}

// TestWriteRunDefaultsRecognizesHeaderVariants pins that a valid [run] header
// with an inline comment or inner whitespace is updated in place rather than
// duplicated, which would make the file unparseable.
func TestWriteRunDefaultsRecognizesHeaderVariants(t *testing.T) {
	tests := []struct {
		name   string
		header string
	}{
		{"bare", "[run]"},
		{"inline comment", "[run] # note"},
		{"inner spaces", "[ run ]"},
		{"tab comment", "[run]\t# note"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := writeConfig(t, dir, "user.toml", tt.header+"\nsandbox = \"local\"\n")

			if err := WriteRunDefaults(path, false, "", RunDefaults{Harness: "opencode"}); err != nil {
				t.Fatalf("WriteRunDefaults() error = %v", err)
			}
			got := readFile(t, path)
			if strings.Count(got, "[run]")+strings.Count(got, "[ run ]") != 1 {
				t.Fatalf("expected exactly one [run] header, got:\n%s", got)
			}
			if !strings.Contains(got, `harness = "opencode"`) {
				t.Fatalf("harness not written:\n%s", got)
			}
			cfg, err := Load(Input{UserPath: path, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.Run.Sandbox != "local" || cfg.Run.Harness != "opencode" {
				t.Fatalf("Run = %+v, want local/opencode", cfg.Run)
			}
		})
	}
}

// TestWriteRunDefaultsWritesAllowHost pins that the host-scoped opt-in is
// written as a bare TOML bool the loader reads back, not a quoted string.
func TestWriteRunDefaultsWritesAllowHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "user.toml")

	if err := WriteRunDefaults(path, false, "", RunDefaults{Sandbox: "local", Harness: "opencode", AllowHost: true}); err != nil {
		t.Fatalf("WriteRunDefaults() error = %v", err)
	}
	got := readFile(t, path)
	if !strings.Contains(got, "allow_host = true") {
		t.Fatalf("user config missing a bare allow_host bool:\n%s", got)
	}
	if strings.Contains(got, `allow_host = "true"`) {
		t.Fatalf("allow_host written as a string, not a bool:\n%s", got)
	}
	cfg, err := Load(Input{UserPath: path, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.Run.AllowHost {
		t.Fatalf("Run.AllowHost = false, want the written opt-in")
	}
}

// TestWriteRunDefaultsRejectsAllowHostInProject pins that the host-scoped opt-in
// can never land in a committed project config.
func TestWriteRunDefaultsRejectsAllowHostInProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".factotum", "config.toml")

	err := WriteRunDefaults(path, true, "acme", RunDefaults{Sandbox: "local", AllowHost: true})
	if err == nil {
		t.Fatalf("project-scoped allow_host error = nil, want a refusal")
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("project config was written despite the refusal: %v", statErr)
	}
}

func TestWriteRunDefaultsProjectPinsProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".factotum", "config.toml")

	if err := WriteRunDefaults(path, true, "acme", RunDefaults{Sandbox: "openshell", Harness: "opencode"}); err != nil {
		t.Fatalf("WriteRunDefaults() error = %v", err)
	}
	got := readFile(t, path)
	for _, want := range []string{`project = "acme"`, "[run]", `sandbox = "openshell"`, `harness = "opencode"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("project config missing %q:\n%s", want, got)
		}
	}
	cfg, err := Load(Input{ProjectPath: path, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "acme" || cfg.Run.Sandbox != "openshell" {
		t.Fatalf("cfg = %+v, want acme/openshell", cfg)
	}
}
