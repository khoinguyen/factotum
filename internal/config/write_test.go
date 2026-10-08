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

func TestWriteProjectTechStackCreatesAndPreserves(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".factotum", "config.toml")
	if _, err := WriteProjectConfig(path, "acme"); err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}

	wrote, err := WriteProjectTechStack(path, "go")
	if err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false, want true")
	}
	got := readFile(t, path)
	if !strings.Contains(got, `tech_stack = "go"`) {
		t.Fatalf("project config missing tech stack:\n%s", got)
	}
	if !strings.Contains(got, `project = "acme"`) {
		t.Fatalf("project pin was lost:\n%s", got)
	}
}

func TestWriteProjectTechStackIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := WriteProjectConfig(path, "acme"); err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}
	if _, err := WriteProjectTechStack(path, "go"); err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	first := readFile(t, path)

	wrote, err := WriteProjectTechStack(path, "go")
	if err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	if wrote {
		t.Fatal("second write wrote = true, want false")
	}
	if got := readFile(t, path); got != first {
		t.Fatalf("second write changed the file:\n%s", got)
	}
}

func TestWriteProjectTechStackReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if _, err := WriteProjectConfig(path, "acme"); err != nil {
		t.Fatalf("WriteProjectConfig() error = %v", err)
	}
	if _, err := WriteProjectTechStack(path, "go"); err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	if _, err := WriteProjectTechStack(path, "rust"); err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	got := readFile(t, path)
	if strings.Count(got, "tech_stack") != 1 {
		t.Fatalf("tech_stack should appear once, got:\n%s", got)
	}
	if !strings.Contains(got, `tech_stack = "rust"`) {
		t.Fatalf("tech_stack not replaced:\n%s", got)
	}
	cfg, err := Load(Input{ProjectPath: path, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v (duplicate or invalid key?)", err)
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

// TestWriteProjectTechStackReplacesAfterMultilineArray pins the hardening for
// hand-edited configs: a top-level multi-line array before the key must not be
// mistaken for a table header, so the existing tech_stack is replaced in place
// rather than duplicated.
func TestWriteProjectTechStackReplacesAfterMultilineArray(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "# hand edited\nproject = \"acme\"\nmatrix = [\n  [1, 2],\n  [3, 4],\n]\ntech_stack = \"go\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	wrote, err := WriteProjectTechStack(path, "rust")
	if err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	if !wrote {
		t.Fatal("wrote = false, want true")
	}
	got := readFile(t, path)
	if strings.Count(got, "tech_stack") != 1 {
		t.Fatalf("tech_stack should appear once, got:\n%s", got)
	}
	if !strings.Contains(got, `tech_stack = "rust"`) {
		t.Fatalf("tech_stack not replaced:\n%s", got)
	}
	if !strings.Contains(got, "matrix = [") {
		t.Fatalf("the hand-edited array was lost:\n%s", got)
	}
	cfg, err := Load(Input{ProjectPath: path, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v (duplicate or invalid key?)", err)
	}
	if cfg.TechStack != "rust" {
		t.Fatalf("TechStack = %q, want rust", cfg.TechStack)
	}
}

// TestWriteProjectTechStackIgnoresBracketsInStrings pins that an unbalanced
// bracket inside a quoted value does not fool the nesting scan into treating the
// rest of the file as array contents.
func TestWriteProjectTechStackIgnoresBracketsInStrings(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "project = \"acme\"\nbanner = \"a[unclosed\"\ntech_stack = \"go\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := WriteProjectTechStack(path, "rust"); err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	got := readFile(t, path)
	if strings.Count(got, "tech_stack") != 1 {
		t.Fatalf("tech_stack should appear once, got:\n%s", got)
	}
	if !strings.Contains(got, `tech_stack = "rust"`) {
		t.Fatalf("tech_stack not replaced:\n%s", got)
	}
}

// TestWriteProjectTechStackStopsAtTableHeader pins that the scan still stops at a
// real table header: a top-level key is inserted at the top, never inside a table.
func TestWriteProjectTechStackStopsAtTableHeader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	body := "# hand edited\nproject = \"acme\"\n\n[run]\nsandbox = \"local\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if _, err := WriteProjectTechStack(path, "go"); err != nil {
		t.Fatalf("WriteProjectTechStack() error = %v", err)
	}
	got := readFile(t, path)
	tech := strings.Index(got, "tech_stack")
	run := strings.Index(got, "[run]")
	if tech < 0 || run < 0 || tech > run {
		t.Fatalf("tech_stack should be inserted above [run]:\n%s", got)
	}
	cfg, err := Load(Input{ProjectPath: path, Getenv: emptyEnv})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.TechStack != "go" {
		t.Fatalf("TechStack = %q, want go", cfg.TechStack)
	}
}
