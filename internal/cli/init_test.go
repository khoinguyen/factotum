package cli

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
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

func TestInitBareInRepoInteractiveYes(t *testing.T) {
	r := newRunner(t)
	r.gitDetect = fakeRepo
	r.isTerminal = terminal
	p := &scriptedPrompter{t: t, confirms: []bool{true}, inputs: []string{"", "", "", ""}}
	r.prompt = p

	out := r.run("init")
	if !strings.Contains(out, "project: widget") {
		t.Fatalf("interactive init did not register the project:\n%s", out)
	}
	if project := readFile(t, r.projectPath); !strings.Contains(project, `project = "widget"`) {
		t.Fatalf("project config missing the pin:\n%s", project)
	}
	if len(p.asked) != 5 || !strings.HasPrefix(p.asked[0], "confirm:") {
		t.Fatalf("prompt sequence = %v, want a confirm then four inputs", p.asked)
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
	p := &scriptedPrompter{t: t, confirms: []bool{true}, inputs: []string{"", "", "", ""}}
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
