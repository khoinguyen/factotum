package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}

func TestEnsureMachineConfigCreatesThenPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "config.toml")

	created, err := EnsureMachineConfig(path)
	if err != nil {
		t.Fatalf("EnsureMachineConfig() error = %v", err)
	}
	if !created {
		t.Fatal("first call created = false, want true")
	}
	if got := readFile(t, path); !strings.Contains(got, "Machine-scoped Factotum config") {
		t.Fatalf("new config missing template header:\n%s", got)
	}

	if err := os.WriteFile(path, []byte("# hand edited\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	created, err = EnsureMachineConfig(path)
	if err != nil {
		t.Fatalf("EnsureMachineConfig() error = %v", err)
	}
	if created {
		t.Fatal("second call created = true, want false")
	}
	if got := readFile(t, path); got != "# hand edited\n" {
		t.Fatalf("existing config was clobbered:\n%s", got)
	}
}

func TestAddProjectEntryAppendsAndSetsDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := EnsureMachineConfig(path); err != nil {
		t.Fatalf("EnsureMachineConfig() error = %v", err)
	}

	created, err := AddProjectEntry(path, "acme", "~/.factotum/acme.db")
	if err != nil {
		t.Fatalf("AddProjectEntry() error = %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	got := readFile(t, path)
	for _, want := range []string{`default_project = "acme"`, "[projects.acme]", `db_path = "~/.factotum/acme.db"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q:\n%s", want, got)
		}
	}
	if header, key := strings.Index(got, "# Machine-scoped"), strings.Index(got, "default_project"); header > key {
		t.Fatalf("default_project should follow the header comment:\n%s", got)
	}

	cfg, err := Load(Input{UserPath: path, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "acme" {
		t.Fatalf("Project = %q, want acme", cfg.Project)
	}
	if cfg.Store.Backend != "sqlite" || cfg.Store.Options["path"] != ExpandPath("~/.factotum/acme.db") {
		t.Fatalf("Store = %+v, want sqlite at the entry path", cfg.Store)
	}
}

func TestAddProjectEntryPreservesExistingContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := `# Machine-scoped Factotum config.
default_project = "other"

[embed]
provider = "ollama"
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := AddProjectEntry(path, "acme", "/tmp/acme.db"); err != nil {
		t.Fatalf("AddProjectEntry() error = %v", err)
	}
	got := readFile(t, path)
	for _, want := range []string{"# Machine-scoped Factotum config.", `default_project = "other"`, `provider = "ollama"`, "[projects.acme]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("config missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "default_project") != 1 {
		t.Fatalf("default_project was duplicated:\n%s", got)
	}
}

func TestAddProjectEntryIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := EnsureMachineConfig(path); err != nil {
		t.Fatalf("EnsureMachineConfig() error = %v", err)
	}
	if _, err := AddProjectEntry(path, "acme", "/tmp/acme.db"); err != nil {
		t.Fatalf("AddProjectEntry() error = %v", err)
	}
	first := readFile(t, path)

	created, err := AddProjectEntry(path, "acme", "/tmp/other.db")
	if err != nil {
		t.Fatalf("AddProjectEntry() error = %v", err)
	}
	if created {
		t.Fatal("second add created = true, want false")
	}
	if got := readFile(t, path); got != first {
		t.Fatalf("second add changed the file:\nfirst:\n%s\nsecond:\n%s", first, got)
	}
}

func TestWriteProjectConfigCreates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".factotum", "config.toml")

	created, err := WriteProjectConfig(path, "acme")
	if err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	if got := readFile(t, path); !strings.Contains(got, `project = "acme"`) {
		t.Fatalf("project config missing project line:\n%s", got)
	}

	cfg, err := Load(Input{ProjectPath: path, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "acme" {
		t.Fatalf("Project = %q, want acme", cfg.Project)
	}
}

func TestWriteProjectConfigIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := WriteProjectConfig(path, "acme"); err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}
	first := readFile(t, path)

	created, err := WriteProjectConfig(path, "acme")
	if err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}
	if created {
		t.Fatal("second write created = true, want false")
	}
	if got := readFile(t, path); got != first {
		t.Fatalf("second write changed the file:\n%s", got)
	}
}

func TestWriteProjectConfigRefusesDifferentProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := WriteProjectConfig(path, "acme"); err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}

	_, err := WriteProjectConfig(path, "other")
	if err == nil {
		t.Fatal("WriteProjectConfig() error = nil, want refusal")
	}
	if got := readFile(t, path); !strings.Contains(got, `project = "acme"`) {
		t.Fatalf("existing project was clobbered:\n%s", got)
	}
}

func TestWriteProjectConfigFillsEmptyProject(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "# hand written\nno_hints = true\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	created, err := WriteProjectConfig(path, "acme")
	if err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}
	if !created {
		t.Fatal("created = false, want true")
	}
	got := readFile(t, path)
	if !strings.Contains(got, "# hand written") || !strings.Contains(got, "no_hints = true") {
		t.Fatalf("existing project content was lost:\n%s", got)
	}
	cfg, err := Load(Input{ProjectPath: path, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "acme" {
		t.Fatalf("Project = %q, want acme", cfg.Project)
	}
}

func TestAddProjectEntryQuotesUnsafeID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := EnsureMachineConfig(path); err != nil {
		t.Fatalf("EnsureMachineConfig() error = %v", err)
	}
	if _, err := AddProjectEntry(path, "weird id", "/tmp/weird.db"); err != nil {
		t.Fatalf("AddProjectEntry() error = %v", err)
	}

	cfg, err := Load(Input{UserPath: path, ProjectPath: filepath.Join(dir, "none.toml"), Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Project != "weird id" {
		t.Fatalf("Project = %q, want %q", cfg.Project, "weird id")
	}
}
