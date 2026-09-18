package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store/builtins"
)

type runner struct {
	t           *testing.T
	path        string
	projectPath string
	userPath    string
	lastOut     string
	lastErr     string
}

func newRunner(t *testing.T) *runner {
	t.Helper()
	dir := t.TempDir()
	return &runner{
		t:           t,
		path:        filepath.Join(dir, "factotum.json"),
		projectPath: filepath.Join(dir, "project-config.toml"),
		userPath:    filepath.Join(dir, "user-config.toml"),
	}
}

func (r *runner) run(args ...string) string {
	r.t.Helper()
	out, _ := r.runSplit(args...)
	return out
}

// runSplit executes a command and returns stdout and stderr separately. Config
// files are isolated in the test's temp dir unless the caller overrides them.
func (r *runner) runSplit(args ...string) (string, string) {
	r.t.Helper()
	var stdout, stderr bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, nil)
	builtins.RegisterAll(deps.StoreFactories)

	root := NewRoot(deps)
	root.SetArgs(append([]string{
		"--store", "jsonfile", "--store-opt", "path=" + r.path,
		"--config", r.projectPath, "--user-config", r.userPath,
	}, args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil && !errors.Is(err, ErrUsage) {
		r.t.Fatalf("execute %v: %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
	}
	r.lastOut, r.lastErr = stdout.String(), stderr.String()
	return r.lastOut, r.lastErr
}

func firstField(t *testing.T, out string) string {
	t.Helper()
	line := strings.TrimSpace(out)
	if line == "" {
		t.Fatal("expected output, got empty")
	}
	return strings.SplitN(line, "\t", 2)[0]
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", path, err)
	}
	return string(data)
}

func TestProjectCreateSlugID(t *testing.T) {
	r := newRunner(t)
	if got := firstField(t, r.run("project", "create", "Kloobot")); got != "kloobot" {
		t.Fatalf("project id = %q, want kloobot", got)
	}
}

func TestTaskNextHeader(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "add", "--project", projectID, "--title", "one")

	out := r.run("task", "next", "--project", projectID)
	header := strings.SplitN(out, "\n", 2)[0]
	if fields := strings.Fields(header); len(fields) != 3 || fields[0] != "SCORE" || fields[1] != "TASK" || fields[2] != "TITLE" {
		t.Fatalf("task next missing header: %q", header)
	}
	if jsonOut := r.run("task", "next", "--project", projectID, "-o", "json"); strings.Contains(jsonOut, "SCORE") {
		t.Fatalf("json output should not contain the text header:\n%s", jsonOut)
	}
}

func TestPrintTableAlignsColumns(t *testing.T) {
	var buf bytes.Buffer
	deps := &Deps{Out: &buf}
	deps.printTable(
		[]string{"ID", "REPO", "TITLE"},
		[][]string{
			{"a", "", "hello"},
			{"long-id", "backend", "world"},
		},
	)
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines, got %d:\n%s", len(lines), buf.String())
	}
	if strings.Index(lines[0], "REPO") != strings.Index(lines[2], "backend") {
		t.Fatalf("REPO column not aligned:\n%s", buf.String())
	}
	if strings.Index(lines[0], "TITLE") != strings.Index(lines[2], "world") {
		t.Fatalf("TITLE column not aligned:\n%s", buf.String())
	}
}

func TestParseRepoSpec(t *testing.T) {
	tests := []struct {
		name    string
		spec    string
		want    core.Repository
		wantErr bool
	}{
		{"name only", "backend", core.Repository{Name: "backend"}, false},
		{"name and url", "backend=git@example.com:acme/backend.git", core.Repository{Name: "backend", URL: "git@example.com:acme/backend.git"}, false},
		{
			"full",
			"name=web,url=git@example.com:acme/web.git,path=repos/web,brief=Frontend app",
			core.Repository{Name: "web", URL: "git@example.com:acme/web.git", Path: "repos/web", Description: "Frontend app"},
			false,
		},
		{"empty", "", core.Repository{}, true},
		{"missing name", "url=git@example.com/x.git", core.Repository{}, true},
		{"unknown field", "name=web,unknown=1", core.Repository{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRepoSpec(tt.spec)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseRepoSpec(%q) error = %v, wantErr %v", tt.spec, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if got != tt.want {
				t.Fatalf("parseRepoSpec(%q) = %+v, want %+v", tt.spec, got, tt.want)
			}
		})
	}
}

