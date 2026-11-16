package cli

import (
	"bufio"
	"bytes"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/store/builtins"
)

// scriptedPrompter answers a fixed sequence of inputs and confirms, and records
// the labels it was asked. It fails the test on an unexpected prompt, so a code
// path that prompts when it must not is caught.
type scriptedPrompter struct {
	t        *testing.T
	inputs   []string
	confirms []bool
	asked    []string
}

func (p *scriptedPrompter) Input(label, def string) (string, error) {
	p.asked = append(p.asked, "input:"+label)
	if len(p.inputs) == 0 {
		p.t.Fatalf("unexpected Input(%q); asked=%v", label, p.asked)
	}
	value := p.inputs[0]
	p.inputs = p.inputs[1:]
	if value == "" {
		return def, nil
	}
	return value, nil
}

func (p *scriptedPrompter) Confirm(label string, def bool) (bool, error) {
	p.asked = append(p.asked, "confirm:"+label)
	if len(p.confirms) == 0 {
		p.t.Fatalf("unexpected Confirm(%q); asked=%v", label, p.asked)
	}
	value := p.confirms[0]
	p.confirms = p.confirms[1:]
	return value, nil
}

func fakeRepo(string) (gitRepo, bool) {
	return gitRepo{Root: "/work/widget", Remote: "git@github.com:acme/widget.git"}, true
}

func noRepo(string) (gitRepo, bool) { return gitRepo{}, false }

func terminal(io.Writer) bool { return true }

// TestPromptsAllowed pins the prompt gate: a terminal on stdout is not enough.
// With stdin redirected the prompts read EOF, accept every default, and persist
// choices nobody made, so the gate must also require a terminal on stdin.
func TestPromptsAllowed(t *testing.T) {
	tests := []struct {
		name   string
		stdout bool
		stdin  bool
		prompt Prompter
		want   bool
	}{
		{name: "both terminals", stdout: true, stdin: true, want: true},
		{name: "stdout terminal stdin pipe", stdout: true, stdin: false, want: false},
		{name: "stdin terminal stdout pipe", stdout: false, stdin: true, want: false},
		{name: "neither terminal", want: false},
		{name: "injected prompter", prompt: &scriptedPrompter{t: t}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
			d.Prompt = tt.prompt
			d.IsTerminal = func(io.Writer) bool { return tt.stdout }
			stdin := strings.NewReader("distinctive")
			var probed io.Reader
			d.IsTerminalReader = func(r io.Reader) bool {
				probed = r
				return tt.stdin
			}
			cmd := &cobra.Command{}
			cmd.SetIn(stdin)

			if got := d.promptsAllowed(cmd); got != tt.want {
				t.Fatalf("promptsAllowed() = %v, want %v", got, tt.want)
			}
			// The gate probes the command's own stdin, not the process's.
			if tt.prompt == nil && tt.stdout && probed != stdin {
				t.Fatalf("IsTerminalReader got %v, want cmd.InOrStdin()", probed)
			}
		})
	}
}

func TestInitUserOnlyCreatesMachineConfig(t *testing.T) {
	r := newRunner(t)
	out := r.run("init", "-u")

	if !strings.Contains(out, "created: true") {
		t.Fatalf("init -u output missing created: true:\n%s", out)
	}
	if !strings.Contains(out, "user_config: "+r.userPath) {
		t.Fatalf("init -u output missing user_config path:\n%s", out)
	}
	if _, err := os.Stat(r.userPath); err != nil {
		t.Fatalf("machine config not created: %v", err)
	}
	if _, err := os.Stat(r.projectPath); !os.IsNotExist(err) {
		t.Fatalf("init -u created the project config (err=%v)", err)
	}
}

