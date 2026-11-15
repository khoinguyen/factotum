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
	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
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
			got, err := projectDataDir(config.Store{
				Backend: tt.backend,
				Options: map[string]string{"path": tt.path},
			})
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

// groomDocs holds the bodies a fake session stages for its five deterministic
// outputs.
type groomDocs struct {
	report     string
	deferred   string
	spec       string
	plan       string
	techDesign string
}

// defaultGroomDocs returns well-formed, section-complete bodies for every
// output, so a test only overrides the document it is exercising.
func defaultGroomDocs() groomDocs {
	return groomDocs{
		report:     "# Grooming report - 2026-10-07\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n",
		deferred:   "# Deferred questions\n\n## Questions\n\n## Resolved\n",
		spec:       "# Feature spec - cache\n\n## Summary\n\n## Problem\n\n## Goals\n\n## Non-goals\n\n## Requirements\n\n## Acceptance\n",
		plan:       "# Feature plan - cache\n\n## Summary\n\n## Milestones\n\n## Tasks\n\n## Dependencies\n\n## Verification\n\n## Rollout\n",
		techDesign: "# Feature tech design - cache\n\n## Summary\n\n## Context\n\n## Design\n\n## Interfaces\n\n## Data\n\n## Cross-cutting impact\n\n## Risks\n",
	}
}

// stageGroomOutputs stages the five outputs a session would write, at the
// workspace-relative paths the kickoff names, into the fake environment's
// filesystem so the run service captures them.
func stageGroomOutputs(t *testing.T, base *isofake.Backend, prompt string, docs groomDocs) {
	t.Helper()
	paths := map[string]string{
		"report":      kickoffPath(prompt, "Write the report to: "),
		"deferred":    kickoffPath(prompt, "Write the deferred questions to: "),
		"spec":        kickoffPath(prompt, "Write the feature spec to: "),
		"plan":        kickoffPath(prompt, "Write the feature plan to: "),
		"tech_design": kickoffPath(prompt, "Write the feature tech design to: "),
	}
	for key, path := range paths {
		if path == "" {
			t.Fatalf("prompt kickoff does not name the %s output:\n%s", key, prompt)
		}
	}
	base.Stage(
		isolation.File{Path: paths["report"], Content: []byte(docs.report)},
		isolation.File{Path: paths["deferred"], Content: []byte(docs.deferred)},
		isolation.File{Path: paths["spec"], Content: []byte(docs.spec)},
		isolation.File{Path: paths["plan"], Content: []byte(docs.plan)},
		isolation.File{Path: paths["tech_design"], Content: []byte(docs.techDesign)},
	)
}

