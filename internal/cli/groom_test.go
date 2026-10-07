package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestProjectDataDirIsAbsolutePerBackend pins that the session data dir is
// always absolute, even when the store path is relative, and that a directory
// backend (jsondir) is used as the dir rather than its parent. A relative dir
// would make the kickoff name a relative path the harness resolves against its
// own CWD, not ft's, so capture would miss the files the session wrote.
func TestProjectDataDirIsAbsolutePerBackend(t *testing.T) {
	tests := []struct {
		name    string
		backend string
		path    string
		want    string
	}{
		{"file backend uses the parent", "jsonfile", "sub/db.json", "sub"},
		{"sqlite uses the parent", "sqlite", "sub/db.sqlite", "sub"},
		{"jsondir uses the directory", "jsondir", "sub/tree", "sub/tree"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := projectDataDir(config.Config{Store: config.Store{
				Backend: tt.backend,
				Options: map[string]string{"path": tt.path},
			}})
			if err != nil {
				t.Fatalf("projectDataDir error = %v", err)
			}
			if !filepath.IsAbs(got) {
				t.Fatalf("projectDataDir = %q, want an absolute path", got)
			}
			if want := filepath.Join(mustAbs(t, "."), filepath.FromSlash(tt.want)); got != want {
				t.Fatalf("projectDataDir = %q, want %q", got, want)
			}
		})
	}
}

func mustAbs(t *testing.T, path string) string {
	t.Helper()
	abs, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// groomBackend is the fake isolation backend `ft groom` runs against. On Exec it
// hands the harness command to onExec, so a test can observe the injected prompt
// and stage the report/deferred files a real session would have written.
type groomBackend struct {
	*isofake.Backend
	onExec func(isolation.Command)
}

func (g groomBackend) Exec(ctx context.Context, h isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	if g.onExec != nil {
		g.onExec(cmd)
	}
	return g.Backend.Exec(ctx, h, cmd)
}

// kickoffPath reads a path from a kickoff line that starts with label.
func kickoffPath(prompt, label string) string {
	for _, line := range strings.Split(prompt, "\n") {
		if strings.HasPrefix(line, label) {
			return strings.TrimSpace(strings.TrimPrefix(line, label))
		}
	}
	return ""
}

// writingGroomBackend returns a backend whose Exec writes the report and the
// deferred-questions file named in the injected kickoff, so `ft groom` captures
// them.
func writingGroomBackend(t *testing.T) groomBackend {
	t.Helper()
	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
	return groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
		prompt := cmd.Argv[len(cmd.Argv)-1]
		report := kickoffPath(prompt, "Write the report to: ")
		deferred := kickoffPath(prompt, "Write the deferred questions to: ")
		if report == "" || deferred == "" {
			t.Fatalf("prompt kickoff does not name both outputs:\n%s", prompt)
		}
		if err := os.MkdirAll(filepath.Dir(report), 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, report, "# Grooming report - 2026-10-07\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n")
		mustWrite(t, deferred, "# Deferred questions\n\n## Questions\n\n## Resolved\n")
	}}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGroomCommandInjectsKickoffAndCapturesOutputs(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Maybe cache"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add widget"))
	groomedID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Already groomed",
		"--groomed", "--acceptance", "works"))

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"session: groom-", "scope:", "report: art-", "deferred: art-", "run: finished", "project: " + projectID} {
		if !strings.Contains(out, want) {
			t.Fatalf("groom output missing %q:\n%s", want, out)
		}
	}

	// The kickoff names the scope and the deterministic output paths, and does
	// not include a groomed task.
	commands := backend.Commands()
	if len(commands) != 1 {
		t.Fatalf("Exec called %d times, want 1", len(commands))
	}
	prompt := commands[0].Argv[len(commands[0].Argv)-1]
	for _, want := range []string{"## Session kickoff", ideaID, taskID, "Write the report to: ", "Write the deferred questions to: "} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("injected prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, groomedID) {
		t.Fatalf("kickoff includes the already-groomed task %s:\n%s", groomedID, prompt)
	}

	// Both outputs exist at the deterministic session path and are doc artifacts.
	reportPath := kickoffPath(prompt, "Write the report to: ")
	deferredPath := kickoffPath(prompt, "Write the deferred questions to: ")
	if !filepath.IsAbs(reportPath) || !filepath.IsAbs(deferredPath) {
		t.Fatalf("kickoff paths must be absolute, got report=%q deferred=%q", reportPath, deferredPath)
	}
	if _, err := os.Stat(reportPath); err != nil {
		t.Fatalf("report not captured at %s: %v", reportPath, err)
	}
	if _, err := os.Stat(deferredPath); err != nil {
		t.Fatalf("deferred questions not captured at %s: %v", deferredPath, err)
	}
	docs := r.run("doc", "list", "-p", projectID)
	for _, want := range []string{"Grooming report", "Grooming deferred questions"} {
		if !strings.Contains(docs, want) {
			t.Fatalf("doc list missing %q:\n%s", want, docs)
		}
	}
}

// TestGroomCommandLeavesNoPartialArtifactWhenOneOutputMissing guards the capture
// order: a session that wrote only the report must not leave a report artifact
// behind when the deferred file is missing.
func TestGroomCommandLeavesNoPartialArtifactWhenOneOutputMissing(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
		prompt := cmd.Argv[len(cmd.Argv)-1]
		report := kickoffPath(prompt, "Write the report to: ")
		mustWrite(t, report, "# Grooming report\n")
	}}
	r.runHarness = harnessfake.New("opencode")

	if err := r.runErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir()); err == nil {
		t.Fatal("groom with only the report error = nil, want a missing-output error")
	}
	if docs := r.run("doc", "list", "-p", projectID); strings.Contains(docs, "Grooming report") {
		t.Fatalf("a missing deferred file left a partial report artifact:\n%s", docs)
	}
}

func TestGroomCommandFailsWhenOutputsMissing(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	err := r.runErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--backend", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if err == nil {
		t.Fatal("groom with no outputs error = nil, want a missing-output error")
	}
	if errors.Is(err, ErrUsage) {
		t.Fatalf("missing output error = %v, want a non-usage input error", err)
	}
	if !strings.Contains(err.Error(), "report.md") {
		t.Fatalf("error does not name the missing report: %v", err)
	}
}