func TestInitUserOnlyPreservesExisting(t *testing.T) {
	r := newRunner(t)
	body := "default_project = \"x\"\n"
	if err := os.WriteFile(r.userPath, []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	out := r.run("init", "-u")

	if !strings.Contains(out, "created: false") {
		t.Fatalf("init -u on existing config should report created: false:\n%s", out)
	}
	if got := readFile(t, r.userPath); got != body {
		t.Fatalf("existing machine config was clobbered:\n%s", got)
	}
}

func TestInitRegistersProjectFromGit(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo

	out := r.run("init", "-p")
	if !strings.Contains(out, "project: widget") {
		t.Fatalf("init -p output missing derived project id:\n%s", out)
	}

	machine := readFile(t, r.userPath)
	for _, want := range []string{`default_project = "widget"`, "[projects.widget]"} {
		if !strings.Contains(machine, want) {
			t.Fatalf("machine config missing %q:\n%s", want, machine)
		}
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `project = "widget"`) {
		t.Fatalf("project config missing the pin:\n%s", project)
	}

	db := filepath.Join(filepath.Dir(r.userPath), "widget.db")
	got := r.run("--store", "sqlite", "--store-opt", "path="+db, "project", "get", "widget")
	if !strings.Contains(got, "widget") {
		t.Fatalf("project entity was not created in its store:\n%s", got)
	}
}

// TestInitProjectConsentsToMigration pins that the consent the store guard
// advertises (`--store-opt migrate=yes`) reaches the project store init opens:
// an older-schema database at the derived path is backed up and migrated
// instead of leaving the project unregistered.
func TestInitProjectConsentsToMigration(t *testing.T) {
	t.Chdir(t.TempDir())
	home := t.TempDir()
	dotFactotum := filepath.Join(home, ".factotum")
	if err := os.MkdirAll(dotFactotum, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	db := filepath.Join(dotFactotum, "widget.db")
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE tasks (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL)"); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	_ = raw.Close()

	detect := func(string) (gitRepo, bool) {
		return gitRepo{Root: "/work/widget", Remote: "git@github.com:acme/widget.git"}, true
	}
	out := runRoot(t, home, detect, "--store", "sqlite", "--store-opt", "migrate=yes", "init", "-p")
	if !strings.Contains(out, "project: widget") {
		t.Fatalf("init -p with consent did not register the project:\n%s", out)
	}
	backups, err := filepath.Glob(db + ".bak-*")
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	if len(backups) != 1 {
		t.Fatalf("init -p consent should back up before migrating, got %v", backups)
	}
	shown := runRoot(t, home, detect, "--store", "sqlite", "--store-opt", "path="+db, "project", "get", "widget")
	if !strings.Contains(shown, "widget") {
		t.Fatalf("project entity missing after the consented migration:\n%s", shown)
	}
}

// TestInitProjectRefusedMigrationLeavesNoConfig pins the acceptance behavior: a
// derived store that refuses to migrate (no consent) aborts init before any
// config is written, so the project is never half-registered. The store open is
// the gate, and a refusal there must leave the machine and project configs
// untouched rather than carrying a pin or a [projects.<id>] entry for a project
// that was never created.
func TestInitProjectRefusedMigrationLeavesNoConfig(t *testing.T) {
	cwd := t.TempDir()
	t.Chdir(cwd)
	home := t.TempDir()
	dotFactotum := filepath.Join(home, ".factotum")
	if err := os.MkdirAll(dotFactotum, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	db := filepath.Join(dotFactotum, "widget.db")
	raw, err := sql.Open("sqlite", db)
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	if _, err := raw.Exec("CREATE TABLE tasks (id TEXT PRIMARY KEY, project_id TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, data TEXT NOT NULL)"); err != nil {
		t.Fatalf("create old schema: %v", err)
	}
	_ = raw.Close()

	detect := func(string) (gitRepo, bool) {
		return gitRepo{Root: "/work/widget", Remote: "git@github.com:acme/widget.git"}, true
	}
	if _, err := runRootErr(t, home, detect, "--store", "sqlite", "init", "-p"); err == nil {
		t.Fatal("init -p without migrate consent should refuse")
	}

	projectPath := filepath.Join(cwd, ".factotum", "config.toml")
	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("refused init wrote the project config (err=%v):\n%s", err, readFile(t, projectPath))
	}
	machine := readFile(t, filepath.Join(dotFactotum, "config.toml"))
	for _, want := range []string{"[projects.widget]", "default_project"} {
		if strings.Contains(machine, want) {
			t.Fatalf("refused init left %q in the machine config:\n%s", want, machine)
		}
	}
}

func TestInitProjectNamed(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = noRepo

	out := r.run("init", "-p", "Acme Widgets")
	if !strings.Contains(out, "project: acme-widgets") {
		t.Fatalf("init -p <name> should slug the name into an id:\n%s", out)
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `project = "acme-widgets"`) {
		t.Fatalf("project config missing the slugged id:\n%s", project)
	}
}

func TestInitBareInRepoNonInteractiveDoesNotRegister(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo

	out, errOut := r.runSplit("init")
	if !strings.Contains(out, "created: true") {
		t.Fatalf("init should create the machine config:\n%s", out)
	}
	if _, err := os.Stat(r.projectPath); !os.IsNotExist(err) {
		t.Fatalf("init without a terminal must not register a project (err=%v)", err)
	}
	if !strings.Contains(errOut, "ft init -p") {
		t.Fatalf("init should hint at ft init -p:\n%s", errOut)
	}
}

// TestInitHelpStatesThePromptGate pins the surface: --help must name the real
// gate (a terminal on both stdin and stdout), not the old stdout-only rule.
func TestInitHelpStatesThePromptGate(t *testing.T) {
	r := newRunner(t)

	out := r.run("init", "--help")
	// Help is hard-wrapped, so match on the reflowed text.
	flat := strings.Join(strings.Fields(out), " ")
	for _, want := range []string{"stdin and stdout both on a terminal", "Without a terminal on stdin or stdout"} {
		if !strings.Contains(flat, want) {
			t.Fatalf("init --help missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(flat, "inside a git repository on a terminal") {
		t.Fatalf("init --help still states the stdout-only rule:\n%s", out)
	}
}

// TestInitPipedStdinDoesNotPrompt pins the regression this task fixes: stdout
// on a terminal with stdin redirected must take the headless path, not run the
// prompts and accept every default. Otherwise `ft init < /dev/null` registers a
// project (and a tech stack) the caller never chose.
func TestInitPipedStdinDoesNotPrompt(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo
	r.isTerminal = terminal
	r.stdinTerminal = func(io.Reader) bool { return false }

	t.Run("bare init does not register", func(t *testing.T) {
		out, errOut := r.runSplit("init")
		if !strings.Contains(out, "created: true") {
			t.Fatalf("init should still create the machine config:\n%s", out)
		}
		if _, err := os.Stat(r.projectPath); !os.IsNotExist(err) {
			t.Fatalf("piped stdin must not register a project (err=%v)", err)
		}
		if strings.Contains(errOut, "Register this repository") {
			t.Fatalf("piped stdin must not prompt:\n%s", errOut)
		}
		if !strings.Contains(errOut, "ft init -p") {
			t.Fatalf("piped stdin should keep the ft init -p hint:\n%s", errOut)
		}
	})

	t.Run("init -p takes defaults", func(t *testing.T) {
		r := newRunner(t)
		root := t.TempDir()
		r.gitDetect = func(string) (gitRepo, bool) {
			return gitRepo{Root: root, Remote: "git@github.com:acme/widget.git"}, true
		}
		r.isTerminal = terminal
		r.stdinTerminal = func(io.Reader) bool { return false }

		out, errOut := r.runSplit("init", "-p")
		if !strings.Contains(out, "project: widget") {
			t.Fatalf("init -p should register with the derived defaults:\n%s", out)
		}
		if strings.Contains(out, "tech_stack") {
			t.Fatalf("piped stdin must not record a tech stack:\n%s", out)
		}
		if strings.Contains(errOut, "Tech stack") {
			t.Fatalf("piped stdin must not prompt:\n%s", errOut)
		}
	})
}

// TestInitBothTerminalsPromptsFromStdin pins the other direction: with a
// terminal on both stdin and stdout the real prompter still runs and records
// the answers, so the gate does not overshoot into a headless init.
func TestInitBothTerminalsPromptsFromStdin(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	r.gitDetect = func(string) (gitRepo, bool) {
		return gitRepo{Root: root, Remote: "git@github.com:acme/widget.git"}, true
	}
	r.isTerminal = terminal
	r.stdinTerminal = func(io.Reader) bool { return true }
	// register, then blank project id/name/db/repo defaults, then the tech stack.
	r.stdin = strings.NewReader("y\n\n\n\n\nrust\n")

	out, errOut := r.runSplit("init")
	if !strings.Contains(errOut, "Register this repository as a Factotum project?") {
		t.Fatalf("both terminals should prompt for registration:\n%s", errOut)
	}
	if !strings.Contains(out, "project: widget") || !strings.Contains(out, "tech_stack: rust") {
		t.Fatalf("both terminals should record the prompted answers:\n%s", out)
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `tech_stack = "rust"`) {
		t.Fatalf("project config missing the prompted tech stack:\n%s", project)
	}
}

func TestInitBareInRepoInteractiveYes(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo
	r.isTerminal = terminal
	p := &scriptedPrompter{t: t, confirms: []bool{true}, inputs: []string{"", "", "", "", "go"}}
	r.prompt = p

	out := r.run("init")
	if !strings.Contains(out, "project: widget") {
		t.Fatalf("interactive init did not register the project:\n%s", out)
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `project = "widget"`) {
		t.Fatalf("project config missing the pin:\n%s", project)
	}
	if len(p.asked) != 6 || !strings.HasPrefix(p.asked[0], "confirm:") {
		t.Fatalf("prompt sequence = %v, want a confirm then five inputs", p.asked)
	}
	if !strings.HasPrefix(p.asked[5], "input:Tech stack") {
		t.Fatalf("last prompt = %q, want the tech-stack question", p.asked[5])
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `tech_stack = "go"`) {
		t.Fatalf("greenfield init did not record the tech stack:\n%s", project)
	}
}

// TestInitGreenfieldAsksTechStack pins the acceptance behavior: registering a
// greenfield directory interactively asks a tech-stack question and records the
// answer in the committed project config.
func TestInitGreenfieldAsksTechStack(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	r.gitDetect = func(string) (gitRepo, bool) {
		return gitRepo{Root: root, Remote: "git@github.com:acme/widget.git"}, true
	}
	r.isTerminal = terminal
	p := &scriptedPrompter{t: t, confirms: []bool{true}, inputs: []string{"", "", "", "", "rust"}}
	r.prompt = p

	out := r.run("init", "-p")
	if !strings.Contains(out, "tech_stack: rust") {
		t.Fatalf("init output missing the recorded tech stack:\n%s", out)
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `tech_stack = "rust"`) {
		t.Fatalf("project config missing the tech stack:\n%s", project)
	}
}

// TestInitGreenfieldNonInteractiveDefault pins the non-interactive default: no
// prompt runs, and with no --tech-stack nothing is recorded.
func TestInitGreenfieldNonInteractiveDefault(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	r.gitDetect = func(string) (gitRepo, bool) {
		return gitRepo{Root: root, Remote: "git@github.com:acme/widget.git"}, true
	}

	out := r.run("init", "-p")
	if strings.Contains(out, "tech_stack") {
		t.Fatalf("non-interactive init with no flag should record no tech stack:\n%s", out)
	}
	if project := readFile(t, r.projectPath); strings.Contains(project, "tech_stack") {
		t.Fatalf("non-interactive init should not write a tech stack:\n%s", project)
	}
}

// TestInitTechStackRequiresProject pins that --tech-stack is only meaningful
// with -p: without a project registration the choice has nowhere to land, so the
// flag is a usage error rather than being silently ignored.
func TestInitTechStackRequiresProject(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = noRepo

	if err := r.runErr("init", "--tech-stack", "go"); !errors.Is(err, ErrUsage) {
		t.Fatalf("init --tech-stack without -p error = %v, want ErrUsage", err)
	}
	if err := r.runErr("init", "-u", "--tech-stack", "go"); !errors.Is(err, ErrUsage) {
		t.Fatalf("init -u --tech-stack error = %v, want ErrUsage", err)
	}
	if _, err := os.Stat(r.projectPath); !os.IsNotExist(err) {
		t.Fatalf("a rejected --tech-stack must not write the project config (err=%v)", err)
	}
}

// TestInitTechStackFlagNonInteractive pins the non-interactive channel: the flag
// records the choice without a terminal.
func TestInitTechStackFlagNonInteractive(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	r.gitDetect = func(string) (gitRepo, bool) {
		return gitRepo{Root: root, Remote: "git@github.com:acme/widget.git"}, true
	}

	out := r.run("init", "-p", "--tech-stack", "go")
	if !strings.Contains(out, "tech_stack: go") {
		t.Fatalf("init --tech-stack output missing the choice:\n%s", out)
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `tech_stack = "go"`) {
		t.Fatalf("project config missing the flagged tech stack:\n%s", project)
	}
}

// TestInitNonGreenfieldSkipsTechStack pins that the question is only asked for a
// greenfield directory: a checkout that already holds source files is left alone.
func TestInitNonGreenfieldSkipsTechStack(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r.gitDetect = func(string) (gitRepo, bool) {
		return gitRepo{Root: root, Remote: "git@github.com:acme/widget.git"}, true
	}
	r.isTerminal = terminal
	p := &scriptedPrompter{t: t, confirms: []bool{true}, inputs: []string{"", "", "", ""}}
	r.prompt = p

	r.run("init", "-p")
	if len(p.asked) != 5 {
		t.Fatalf("prompt sequence = %v, want a confirm then four inputs (no tech stack)", p.asked)
	}
	if project := readFile(t, r.projectPath); strings.Contains(project, "tech_stack") {
		t.Fatalf("non-greenfield init should not record a tech stack:\n%s", project)
	}
}

// TestInitCommunityDocsRepoAsksTechStack pins the observed repro: a repository
// created by `gh repo create --add-readme` and then given GitHub's "add a code
// of conduct / security policy" files holds README and community docs but no
// source files, so it is greenfield and must ask for the tech stack. The README
// here carries the .markdown extension, which no source table lists.
func TestInitCommunityDocsRepoAsksTechStack(t *testing.T) {
	r := newRunner(t)
	root := t.TempDir()
	for _, name := range []string{
		"README.markdown", "CODE_OF_CONDUCT.md", "SECURITY.md", "SUPPORT.md",
		"LICENSE-MIT", "CONTRIBUTING.md", ".gitignore",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("scaffolding\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	r.gitDetect = func(string) (gitRepo, bool) {
		return gitRepo{Root: root, Remote: "git@github.com:acme/widget.git"}, true
	}
	r.isTerminal = terminal
	p := &scriptedPrompter{t: t, confirms: []bool{true}, inputs: []string{"", "", "", "", "rust"}}
	r.prompt = p

	out := r.run("init", "-p")
	if len(p.asked) != 6 || !strings.HasPrefix(p.asked[5], "input:Tech stack") {
		t.Fatalf("prompt sequence = %v, want a tech-stack question after the README-only scaffolding", p.asked)
	}
	if !strings.Contains(out, "tech_stack: rust") {
		t.Fatalf("init output missing the recorded tech stack:\n%s", out)
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `tech_stack = "rust"`) {
		t.Fatalf("project config missing the tech stack:\n%s", project)
	}
}

// TestGreenfieldDir pins the classification rule from both directions: a
// directory holding only documentation and other scaffolding is greenfield
// (so the tech-stack question is asked), while any file with a known source or
// build-manifest extension is not. The community-doc shapes are the miss this
// rule replaces: they are greenfield by default rather than by name.
func TestGreenfieldDir(t *testing.T) {
	cases := []struct {
		name  string
		setup []string
		want  bool
	}{
		{name: "empty", want: true},
		{name: "readme", setup: []string{"README.md"}, want: true},
		{name: "bare readme", setup: []string{"README"}, want: true},
		{name: "markdown readme", setup: []string{"README.markdown"}, want: true},
		{name: "rst readme", setup: []string{"README.rst"}, want: true},
		{name: "scaffolding", setup: []string{"README.md", "LICENSE", ".gitignore", ".github/", "docs/"}, want: true},
		{name: "license only", setup: []string{"LICENSE.txt"}, want: true},
		{name: "license variants", setup: []string{"README.md", "LICENSE-MIT", "LICENSE-APACHE", "COPYING", "COPYING.LESSER", "UNLICENSE"}, want: true},
		{name: "community files", setup: []string{"README.md", "UNLICENSE", "CHANGELOG.md", "CONTRIBUTING.md"}, want: true},
		{name: "code of conduct", setup: []string{"README.md", "CODE_OF_CONDUCT.md"}, want: true},
		{name: "security policy", setup: []string{"README.md", "SECURITY.md"}, want: true},
		{name: "support", setup: []string{"README.md", "SUPPORT.md"}, want: true},
		{
			name:  "github scaffold",
			setup: []string{"README.markdown", "CODE_OF_CONDUCT.md", "SECURITY.md", "SUPPORT.md", "LICENSE", ".gitignore", ".github/", "docs/"},
			want:  true,
		},
		{name: "unknown extensionless file", setup: []string{"README.md", "NOTES"}, want: true},
		{name: "go source", setup: []string{"README.md", "main.go"}},
		{name: "python source", setup: []string{"README.md", "app.py"}},
		{name: "typescript source", setup: []string{"README.md", "index.ts"}},
		{name: "license source", setup: []string{"README.md", "license.go"}},
		{name: "npm manifest", setup: []string{"README.md", "package.json"}},
		{name: "go manifest", setup: []string{"README.md", "go.mod"}},
		{name: "python manifest", setup: []string{"README.md", "pyproject.toml"}},
		{name: "cargo manifest", setup: []string{"README.md", "Cargo.toml"}},
		{name: "makefile", setup: []string{"README.md", "Makefile"}},
		{name: "dockerfile", setup: []string{"README.md", "Dockerfile"}},
		{name: "nested source", setup: []string{"README.md", "src/main.go"}},
		{name: "unknown directory", setup: []string{"cmd/"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, entry := range tc.setup {
				path := filepath.Join(dir, strings.TrimSuffix(entry, "/"))
				if strings.HasSuffix(entry, "/") {
					if err := os.MkdirAll(path, 0o755); err != nil {
						t.Fatalf("MkdirAll(%q) error = %v", entry, err)
					}
					continue
				}
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
					t.Fatalf("WriteFile(%q) error = %v", entry, err)
				}
			}
			if got := greenfieldDir(dir); got != tc.want {
				t.Fatalf("greenfieldDir(%v) = %v, want %v", tc.setup, got, tc.want)
			}
		})
	}
}

func TestInitBareInRepoInteractiveNo(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo
	r.isTerminal = terminal
	r.prompt = &scriptedPrompter{t: t, confirms: []bool{false}}

	out := r.run("init")
	if !strings.Contains(out, "created: true") {
		t.Fatalf("init should still create the machine config:\n%s", out)
	}
	if _, err := os.Stat(r.projectPath); !os.IsNotExist(err) {
		t.Fatalf("declining registration must not write the project config (err=%v)", err)
	}
}

func TestInitWritesTildeDBPathUnderHome(t *testing.T) {
	r := newRunner(t)
	home := t.TempDir()
	r.userPath = filepath.Join(home, ".factotum", "config.toml")
	r.getenv = func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	}
	r.gitDetect = fakeRepo

	r.run("init", "-p")
	machine := readFile(t, r.userPath)
	if !strings.Contains(machine, `db_path = "~/.factotum/widget.db"`) {
		t.Fatalf("db_path should be home-relative:\n%s", machine)
	}
	if _, err := os.Stat(filepath.Join(home, ".factotum", "widget.db")); err != nil {
		t.Fatalf("project db not created at the expanded path: %v", err)
	}
}

func TestInitProjectNonGitUsesDirectory(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = noRepo

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	want := app.Slug(filepath.Base(cwd))

	out := r.run("init", "-p")
	if !strings.Contains(out, "project: "+want) {
		t.Fatalf("init -p outside a repo should use the directory name %q:\n%s", want, out)
	}
}

func TestInitProjectIdempotent(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo

	r.run("init", "-p")
	firstMachine := readFile(t, r.userPath)
	firstProject := readFile(t, r.projectPath)

	out := r.run("init", "-p")
	if !strings.Contains(out, "created: false") {
		t.Fatalf("re-running init should report created: false:\n%s", out)
	}
	if got := readFile(t, r.userPath); got != firstMachine {
		t.Fatalf("re-running init changed the machine config:\n%s", got)
	}
	if got := readFile(t, r.projectPath); got != firstProject {
		t.Fatalf("re-running init changed the project config:\n%s", got)
	}
}

func TestInitProjectCreatesUsableProject(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo
	r.run("init", "-p")

	db := filepath.Join(filepath.Dir(r.userPath), "widget.db")
	out := r.run("--store", "sqlite", "--store-opt", "path="+db, "task", "create", "-p", "widget", "-t", "first")
	if !strings.Contains(out, "task_id:") {
		t.Fatalf("freshly initialized project cannot take a task:\n%s", out)
	}
}

func TestInitProjectMissingMachineConfigAsks(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo
	r.isTerminal = terminal
	p := &scriptedPrompter{t: t, confirms: []bool{true}, inputs: []string{"", "", "", "", ""}}
	r.prompt = p

	out := r.run("init", "-p")
	if len(p.asked) == 0 || !strings.Contains(p.asked[0], "No machine config") {
		t.Fatalf("init -p should offer to create the missing machine config, asked=%v", p.asked)
	}
	if !strings.Contains(out, "project: widget") {
		t.Fatalf("init -p did not register after creating the machine config:\n%s", out)
	}
}

func TestInitProjectDeclineMachineConfigAborts(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo
	r.isTerminal = terminal
	r.prompt = &scriptedPrompter{t: t, confirms: []bool{false}}

	if err := r.runErr("init", "-p"); err == nil {
		t.Fatal("declining to create the machine config should abort")
	}
	if _, err := os.Stat(r.userPath); !os.IsNotExist(err) {
		t.Fatalf("declined init must not create the machine config (err=%v)", err)
	}
}

func TestInitRejectsUserOnlyWithProject(t *testing.T) {
	r := newRunner(t)
	if err := r.runErr("init", "-u", "-p"); !errors.Is(err, ErrUsage) {
		t.Fatalf("init -u -p error = %v, want ErrUsage", err)
	}
}

func TestInitRejectsNameWithoutProject(t *testing.T) {
	r := newRunner(t)
	if err := r.runErr("init", "name"); !errors.Is(err, ErrUsage) {
		t.Fatalf("init <name> error = %v, want ErrUsage", err)
	}
}

func TestDetectGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	dir := t.TempDir()
	runGit(t, dir, "init")
	runGit(t, dir, "remote", "add", "origin", "git@github.com:acme/widget.git")

	repo, ok := detectGitRepo(dir)
	if !ok {
		t.Fatal("detectGitRepo() ok = false, want true")
	}
	if repo.name() != "widget" {
		t.Fatalf("repo.name() = %q, want widget", repo.name())
	}
	if resolved, err := filepath.EvalSymlinks(repo.Root); err != nil || resolved == "" {
		t.Fatalf("repo.Root = %q is not a real path (err=%v)", repo.Root, err)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// runRoot drives the CLI through a fresh root with a temp HOME and the given git
// probe, so a test can exercise the default (CWD-relative) config resolution
// instead of the runner's fixed --config path.
func runRoot(t *testing.T, home string, gitDetect func(string) (gitRepo, bool), args ...string) string {
	t.Helper()
	var stdout, stderr bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	})
	deps.GitDetect = gitDetect
	builtins.RegisterAll(deps.StoreFactories)
	root := NewRoot(deps)
	root.SetArgs(append([]string{"--user-config", filepath.Join(home, ".factotum", "config.toml")}, args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
	}
	return stdout.String()
}

// runRootErr is runRoot but returns the command's error instead of failing the
// test, so a test can assert the refused-migration path.
func runRootErr(t *testing.T, home string, gitDetect func(string) (gitRepo, bool), args ...string) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, func(key string) string {
		if key == "HOME" {
			return home
		}
		return ""
	})
	deps.GitDetect = gitDetect
	builtins.RegisterAll(deps.StoreFactories)
	root := NewRoot(deps)
	root.SetArgs(append([]string{"--user-config", filepath.Join(home, ".factotum", "config.toml")}, args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	return stdout.String(), root.Execute()
}

func TestTerminalPrompterInput(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		def    string
		want   string
		prompt string
	}{
		{name: "value", line: "hello\n", def: "def", want: "hello", prompt: "label [def]: "},
		{name: "trimmed", line: "  spaced  \n", want: "spaced", prompt: "label: "},
		{name: "blank uses default", line: "\n", def: "def", want: "def", prompt: "label [def]: "},
		{name: "eof uses default", def: "def", want: "def", prompt: "label [def]: "},
		{name: "eof without default", want: "", prompt: "label: "},
		{name: "value at eof", line: "hello", want: "hello", prompt: "label: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			p := &terminalPrompter{reader: bufio.NewReader(strings.NewReader(tt.line)), out: &out}
			got, err := p.Input("label", tt.def)
			if err != nil {
				t.Fatalf("Input() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Input() = %q, want %q", got, tt.want)
			}
			if out.String() != tt.prompt {
				t.Fatalf("prompt = %q, want %q", out.String(), tt.prompt)
			}
		})
	}
}

func TestTerminalPrompterConfirm(t *testing.T) {
	tests := []struct {
		name   string
		line   string
		def    bool
		want   bool
		prompt string
	}{
		{name: "yes lower", line: "y\n", def: false, want: true, prompt: "label [y/N] "},
		{name: "yes word", line: "yes\n", def: false, want: true, prompt: "label [y/N] "},
		{name: "yes upper", line: "Y\n", def: false, want: true, prompt: "label [y/N] "},
		{name: "no", line: "n\n", def: true, want: false, prompt: "label [Y/n] "},
		{name: "no word", line: "no\n", def: true, want: false, prompt: "label [Y/n] "},
		{name: "anything else is no", line: "maybe\n", def: true, want: false, prompt: "label [Y/n] "},
		{name: "blank keeps true", line: "\n", def: true, want: true, prompt: "label [Y/n] "},
		{name: "blank keeps false", line: "\n", def: false, want: false, prompt: "label [y/N] "},
		{name: "eof keeps default", def: true, want: true, prompt: "label [Y/n] "},
		{name: "yes at eof", line: "y", def: false, want: true, prompt: "label [y/N] "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			p := &terminalPrompter{reader: bufio.NewReader(strings.NewReader(tt.line)), out: &out}
			got, err := p.Confirm("label", tt.def)
			if err != nil {
				t.Fatalf("Confirm() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("Confirm() = %v, want %v", got, tt.want)
			}
			if out.String() != tt.prompt {
				t.Fatalf("prompt = %q, want %q", out.String(), tt.prompt)
			}
		})
	}
}

// TestInitProjectFromSubdirectoryKeepsConfigInCWD pins the accepted behavior:
// `ft init -p` registers the current directory, so the project config is written
// to .factotum/config.toml under the CWD, not the git root. Repository detection
// still uses the git root, which is recorded on the project's repository.
func TestInitProjectFromSubdirectoryKeepsConfigInCWD(t *testing.T) {
	gitRoot := t.TempDir()
	sub := filepath.Join(gitRoot, "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	t.Chdir(sub)

	home := t.TempDir()
	detect := func(string) (gitRepo, bool) {
		return gitRepo{Root: gitRoot, Remote: "git@github.com:acme/widget.git"}, true
	}
	storeArgs := []string{"--store", "jsonfile", "--store-opt", "path=" + filepath.Join(home, "store.json")}

	out := runRoot(t, home, detect, append(storeArgs, "init", "-p")...)
	if !strings.Contains(out, "project: widget") {
		t.Fatalf("init -p did not register the project:\n%s", out)
	}

	cwdConfig := filepath.Join(sub, ".factotum", "config.toml")
	if project := readFile(t, cwdConfig); !strings.Contains(project, `project = "widget"`) {
		t.Fatalf("project config should live under the CWD, missing pin:\n%s", project)
	}
	if _, err := os.Stat(filepath.Join(gitRoot, ".factotum", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("ft init -p must not write the project config at the git root (err=%v)", err)
	}

	db := filepath.Join(home, ".factotum", "widget.db")
	shown := runRoot(t, home, detect, "--store", "sqlite", "--store-opt", "path="+db, "project", "get", "widget")
	if !strings.Contains(shown, gitRoot) {
		t.Fatalf("the project repository should record the git root %s:\n%s", gitRoot, shown)
	}
}
