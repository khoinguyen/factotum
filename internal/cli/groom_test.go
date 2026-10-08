package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
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

// stageGroomOutputs stages the two outputs a session would write, at the
// workspace-relative paths the kickoff names, into the fake environment's
// filesystem so the run service captures them.
func stageGroomOutputs(t *testing.T, base *isofake.Backend, prompt, reportBody, deferredBody string) {
	t.Helper()
	report := kickoffPath(prompt, "Write the report to: ")
	deferred := kickoffPath(prompt, "Write the deferred questions to: ")
	if report == "" || deferred == "" {
		t.Fatalf("prompt kickoff does not name both outputs:\n%s", prompt)
	}
	base.Stage(
		isolation.File{Path: report, Content: []byte(reportBody)},
		isolation.File{Path: deferred, Content: []byte(deferredBody)},
	)
}

// writingGroomBackend returns a backend whose Exec stages the report and the
// deferred-questions file named in the injected kickoff, so `ft groom` captures
// them.
func writingGroomBackend(t *testing.T) groomBackend {
	t.Helper()
	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
	return groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
		prompt := cmd.Argv[len(cmd.Argv)-1]
		stageGroomOutputs(t, base, prompt,
			"# Grooming report - 2026-10-07\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n",
			"# Deferred questions\n\n## Questions\n\n## Resolved\n")
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
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	for _, want := range []string{"session: groom-", "scope:", "report: art-", "deferred: art-", "run: finished", "mode: headless", "project: " + projectID} {
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

	// The kickoff names workspace-relative output paths, so the session never
	// writes outside its workspace; ft copies both into the durable session dir.
	reportRel := kickoffPath(prompt, "Write the report to: ")
	deferredRel := kickoffPath(prompt, "Write the deferred questions to: ")
	for _, rel := range []string{reportRel, deferredRel} {
		if filepath.IsAbs(rel) || !strings.HasPrefix(rel, groom.StagingDirName+"/") {
			t.Fatalf("kickoff output path %q is not workspace-relative under %s", rel, groom.StagingDirName)
		}
	}
	dataDir := filepath.Dir(r.path)
	sessionID := firstField(t, out)
	for _, path := range []string{groom.ReportPath(dataDir, sessionID), groom.DeferredQuestionsPath(dataDir, sessionID)} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("output not captured at %s: %v", path, err)
		}
	}
	docs := r.run("doc", "list", "-p", projectID)
	for _, want := range []string{"Grooming report", "Grooming deferred questions"} {
		if !strings.Contains(docs, want) {
			t.Fatalf("doc list missing %q:\n%s", want, docs)
		}
	}
}

// TestGroomInteractiveOnTerminalAttachesAgent pins the mode selection: on a
// terminal without --unattended the session runs attached (the harness command
// requests a TTY) and the manifest records mode interactive. --unattended on the
// same terminal stays headless and records mode unattended.
func TestGroomInteractiveOnTerminalAttachesAgent(t *testing.T) {
	tests := []struct {
		name       string
		unattended bool
		wantTTY    bool
		wantMode   string
	}{
		{"terminal without unattended attaches", false, true, "interactive"},
		{"terminal with unattended stays headless", true, false, "unattended"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.isTerminal = func(io.Writer) bool { return true }
			r.stdinTerminal = func(io.Reader) bool { return true }
			projectID, cfgPath := tasklessContext(t, r)
			r.run("actor", "create", "builder", "--kind", "agent")
			taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add widget"))
			groomAssign(t, r, taskID)

			promptPath := filepath.Join(t.TempDir(), "prompt.md")
			mustWrite(t, promptPath, "# Grooming session prompt\n")

			backend := writingGroomBackend(t)
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			args := []string{"--config", cfgPath, "groom", "-p", projectID,
				"--prompt-file", promptPath, "--sandbox", "fake", "--harness", "fake",
				"--workspace", t.TempDir(), taskID}
			if tc.unattended {
				args = append(args, "--unattended")
			}
			out, err := runGroomCapture(r, args...)
			if err != nil {
				t.Fatalf("groom error = %v\n%s", err, out)
			}
			if !strings.Contains(out, "mode: "+tc.wantMode) {
				t.Fatalf("groom output missing mode %q:\n%s", tc.wantMode, out)
			}
			cmds := backend.Commands()
			if len(cmds) != 1 {
				t.Fatalf("Exec called %d times, want 1", len(cmds))
			}
			if cmds[0].TTY != tc.wantTTY {
				t.Fatalf("TTY = %v, want %v", cmds[0].TTY, tc.wantTTY)
			}
		})
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
		base.Stage(isolation.File{Path: report, Content: []byte("# Grooming report\n")})
	}}
	r.runHarness = harnessfake.New("opencode")

	if err := r.runErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir()); err == nil {
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
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
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

// runGroomCapture executes the groom command and returns stdout plus the error
// without failing the test, so a table can drive both clean and rejected runs.
func runGroomCapture(r *runner, args ...string) (string, error) {
	var out bytes.Buffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &out, &out, nil)
	base := r.setup(deps)
	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&out)
	root.SetErr(&out)
	err := root.Execute()
	return out.String(), err
}

