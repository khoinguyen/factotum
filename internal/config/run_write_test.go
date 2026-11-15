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

// TestWriteRunDefaultsProjectScopeRequiresProject pins that a project-scoped
// write never leaves a [run] table in a project config with no project pin: the
// caller must say which project, or the write is refused before it touches the
// file. A machine config is unaffected, since it needs no pin.
func TestWriteRunDefaultsProjectScopeRequiresProject(t *testing.T) {
	tests := []struct {
		name      string
		project   bool
		projectID string
		existing  string
		wantErr   bool
	}{
		{"project scope with no id", true, "", "", true},
		{"project scope with a blank id", true, "  ", "", true},
		{"project config pinned to another project", true, "acme", "project = \"other\"\n", true},
		{"machine scope needs no id", false, "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".factotum", "config.toml")
			if tt.existing != "" {
				path = writeConfig(t, dir, "config.toml", tt.existing)
			}

			err := WriteRunDefaults(path, tt.project, tt.projectID, RunDefaults{Sandbox: "local", Harness: "opencode"})
			if tt.wantErr {
				if err == nil {
					t.Fatalf("WriteRunDefaults() error = nil, want a refusal")
				}
				if !strings.Contains(err.Error(), "project") {
					t.Fatalf("error does not name the project requirement: %v", err)
				}
				got, readErr := os.ReadFile(path)
				if readErr == nil && string(got) != tt.existing {
					t.Fatalf("config changed despite the refusal:\n%s", got)
				}
				if readErr != nil && !os.IsNotExist(readErr) {
					t.Fatalf("read config: %v", readErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("WriteRunDefaults() error = %v", err)
			}
			if got := readFile(t, path); !strings.Contains(got, "[run]") {
				t.Fatalf("config missing the [run] table:\n%s", got)
			}
		})
	}
}

// TestWriteRunDefaultsOutputIsByteExact pins the exact bytes written, so an
// append never adds a blank line to a file that already ends in a newline and
// the table separator is stable wherever the table lands.
func TestWriteRunDefaultsOutputIsByteExact(t *testing.T) {
	both := RunDefaults{Sandbox: "local", Harness: "opencode"}
	tests := []struct {
		name      string
		existing  string
		missing   bool
		project   bool
		projectID string
		defaults  RunDefaults
		want      string
	}{
		{
			name:     "user file ending in one newline",
			existing: `default_project = "acme"` + "\n",
			defaults: both,
			want:     `default_project = "acme"` + "\n\n[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
		{
			name:     "user file ending in a blank line",
			existing: `default_project = "acme"` + "\n\n",
			defaults: both,
			want:     `default_project = "acme"` + "\n\n[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
		{
			name:     "user file with no trailing newline",
			existing: `default_project = "acme"`,
			defaults: both,
			want:     `default_project = "acme"` + "\n\n[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
		{
			name:     "empty user file",
			defaults: both,
			want:     "[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
		{
			name:     "missing user file seeds the machine template",
			missing:  true,
			defaults: both,
			want:     machineConfigTemplate + "\n[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
		{
			name:     "existing run table at eof with a trailing blank line",
			existing: "[run]\nsandbox = \"local\"\n\n",
			defaults: RunDefaults{Harness: "opencode"},
			want:     "[run]\nsandbox = \"local\"\nharness = \"opencode\"\n",
		},
		{
			name:     "existing run table followed by another table",
			existing: "[run]\nsandbox = \"local\"\n\n[embed]\nprovider = \"ollama\"\n",
			defaults: RunDefaults{Harness: "opencode"},
			want:     "[run]\nsandbox = \"local\"\nharness = \"opencode\"\n\n[embed]\nprovider = \"ollama\"\n",
		},
		{
			name:      "new project config from the template",
			missing:   true,
			project:   true,
			projectID: "acme",
			defaults:  both,
			want:      projectConfigTemplate + "project = \"acme\"\n\n[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
		{
			name:      "pinned project config ending in a blank line",
			existing:  "project = \"acme\"\n\n",
			project:   true,
			projectID: "acme",
			defaults:  both,
			want:      "project = \"acme\"\n\n[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
		{
			name:      "unpinned project config gains the pin",
			existing:  "no_hints = true\n",
			project:   true,
			projectID: "acme",
			defaults:  both,
			want:      "project = \"acme\"\nno_hints = true\n\n[run]\nharness = \"opencode\"\nsandbox = \"local\"\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".factotum", "config.toml")
			if !tt.missing {
				path = writeConfig(t, dir, "config.toml", tt.existing)
			}

			if err := WriteRunDefaults(path, tt.project, tt.projectID, tt.defaults); err != nil {
				t.Fatalf("WriteRunDefaults() error = %v", err)
			}
			if got := readFile(t, path); got != tt.want {
				t.Fatalf("config bytes = %q, want %q", got, tt.want)
			}
		})
	}
}