// writingGroomBackend returns a backend whose Exec stages every output named in
// the injected kickoff, so `ft groom` captures them.
func writingGroomBackend(t *testing.T) groomBackend {
	t.Helper()
	base := isofake.New("sandbox")
	base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
	return groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
		prompt := cmd.Argv[len(cmd.Argv)-1]
		stageGroomOutputs(t, base, prompt, defaultGroomDocs())
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
	for _, want := range []string{
		"session: groom-", "scope:", "report: art-", "deferred: art-",
		"spec: art-", "plan: art-", "tech_design: art-",
		"run: finished", "mode: headless", "project: " + projectID,
	} {
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
	for _, want := range []string{
		"## Session kickoff", ideaID, taskID,
		"Write the report to: ", "Write the deferred questions to: ",
		"Write the feature spec to: ", "Write the feature plan to: ",
		"Write the feature tech design to: ",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("injected prompt missing %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, groomedID) {
		t.Fatalf("kickoff includes the already-groomed task %s:\n%s", groomedID, prompt)
	}

	// The kickoff names workspace-relative output paths, so the session never
	// writes outside its workspace; ft copies all five into the durable session
	// dir.
	for _, label := range []string{
		"Write the report to: ", "Write the deferred questions to: ",
		"Write the feature spec to: ", "Write the feature plan to: ",
		"Write the feature tech design to: ",
	} {
		rel := kickoffPath(prompt, label)
		if filepath.IsAbs(rel) || !strings.HasPrefix(rel, groom.StagingDirName+"/") {
			t.Fatalf("kickoff output path for %q = %q, not workspace-relative under %s", label, rel, groom.StagingDirName)
		}
	}
	dataDir := filepath.Dir(r.path)
	sessionID := firstField(t, out)
	for _, path := range []string{
		groom.ReportPath(dataDir, sessionID),
		groom.DeferredQuestionsPath(dataDir, sessionID),
		groom.SpecPath(dataDir, sessionID),
		groom.PlanPath(dataDir, sessionID),
		groom.TechDesignPath(dataDir, sessionID),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("output not captured at %s: %v", path, err)
		}
	}
	docs := r.run("doc", "list", "-p", projectID)
	for _, want := range []string{"Grooming report", "Grooming deferred questions", "Feature spec", "Feature plan", "Feature tech design"} {
		if !strings.Contains(docs, want) {
			t.Fatalf("doc list missing %q:\n%s", want, docs)
		}
	}
	// The spec is recorded with the spec kind so `ft doc list -k spec` finds it.
	specs := r.run("doc", "list", "-p", projectID, "-k", "spec")
	if !strings.Contains(specs, "Feature spec") {
		t.Fatalf("doc list -k spec missing the session spec:\n%s", specs)
	}
}

// TestGroomCommandCleansUpStagingDir pins the hygiene fix: the session stages its
// outputs inside the run workspace and `ft groom` removes the staging tree once
// the run has captured them, so a reused workspace does not accumulate
// .ft-groom/<id> directories. Capture happens even when the harness exits
// non-zero, so a failed session is cleaned up too.
func TestGroomCommandCleansUpStagingDir(t *testing.T) {
	for _, tt := range []struct {
		name     string
		exitCode int
	}{
		{"after a completed session", 0},
		{"after a failed session", 3},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			projectID, cfgPath := tasklessContext(t, r)
			r.run("task", "create", "-p", projectID, "-t", "Add widget")

			promptPath := filepath.Join(t.TempDir(), "prompt.md")
			mustWrite(t, promptPath, "# Grooming session prompt\n")

			workspace := t.TempDir()
			base := isofake.New("sandbox")
			base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: tt.exitCode})
			r.runBackend = groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
				prompt := cmd.Argv[len(cmd.Argv)-1]
				stageGroomOutputs(t, base, prompt, defaultGroomDocs())
				// A real local session writes the staged files into its workspace;
				// mirror that on disk so cleanup has a tree to remove.
				for _, label := range []string{
					"Write the report to: ", "Write the deferred questions to: ",
					"Write the feature spec to: ", "Write the feature plan to: ",
					"Write the feature tech design to: ",
				} {
					full := filepath.Join(workspace, filepath.FromSlash(kickoffPath(prompt, label)))
					if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(full, []byte("body\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}}
			r.runHarness = harnessfake.New("opencode")

			out, err := runGroomCapture(r, "--config", cfgPath, "groom", "-p", projectID,
				"--prompt-file", promptPath, "--sandbox", "fake", "--harness", "fake",
				"--workspace", workspace)
			if tt.exitCode == 0 && err != nil {
				t.Fatalf("groom error = %v\n%s", err, out)
			}
			if tt.exitCode != 0 && err == nil {
				t.Fatalf("groom with a failing harness error = nil, want a run error\n%s", out)
			}
			if _, err := os.Stat(filepath.Join(workspace, groom.StagingDirName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("groom left its staging dir behind: %v", err)
			}
		})
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
			stage := backend.onExec
			backend.onExec = func(cmd isolation.Command) {
				stage(cmd)
				ackSession(t, r, cmd.Env[harnesspkg.EnvSession], projectID)
			}
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

// TestGroomCommandPrintsHarnessStderrOnFailure proves a session the harness
// killed reports why: the harness's stderr tail is printed as the same
// `stderr:` block `ft run` emits, so the failure names the cause instead of only
// the exit code.
func TestGroomCommandPrintsHarnessStderrOnFailure(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{
		Stderr:   []byte("agent said: no API key configured\n"),
		ExitCode: 3,
	})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderrOut, err := r.runSplitErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("groom error = %v, want ErrRunFailed", err)
	}
	if want := "stderr:\nagent said: no API key configured"; !strings.Contains(stderrOut, want) {
		t.Fatalf("groom stderr missing the harness's stderr block %q:\n%s", want, stderrOut)
	}
}

// TestGroomCommandBoundsHarnessStderr pins that the reported block carries the
// bounded tail, not the whole stream: a chatty harness cannot flood the
// terminal, and the reason it failed last is still there.
func TestGroomCommandBoundsHarnessStderr(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{
		Stderr:   []byte("start-of-stream\n" + strings.Repeat("x", 6000) + "\nfatal: no API key\n"),
		ExitCode: 3,
	})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderrOut, err := r.runSplitErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("groom error = %v, want ErrRunFailed", err)
	}
	for _, want := range []string{"stderr:", "... [truncated]", "fatal: no API key"} {
		if !strings.Contains(stderrOut, want) {
			t.Fatalf("groom stderr missing %q:\n%s", want, stderrOut)
		}
	}
	if strings.Contains(stderrOut, "start-of-stream") {
		t.Fatalf("groom stderr carried the head of an unbounded stream:\n%s", stderrOut)
	}
}