func TestTaskShowResolvesActorsAndNotes(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "add", "--kind", "agent", "claude")
	taskID := firstField(t, r.run("task", "add", "--project", projectID, "--title", "work"))
	r.run("task", "assign", taskID, "--actor", "claude")
	r.run("task", "note", "add", taskID, "--body", "first note")
	r.run("task", "note", "add", taskID, "--body", "second note")

	shown := r.run("task", "show", taskID)
	if !strings.HasPrefix(shown, "(todo) ") {
		t.Fatalf("header should be '(status) ID: title':\n%s", shown)
	}
	if !strings.Contains(shown, "assignee: (agent) claude") {
		t.Fatalf("assignee not resolved to (kind) name:\n%s", shown)
	}
	for _, want := range []string{"=== Notes ===", "first note", "second note"} {
		if !strings.Contains(shown, want) {
			t.Fatalf("task show missing %q:\n%s", want, shown)
		}
	}
}

func TestTaskBodyFile(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	bodyFile := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(bodyFile, []byte("# Long description\n\nlots of detail\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	taskID := firstField(t, r.run("task", "add", "--project", projectID, "--title", "big", "--body-file", bodyFile))
	shown := r.run("task", "show", taskID)
	if !strings.Contains(shown, "=== Description ===") || !strings.Contains(shown, "lots of detail") {
		t.Fatalf("body-file not applied:\n%s", shown)
	}

	if err := os.WriteFile(bodyFile, []byte("revised body\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	r.run("task", "update", taskID, "--body-file", bodyFile)
	if shown := r.run("task", "show", taskID); !strings.Contains(shown, "revised body") {
		t.Fatalf("body-file update not applied:\n%s", shown)
	}
}

func TestTaskUpdateKind(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "add", "--project", projectID, "--title", "feature flag rollout"))

	r.run("task", "update", taskID, "--kind", "milestone")
	if shown := r.run("task", "show", taskID); !strings.Contains(shown, "milestone") {
		t.Fatalf("task show does not reflect milestone kind:\n%s", shown)
	}
}

func TestTaskRepoFlow(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("project", "repo", "add", projectID, "data", "--brief", "data repo")
	r.run("project", "repo", "add", projectID, "devops", "--brief", "infra")

	dataID := firstField(t, r.run("task", "add", "--project", projectID, "--repo", "data", "--title", "model customers"))
	r.run("task", "add", "--project", projectID, "--repo", "devops", "--title", "provision db")

	listed := r.run("task", "list", "--project", projectID, "--repo", "data")
	if !strings.Contains(listed, dataID) || strings.Contains(listed, "provision db") {
		t.Fatalf("repo filter wrong:\n%s", listed)
	}

	if shown := r.run("task", "show", dataID); !strings.Contains(shown, "repo: data") {
		t.Fatalf("show missing repo:\n%s", shown)
	}
	r.run("task", "update", dataID, "--repo", "devops")
	if shown := r.run("task", "show", dataID); !strings.Contains(shown, "repo: devops") {
		t.Fatalf("update repo failed:\n%s", shown)
	}

	next := r.run("task", "next", "--project", projectID, "--repo", "devops")
	if !strings.Contains(next, dataID) {
		t.Fatalf("next --repo missing task:\n%s", next)
	}
}

func TestTaskAddRejectsUnknownRepo(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, nil)
	builtins.RegisterAll(deps.StoreFactories)
	root := NewRoot(deps)
	root.SetArgs([]string{"--store", "jsonfile", "--store-opt", "path=" + r.path,
		"task", "add", "--project", projectID, "--repo", "nope", "--title", "x"})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err == nil {
		t.Fatal("expected invalid repo error, got nil")
	}
}

func TestProjectRepoCommands(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))

	r.run("project", "repo", "add", projectID, "backend", "--url", "git@example.com:acme/backend.git", "--path", "repos/backend", "--brief", "API service")
	if out := r.run("project", "repo", "list", projectID); !strings.Contains(out, "API service") {
		t.Fatalf("repo list missing brief:\n%s", out)
	}

	r.run("project", "repo", "update", projectID, "backend", "--brief", "API and workers")
	if out := r.run("project", "show", projectID); !strings.Contains(out, "API and workers") {
		t.Fatalf("project show missing updated brief:\n%s", out)
	}

	r.run("project", "repo", "rm", projectID, "backend")
	if out := r.run("project", "repo", "list", projectID); strings.Contains(out, "backend") {
		t.Fatalf("repo list still shows removed repo:\n%s", out)
	}
}