// groomAssign makes a task agent-ready: groomed with a criterion, assigned to an
// agent actor.
func groomAssign(t *testing.T, r *runner, taskID string) {
	t.Helper()
	r.run("task", "update", taskID, "--groomed", "--acceptance", "works")
	r.run("task", "assign", taskID, "--actor", "builder")
}

// agentReadyTask creates a task and leaves it in the agent bucket.
func agentReadyTask(t *testing.T, r *runner, projectID, title string) string {
	t.Helper()
	id := firstField(t, r.run("task", "create", "-p", projectID, "-t", title))
	groomAssign(t, r, id)
	return id
}

// TestGroomUnattendedCompletesOrDefers is the acceptance table for unattended
// mode: a session with no product owner must leave every scoped item either
// agent-ready (groomed and assigned to an agent) or named in the
// deferred-questions file. The table seeds the graph state a session would
// leave, the fake harness writes the two outputs it would, and the command
// accepts the run only when every item resolves.
func TestGroomUnattendedCompletesOrDefers(t *testing.T) {
	const reportBody = "# Grooming report - 2026-10-07\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n"
	const emptyDeferred = "# Deferred questions\n\n## Questions\n\n## Resolved\n"

	tests := []struct {
		name    string
		build   func(t *testing.T, r *runner, projectID string) (scope []string, deferred string)
		wantErr string
	}{
		{
			name: "both items agent-ready",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				return []string{
					agentReadyTask(t, r, projectID, "Add widget"),
					agentReadyTask(t, r, projectID, "Add gadget"),
				}, emptyDeferred
			},
		},
		{
			name: "an unresolved item deferred to the PO",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				ready := agentReadyTask(t, r, projectID, "Add widget")
				open := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add gadget"))
				deferred := "# Deferred questions\n\n## Questions\n\n- Which scope? (item: " + open + ", owner: PO)\n\n## Resolved\n"
				return []string{ready, open}, deferred
			},
		},
		{
			name: "an item neither agent-ready nor deferred",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				ready := agentReadyTask(t, r, projectID, "Add widget")
				open := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add gadget"))
				return []string{ready, open}, emptyDeferred
			},
			wantErr: "neither agent-ready nor deferred",
		},
		{
			name: "a promoted idea is agent-ready",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				ready := agentReadyTask(t, r, projectID, "Add widget")
				idea := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Maybe cache"))
				promoted := firstField(t, r.run("task", "create", "-p", projectID, "-t", "From idea", "--dep", idea))
				groomAssign(t, r, promoted)
				return []string{ready, idea}, emptyDeferred
			},
		},
		{
			name: "a resolved item is handled",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				done := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add widget"))
				r.run("task", "set", done, "status=ready_for_review")
				return []string{done}, emptyDeferred
			},
		},
		{
			name: "a groomed task assigned to a human is not agent-ready",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				r.run("actor", "create", "khoi", "--kind", "human")
				id := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add widget"))
				r.run("task", "update", id, "--groomed", "--acceptance", "works")
				r.run("task", "assign", id, "--actor", "khoi")
				return []string{id}, emptyDeferred
			},
			wantErr: "neither agent-ready nor deferred",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			projectID, cfgPath := tasklessContext(t, r)
			r.run("actor", "create", "builder", "--kind", "agent")
			scope, deferredBody := tt.build(t, r, projectID)

			promptPath := filepath.Join(t.TempDir(), "prompt.md")
			mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

			base := isofake.New("sandbox")
			base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
			backend := groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
				prompt := cmd.Argv[len(cmd.Argv)-1]
				if !strings.Contains(prompt, "## Unattended mode") {
					t.Errorf("unattended kickoff is missing the override:\n%s", prompt)
				}
				stageGroomOutputs(t, base, prompt, reportBody, deferredBody)
			}}
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			args := append([]string{"--config", cfgPath, "groom", "-p", projectID, "--unattended",
				"--prompt-file", promptPath, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir()}, scope...)
			out, err := runGroomCapture(r, args...)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("groom error = %v, want %q\n%s", err, tt.wantErr, out)
				}
				return
			}
			if err != nil {
				t.Fatalf("groom error = %v\n%s", err, out)
			}
			if !strings.Contains(out, "mode: unattended") {
				t.Fatalf("groom output missing the mode:\n%s", out)
			}
		})
	}
}

// TestGroomUnattendedReadsSessionWrites guards the store refresh: the session
// mutates the graph in its own process (here a second backend over the same
// jsonfile store), so the guard must re-read the store rather than serve the
// snapshot the parent loaded before the run. A caching backend (jsonfile,
// jsondir) would otherwise report the item unresolved even though the session
// groomed it.
func TestGroomUnattendedReadsSessionWrites(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("actor", "create", "builder", "--kind", "agent")
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add widget"))

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
	backend := groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
		prompt := cmd.Argv[len(cmd.Argv)-1]
		// The session edits the graph through its own ft process.
		groomAssign(t, r, taskID)
		stageGroomOutputs(t, base, prompt,
			"# Grooming report\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n",
			"# Deferred questions\n\n## Questions\n\n## Resolved\n")
	}}
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, err := runGroomCapture(r, "--config", cfgPath, "groom", "-p", projectID, "--unattended",
		"--prompt-file", promptPath, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if err != nil {
		t.Fatalf("groom did not see the session's writes: %v\n%s", err, out)
	}
}