// TestGroomCommandStructuredOutputOmitsStderrBlock keeps json/yaml failures
// machine-readable: the harness's error stream is a text block only, so a
// structured caller reads the error envelope without interleaved prose.
func TestGroomCommandStructuredOutputOmitsStderrBlock(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{
		Stderr:   []byte("agent said: no API key configured\n"),
		ExitCode: 3,
	})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderrOut, err := r.runSplitErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath,
		"-o", "json", "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !errors.Is(err, app.ErrRunFailed) {
		t.Fatalf("groom error = %v, want ErrRunFailed", err)
	}
	if strings.Contains(stderrOut, "agent said: no API key configured") {
		t.Fatalf("structured groom output carried the text stderr block:\n%s", stderrOut)
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
			name: "a deferred id that only shares a prefix does not excuse the item",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				ready := agentReadyTask(t, r, projectID, "Add widget")
				open := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add gadget"))
				// A longer id naming a different item: a substring check would
				// find open inside it and wrongly pass the session.
				deferred := "# Deferred questions\n\n## Questions\n\n- [ ] Which scope?\n  - item: " + open + "9\n  - owner: PO\n\n## Resolved\n"
				return []string{ready, open}, deferred
			},
			wantErr: "neither agent-ready nor deferred",
		},
		{
			name: "an id quoted in an indented code block does not excuse the item",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				ready := agentReadyTask(t, r, projectID, "Add widget")
				open := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add gadget"))
				// The id appears only as a quoted sample, the way a session
				// copying a command into its notes would write it.
				deferred := "# Deferred questions\n\n## Questions\n\n    ft task get " + open + "\n\n## Resolved\n"
				return []string{ready, open}, deferred
			},
			wantErr: "neither agent-ready nor deferred",
		},
		{
			name: "a blank-line indented continuation under a list item defers the item",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				ready := agentReadyTask(t, r, projectID, "Add widget")
				open := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add gadget"))
				// Four spaces under a bullet is a paragraph inside the item, not a
				// code block: pandoc renders it as a <p> in the <li>.
				deferred := "# Deferred questions\n\n## Questions\n\n- [ ] Which scope?\n\n    item: " + open + "\n    owner: PO\n\n## Resolved\n"
				return []string{ready, open}, deferred
			},
		},
		{
			name: "a lazy continuation keeps the list open for the indented continuation",
			build: func(t *testing.T, r *runner, projectID string) ([]string, string) {
				ready := agentReadyTask(t, r, projectID, "Add widget")
				open := firstField(t, r.run("task", "create", "-p", projectID, "-t", "Add gadget"))
				// The column-0 line is a lazy continuation of the item's
				// paragraph, so the item is still open for the 4-space line.
				deferred := "# Deferred questions\n\n## Questions\n\n- [ ] Which scope?\nlazy prose\n\n    item: " + open + "\n\n## Resolved\n"
				return []string{ready, open}, deferred
			},
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
				ackSession(t, r, cmd.Env[harnesspkg.EnvSession], projectID)
				docs := defaultGroomDocs()
				docs.report, docs.deferred = reportBody, deferredBody
				stageGroomOutputs(t, base, prompt, docs)
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
		ackSession(t, r, cmd.Env[harnesspkg.EnvSession], projectID)
		groomAssign(t, r, taskID)
		docs := defaultGroomDocs()
		docs.report = "# Grooming report\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n"
		stageGroomOutputs(t, base, prompt, docs)
	}}
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out, err := runGroomCapture(r, "--config", cfgPath, "groom", "-p", projectID, "--unattended",
		"--prompt-file", promptPath, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if err != nil {
		t.Fatalf("groom did not see the session's writes: %v\n%s", err, out)
	}
}