func TestProjectCreateWithRepoSpecifics(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Beta",
		"--repo", "name=web,url=git@example.com:acme/web.git,brief=Frontend app,path=repos/web"))
	if out := r.run("project", "show", projectID); !strings.Contains(out, "Frontend app") {
		t.Fatalf("project show missing repo brief:\n%s", out)
	}
}

func TestVersion(t *testing.T) {
	r := newRunner(t)
	if got := strings.TrimSpace(r.run("version")); got == "" {
		t.Fatal("version output is empty")
	}
}

func TestCLIFlow(t *testing.T) {
	r := newRunner(t)

	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("actor", "add", "--kind", "agent", "claude")
	r.run("actor", "add", "--kind", "human", "Khoi")

	firstID := firstField(t, r.run("task", "add", "--project", projectID, "--title", "first"))
	secondID := firstField(t, r.run("task", "add", "--project", projectID, "--title", "second", "--dep", firstID))

	before := r.run("graph", "render", "--project", projectID, "--format", "agent")
	if !strings.Contains(before, firstID) {
		t.Fatalf("agent render missing ready task %s:\n%s", firstID, before)
	}

	r.run("task", "review", firstID)
	if shown := r.run("task", "show", firstID); !strings.Contains(shown, "ready_for_review") {
		t.Fatalf("task status not updated:\n%s", shown)
	}
	after := r.run("graph", "render", "--project", projectID, "--format", "agent")
	if !strings.Contains(after, secondID) {
		t.Fatalf("agent render missing unblocked task %s:\n%s", secondID, after)
	}

	nextOut := r.run("task", "next", "--project", projectID, "--for", "Khoi")
	if !strings.Contains(nextOut, secondID) {
		t.Fatalf("task next missing %s:\n%s", secondID, nextOut)
	}

	events := r.run("event", "list", "--project", projectID)
	if !strings.Contains(events, "project.created") {
		t.Fatalf("event log missing project.created:\n%s", events)
	}
}

func TestAddDepRejectsCycleViaCLI(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	firstID := firstField(t, r.run("task", "add", "--project", projectID, "--title", "first"))
	secondID := firstField(t, r.run("task", "add", "--project", projectID, "--title", "second", "--dep", firstID))

	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, nil)
	builtins.RegisterAll(deps.StoreFactories)
	root := NewRoot(deps)
	root.SetArgs([]string{"--store", "jsonfile", "--store-opt", "path=" + r.path, "task", "dep", "add", firstID, secondID})
	root.SetOut(&out)
	root.SetErr(&out)
	if err := root.Execute(); err == nil {
		t.Fatal("expected cycle error, got nil")
	}
}

func TestGraphRenderHTMLToFile(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	r.run("task", "add", "--project", projectID, "--title", "first")

	outFile := filepath.Join(t.TempDir(), "dag.html")
	r.run("graph", "render", "--project", projectID, "--format", "html", "--layout", "tree", "--out", outFile)

	content := readFile(t, outFile)
	if !strings.Contains(content, "The graph") {
		t.Fatalf("html output missing graph section:\n%s", content)
	}
}
