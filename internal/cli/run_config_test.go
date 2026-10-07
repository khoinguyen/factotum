package cli

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestRunResolvesSelectionFromConfig pins the resolution order for the harness
// and sandbox: flag > project config > user config, with a non-interactive
// unset selection failing clearly. The fake adapters are registered under
// "fake"; a "nope" name is unregistered, so a run that succeeds proves the
// expected source won.
func TestRunResolvesSelectionFromConfig(t *testing.T) {
	tests := []struct {
		name            string
		userRun         string
		projectRun      string
		flags           []string
		wantErr         bool
		wantErrContains string
	}{
		{
			name:       "flag overrides project and user",
			userRun:    "[run]\nsandbox = \"nope-user\"\nharness = \"nope-user\"\n",
			projectRun: "[run]\nsandbox = \"nope-project\"\nharness = \"nope-project\"\n",
			flags:      []string{"--sandbox", "fake", "--harness", "fake"},
		},
		{
			name:       "project overrides user",
			userRun:    "[run]\nsandbox = \"nope-user\"\nharness = \"nope-user\"\n",
			projectRun: "[run]\nsandbox = \"fake\"\nharness = \"fake\"\n",
		},
		{
			name:    "user when project sets none",
			userRun: "[run]\nsandbox = \"fake\"\nharness = \"fake\"\n",
		},
		{
			name:            "non-interactive unset errors",
			wantErr:         true,
			wantErrContains: "--sandbox is required",
		},
		{
			name:            "unknown flag name errors",
			flags:           []string{"--sandbox", "nope", "--harness", "fake"},
			wantErr:         true,
			wantErrContains: "unknown isolation backend",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID, taskID := runContext(t, r)
			backend := isofake.New("sandbox")
			backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
			r.runBackend = backend
			r.runHarness = harnessfake.New("opencode")
			if tc.userRun != "" {
				mustWrite(t, r.userPath, tc.userRun)
			}
			if tc.projectRun != "" {
				mustWrite(t, r.projectPath, "project = \""+projectID+"\"\n"+tc.projectRun)
			}

			args := append([]string{"run", taskID, "--workspace", t.TempDir()}, tc.flags...)
			err := r.runErr(args...)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("run error = nil, want an error")
				}
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("run error = %v, want a usage error", err)
				}
				if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
					t.Fatalf("run error = %v, want it to contain %q", err, tc.wantErrContains)
				}
				if tc.wantErrContains == "--sandbox is required" && !strings.Contains(err.Error(), "fake") {
					t.Fatalf("run error should list the available backends: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("run error = %v, want nil", err)
			}
			if got := backend.Prepared(); len(got) == 0 {
				t.Fatal("the selected backend never ran")
			}
		})
	}
}

// TestRunPromptPersistsSelection pins the interactive one-time prompt: an unset
// harness and sandbox are chosen from the detected options, persisted to the
// scope the user picks, and the next run needs no flag or prompt.
func TestRunPromptPersistsSelection(t *testing.T) {
	t.Run("user scope", func(t *testing.T) {
		r := newRunner(t)
		_, taskID := runContext(t, r)
		backend := isofake.New("sandbox")
		backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
		r.runBackend = backend
		r.runHarness = harnessfake.New("opencode")
		r.isTerminal = terminal
		p := &scriptedPrompter{t: t, inputs: []string{"fake", "fake", "user"}}
		r.prompt = p

		out := r.run("run", taskID, "--workspace", t.TempDir())
		if !strings.Contains(out, "run: finished") {
			t.Fatalf("prompted run did not finish:\n%s", out)
		}
		if asked := strings.Join(p.asked, "\n"); !strings.Contains(asked, "fake") {
			t.Fatalf("prompt did not offer the detected options:\n%s", asked)
		}
		got := readFile(t, r.userPath)
		for _, want := range []string{"[run]", `sandbox = "fake"`, `harness = "fake"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("persisted user config missing %q:\n%s", want, got)
			}
		}

		// The persisted pick resolves the next run with no flag and no prompt.
		r.prompt = nil
		out = r.run("run", taskID, "--workspace", t.TempDir())
		if !strings.Contains(out, "run: finished") {
			t.Fatalf("second run did not resolve the persisted pick:\n%s", out)
		}
	})

	t.Run("project scope", func(t *testing.T) {
		r := newRunner(t)
		projectID, taskID := runContext(t, r)
		mustWrite(t, r.projectPath, "project = \""+projectID+"\"\n")
		backend := isofake.New("sandbox")
		backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
		r.runBackend = backend
		r.runHarness = harnessfake.New("opencode")
		r.isTerminal = terminal
		r.prompt = &scriptedPrompter{t: t, inputs: []string{"fake", "fake", "project"}}

		out := r.run("run", taskID, "--workspace", t.TempDir())
		if !strings.Contains(out, "run: finished") {
			t.Fatalf("prompted project run did not finish:\n%s", out)
		}
		got := readFile(t, r.projectPath)
		for _, want := range []string{`project = "` + projectID + `"`, "[run]", `sandbox = "fake"`, `harness = "fake"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("persisted project config missing %q:\n%s", want, got)
			}
		}
	})
}

// TestRunPromptOnlyPromptsForMissingDimension pins that a configured dimension
// is not re-asked: with the sandbox in config, only the harness is prompted.
func TestRunPromptOnlyPromptsForMissingDimension(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	mustWrite(t, r.userPath, "[run]\nsandbox = \"fake\"\n")
	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")
	r.isTerminal = terminal
	p := &scriptedPrompter{t: t, inputs: []string{"fake", "user"}}
	r.prompt = p

	if out := r.run("run", taskID, "--workspace", t.TempDir()); !strings.Contains(out, "run: finished") {
		t.Fatalf("run did not finish:\n%s", out)
	}
	asked := strings.Join(p.asked, "\n")
	if strings.Contains(asked, "sandbox") {
		t.Fatalf("a configured sandbox must not be prompted for:\n%s", asked)
	}
	if !strings.Contains(asked, "harness") {
		t.Fatalf("the missing harness was not prompted for:\n%s", asked)
	}
	if got := readFile(t, r.userPath); !strings.Contains(got, `harness = "fake"`) {
		t.Fatalf("prompted harness not persisted:\n%s", got)
	}
}

// TestGroomResolvesSelectionFromConfig pins that `ft groom` shares the run
// resolution, so its harness/sandbox are optional too.
func TestGroomResolvesSelectionFromConfig(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")
	mustWrite(t, r.userPath, "[run]\nsandbox = \"fake\"\nharness = \"fake\"\n")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath, "--workspace", t.TempDir())
	if !strings.Contains(out, "run: finished") {
		t.Fatalf("groom without flags did not resolve config:\n%s", out)
	}
}