// TestGroomUnattendedProducedChildReadiness pins the produced-child half of the
// unattended completion contract: every executable task the session creates must
// be agent-ready (groomed and assigned to an agent) or resolved by the time it
// ends, even when every scoped item is agent-ready, and naming it in the
// deferred-questions file does not excuse it - a produced task left unready is
// work the session itself created and stranded. A produced idea is a capture,
// not work, so it needs nothing more.
//
// The produced set is attributed, not diffed: a task the run itself created is
// stamped with the session id the run injects (FACTOTUM_SESSION), so a task
// another writer creates while the session runs - the chief filing a follow-up,
// a parallel builder - is not judged as the session's own work and cannot fail
// an unattended run.
func TestGroomUnattendedProducedChildReadiness(t *testing.T) {
	const report = "# Grooming report\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n"
	tests := []struct {
		name string
		// produce creates the task the run leaves behind and returns its id. The
		// session argument is the id the run injected, so a case can create work
		// as the session or as another writer.
		produce func(t *testing.T, r *runner, projectID, session string) string
		// deferred wraps produce's id in a deferred-questions body; nil leaves
		// the file empty.
		deferred func(id string) string
		// external pins a case whose task belongs to another writer, so a
		// passing run must prove the guard ignored a real unready task.
		external bool
		wantErr  bool
	}{
		{
			name: "an unready produced child fails",
			produce: func(t *testing.T, r *runner, projectID, session string) string {
				var id string
				asSession(t, r, session, func() {
					id = firstField(t, r.run("task", "create", "-p", projectID, "-t", "Split child"))
				})
				return id
			},
			wantErr: true,
		},
		{
			name: "an agent-ready produced child passes",
			produce: func(t *testing.T, r *runner, projectID, session string) string {
				var id string
				asSession(t, r, session, func() {
					id = agentReadyTask(t, r, projectID, "Split child")
				})
				return id
			},
		},
		{
			name: "a deferral does not excuse an unready produced child",
			produce: func(t *testing.T, r *runner, projectID, session string) string {
				var id string
				asSession(t, r, session, func() {
					id = firstField(t, r.run("task", "create", "-p", projectID, "-t", "Split child"))
				})
				return id
			},
			deferred: func(id string) string {
				return "# Deferred questions\n\n## Questions\n\n- [ ] Which scope?\n  - item: " + id + "\n  - owner: PO\n\n## Resolved\n"
			},
			wantErr: true,
		},
		{
			name: "a produced idea is a capture",
			produce: func(t *testing.T, r *runner, projectID, session string) string {
				var id string
				asSession(t, r, session, func() {
					id = firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Maybe cache"))
				})
				return id
			},
		},
		{
			name: "a concurrent writer's unready task is not produced",
			produce: func(t *testing.T, r *runner, projectID, _ string) string {
				return firstField(t, r.run("task", "create", "-p", projectID, "-t", "Chief follow-up"))
			},
			external: true,
		},
		{
			name: "another session's unready task is not produced",
			produce: func(t *testing.T, r *runner, projectID, _ string) string {
				var id string
				asSession(t, r, "groom-other", func() {
					id = firstField(t, r.run("task", "create", "-p", projectID, "-t", "Builder follow-up"))
				})
				return id
			},
			external: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			projectID, cfgPath := tasklessContext(t, r)
			r.run("actor", "create", "builder", "--kind", "agent")
			scope := agentReadyTask(t, r, projectID, "Add widget")

			promptPath := filepath.Join(t.TempDir(), "prompt.md")
			mustWrite(t, promptPath, "# Grooming session prompt\n")

			var child string
			base := isofake.New("sandbox")
			base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
			backend := groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
				prompt := cmd.Argv[len(cmd.Argv)-1]
				session := cmd.Env[harnesspkg.EnvSession]
				if session == "" {
					t.Fatalf("the run injected no session id; env = %v", cmd.Env)
				}
				ackSession(t, r, session, projectID)
				child = tt.produce(t, r, projectID, session)
				docs := defaultGroomDocs()
				docs.report = report
				if tt.deferred != nil {
					docs.deferred = tt.deferred(child)
				}
				stageGroomOutputs(t, base, prompt, docs)
			}}
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			out, err := runGroomCapture(r, "--config", cfgPath, "groom", "-p", projectID, "--unattended",
				"--prompt-file", promptPath, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir(), scope)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("groom error = %v\n%s", err, out)
				}
				if tt.external {
					if got := r.run("task", "get", child); !strings.Contains(got, "(todo)") {
						t.Fatalf("the concurrent writer's task %s is not the unready task the case means to exclude:\n%s", child, got)
					}
				}
				return
			}
			if err == nil {
				t.Fatalf("unattended session left produced child %s unready and still completed:\n%s", child, out)
			}
			if !strings.Contains(err.Error(), "neither agent-ready nor deferred") || !strings.Contains(err.Error(), child) {
				t.Fatalf("error = %v, want it to name the unready child %s", err, child)
			}
		})
	}
}

// asSession runs fn with session attributed to every ft call it makes, standing
// in for the child ft a launched session runs: the fake backend hands back the
// FACTOTUM_SESSION the run injected, so the test's creates carry the same event
// attribution the real child's would. Every other environment key keeps the
// runner's own getenv, so the parent's hermetic setup is unchanged.
func asSession(t *testing.T, r *runner, session string, fn func()) {
	t.Helper()
	prev := r.getenv
	r.getenv = func(key string) string {
		if key == harnesspkg.EnvSession {
			return session
		}
		if prev != nil {
			return prev(key)
		}
		return ""
	}
	defer func() { r.getenv = prev }()
	fn()
}

// ackSession stands in for the child's `ft session ack`: it acknowledges the
// session the run injected, as the launched session must.
func ackSession(t *testing.T, r *runner, session, projectID string) {
	t.Helper()
	asSession(t, r, session, func() {
		r.run("session", "ack", "-p", projectID)
	})
}

// TestGroomUnattendedRequiresSessionAck pins the guard's fail-closed anchor: an
// unattended run refuses to complete unless the session acknowledged the id the
// run injected. Without the acknowledgement an empty produced set is
// indistinguishable from a child ft that never carried the id, so the run would
// pass silently over work it cannot see. A session that names the id without
// carrying it in its environment does not count.
func TestGroomUnattendedRequiresSessionAck(t *testing.T) {
	const report = "# Grooming report\n\n## Summary\n\n## Per item\n\n## Product questions (grill)\n\n## Deferred (for stakeholders)\n\n## DAG changes\n"
	tests := []struct {
		name string
		// ack runs in the session; nil leaves the session unacknowledged.
		ack     func(t *testing.T, r *runner, session, projectID string)
		wantErr bool
	}{
		{
			name: "the session acknowledges its id",
			ack:  ackSession,
		},
		{
			name:    "a session that never acknowledges its id fails",
			wantErr: true,
		},
		{
			name: "an acknowledgement under another session does not count",
			ack: func(t *testing.T, r *runner, _, projectID string) {
				ackSession(t, r, "groom-other", projectID)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRunner(t)
			projectID, cfgPath := tasklessContext(t, r)
			r.run("actor", "create", "builder", "--kind", "agent")
			scope := agentReadyTask(t, r, projectID, "Add widget")

			promptPath := filepath.Join(t.TempDir(), "prompt.md")
			mustWrite(t, promptPath, "# Grooming session prompt\n")

			base := isofake.New("sandbox")
			base.Program(isolation.ExecResult{Stdout: []byte("groomed\n"), ExitCode: 0})
			backend := groomBackend{Backend: base, onExec: func(cmd isolation.Command) {
				prompt := cmd.Argv[len(cmd.Argv)-1]
				session := cmd.Env[harnesspkg.EnvSession]
				if session == "" {
					t.Fatalf("the run injected no session id; env = %v", cmd.Env)
				}
				if tt.ack != nil {
					tt.ack(t, r, session, projectID)
				}
				docs := defaultGroomDocs()
				docs.report = report
				stageGroomOutputs(t, base, prompt, docs)
			}}
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")

			out, err := runGroomCapture(r, "--config", cfgPath, "groom", "-p", projectID, "--unattended",
				"--prompt-file", promptPath, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir(), scope)
			if !tt.wantErr {
				if err != nil {
					t.Fatalf("groom error = %v\n%s", err, out)
				}
				return
			}
			if err == nil {
				t.Fatalf("an unattended run passed without the session acknowledging its id:\n%s", out)
			}
			if !strings.Contains(err.Error(), "acknowledged") || !strings.Contains(err.Error(), harnesspkg.EnvSession) {
				t.Fatalf("error = %v, want it to name the missing acknowledgement and %s", err, harnesspkg.EnvSession)
			}
		})
	}
}

// TestDeferredIDsWholeTokenMatch pins the anchored deferred detection: an id in
// the deferred-questions file counts only as its own token, so a longer id that
// shares a prefix cannot excuse an unresolved item.
func TestDeferredIDsWholeTokenMatch(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []core.TicketID
		// notWant are ids the body must not be read as naming.
		notWant []core.TicketID
	}{
		{
			name: "the template item field",
			body: "# Deferred questions\n\n## Questions\n\n- [ ] Which cache?\n  - item: t-abc123\n  - owner: PO\n\n## Resolved\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "an id inline in prose",
			body: "# Deferred questions\n\n## Questions\n\n- Which cache? (item: t-abc123, owner: PO)\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name:    "a longer id sharing the prefix does not match",
			body:    "# Deferred questions\n\n## Questions\n\n  - item: t-abc1234\n",
			want:    []core.TicketID{"t-abc1234"},
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name: "a longer and an exact id on separate lines",
			body: "# Deferred questions\n\n## Questions\n\n  - item: t-abc1234\n  - item: t-abc123\n",
			want: []core.TicketID{"t-abc1234", "t-abc123"},
		},
		{
			name:    "a suffixed token does not match",
			body:    "# Deferred questions\n\n## Questions\n\n  - item: t-abc123_x\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name: "several ids",
			body: "## Questions\n\n- item: t-aaa111\n- item: t-bbb222\n",
			want: []core.TicketID{"t-aaa111", "t-bbb222"},
		},
		{
			name:    "an id in a fenced code block does not count",
			body:    "## Questions\n\n```sh\nft task get t-abc123\n```\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an id in a tilde fenced block does not count",
			body:    "## Questions\n\n~~~sh\nft task get t-abc123\n~~~\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an id in an indented code block does not count",
			body:    "## Questions\n\n    ft task get t-abc123\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an id in a tab-indented code block does not count",
			body:    "## Questions\n\n\tft task get t-abc123\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an indented code block does not swallow a prose deferral",
			body:    "## Questions\n\n- Which cache? (item: t-abc123, owner: PO)\n\n      ft task get t-def456\n",
			want:    []core.TicketID{"t-abc123"},
			notWant: []core.TicketID{"t-def456"},
		},
		{
			name: "an indented code block does not open a section",
			body: "## Questions\n\n    ## Resolved\n\n- item: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a 4-space prose continuation still defers",
			body: "## Questions\n\n- [ ] Which cache?\n    The cache is undecided; item: t-abc123, owner: PO\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a 4-space nested list item still defers",
			body: "## Questions\n\n- [ ] Which cache?\n    - item: t-abc123\n    - owner: PO\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a 4-space nested list item after a blank line still defers",
			body: "## Questions\n\n- [ ] Which cache?\n\n    - item: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a tab-indented prose continuation still defers",
			body: "## Questions\n\n- [ ] Which cache?\n\titem: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an indented code block after a heading needs no blank line",
			body:    "## Questions\n    ft task get t-abc123\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "a list marker inside an indented code block stays code",
			body:    "## Questions\n\n    SELECT *\n    - item: t-abc123\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name: "a blank-line 4-space continuation under a bullet parent still defers",
			body: "## Questions\n\n- [ ] Which cache?\n\n    item: t-abc123\n    owner: PO\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a blank-line 4-space continuation under an ordered parent still defers",
			body: "## Questions\n\n1. Which cache?\n\n    item: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a blank-line 6-space continuation of a nested item still defers",
			body: "## Questions\n\n- [ ] Which cache?\n  - b\n\n      item: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name:    "a 6-space code block under a bullet parent does not count",
			body:    "## Questions\n\n- [ ] Which cache?\n\n      ft task get t-abc123\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an indented list marker with no enclosing list is code",
			body:    "## Questions\n\n    - item: t-abc123\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name: "a lazy continuation before the blank line keeps the list open",
			body: "## Questions\n\n- [ ] Which cache?\nlazy prose\n\n    item: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a tab in the marker padding counts as columns",
			body: "## Questions\n\n- \t[ ] Which cache?\n\n       item: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name: "a tab in ordered marker padding counts as columns",
			body: "## Questions\n\n1.\tWhich cache?\n\n       item: t-abc123\n",
			want: []core.TicketID{"t-abc123"},
		},
		{
			name:    "a fenced block after a list closes it, so a later sample is code",
			body:    "## Questions\n\n- [ ] Which cache?\n\n```\nft task get t-abc123\n```\n\n    ft task get t-def456\n",
			notWant: []core.TicketID{"t-abc123", "t-def456"},
		},
		{
			name:    "an id in an HTML comment does not count",
			body:    "## Questions\n\n<!-- see t-abc123 -->\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an id under Resolved does not count",
			body:    "## Questions\n\n## Resolved\n\n- [x] Dropped - item: t-abc123 - resolved as duplicate\n",
			notWant: []core.TicketID{"t-abc123"},
		},
		{
			name:    "an id under Questions counts with a Resolved section after it",
			body:    "## Questions\n\n- [ ] Which cache?\n  - item: t-abc123\n\n## Resolved\n\n- [x] Old - item: t-def456\n",
			want:    []core.TicketID{"t-abc123"},
			notWant: []core.TicketID{"t-def456"},
		},
		{
			name:    "an empty body",
			body:    "",
			notWant: []core.TicketID{"t-abc123"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := deferredIDs(tt.body)
			for _, id := range tt.want {
				if !got[id] {
					t.Errorf("deferredIDs(%q) is missing %s", tt.body, id)
				}
			}
			for _, id := range tt.notWant {
				if got[id] {
					t.Errorf("deferredIDs(%q) wrongly matched %s", tt.body, id)
				}
			}
		})
	}
}
