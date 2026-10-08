package cli

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/registry"
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

// newOptInDeps builds a Deps whose run adapters are in-memory fakes, so a test
// can exercise the interactive selection without executing a real backend.
func newOptInDeps(t *testing.T, p Prompter) *Deps {
	t.Helper()
	dir := t.TempDir()
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, func(string) string { return "" })
	deps.Config.Project = "factotum"
	deps.UserConfigPath = filepath.Join(dir, "user-config.toml")
	deps.ProjectConfigPath = filepath.Join(dir, "project-config.toml")
	deps.Prompt = p
	deps.RunBackends = registry.New[IsolationBackendFactory]()
	for _, name := range []string{"local", "docker"} {
		name := name
		if err := deps.RunBackends.Register(name, func(config.Run, io.Writer) (isolation.IsolationBackend, error) {
			return isofake.New(name), nil
		}); err != nil {
			t.Fatalf("register backend %q: %v", name, err)
		}
	}
	deps.RunHarnesses = registry.New[HarnessFactory]()
	if err := deps.RunHarnesses.Register("opencode", func(config.Run) (harness.Harness, error) {
		return harnessfake.New("opencode"), nil
	}); err != nil {
		t.Fatalf("register harness: %v", err)
	}
	return deps
}

// TestResolveRunSelectionLocalOptIn pins the interactive local opt-in: choosing
// local asks explicitly (default no), and on yes records run.allow_host in the
// host-scoped user config while the committable pick lands in the chosen scope.
// Declining aborts without saving, and a non-local sandbox never asks.
func TestResolveRunSelectionLocalOptIn(t *testing.T) {
	tests := []struct {
		name          string
		scope         string
		backend       string
		hasConfirm    bool
		confirm       bool
		wantErr       bool
		wantAllowHost bool
	}{
		{"local accepted records opt-in", "user", "local", true, true, false, true},
		{"local declined aborts without saving", "user", "local", true, false, true, false},
		{"project scope still opts in via the user config", "project", "local", true, true, false, true},
		{"non-local skips the opt-in prompt", "user", "docker", false, false, false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedPrompter{t: t, inputs: []string{tc.backend, "opencode", tc.scope}}
			if tc.hasConfirm {
				p.confirms = []bool{tc.confirm}
			}
			deps := newOptInDeps(t, p)

			cmd := &cobra.Command{Use: "run"}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cfg := config.Run{}
			err := deps.resolveRunSelection(cmd, &cfg)
			asked := strings.Join(p.asked, "\n")
			if got := strings.Contains(asked, "confirm:"); got != tc.hasConfirm {
				t.Fatalf("opt-in prompt asked = %v, want %v (asked=%v)", got, tc.hasConfirm, p.asked)
			}
			if tc.wantErr {
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("declined opt-in error = %v, want a usage error", err)
				}
				for _, path := range []string{deps.UserConfigPath, deps.ProjectConfigPath} {
					if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
						t.Fatalf("declined opt-in wrote %s: %v", path, statErr)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRunSelection() error = %v", err)
			}
			if cfg.Sandbox != tc.backend || cfg.Harness != "opencode" {
				t.Fatalf("resolved = %q/%q, want %q/opencode", cfg.Sandbox, cfg.Harness, tc.backend)
			}
			if cfg.AllowHost != tc.wantAllowHost {
				t.Fatalf("cfg.AllowHost = %v, want %v", cfg.AllowHost, tc.wantAllowHost)
			}

			user := readFile(t, deps.UserConfigPath)
			if got := strings.Contains(user, `sandbox = "`+tc.backend+`"`); got != (tc.scope == "user") {
				t.Fatalf("user config sandbox presence = %v, want %v:\n%s", got, tc.scope == "user", user)
			}
			if got := strings.Contains(user, "allow_host = true"); got != tc.wantAllowHost {
				t.Fatalf("user config allow_host = %v, want %v:\n%s", got, tc.wantAllowHost, user)
			}
			if tc.scope == "project" {
				project := readFile(t, deps.ProjectConfigPath)
				if !strings.Contains(project, `sandbox = "`+tc.backend+`"`) {
					t.Fatalf("project config missing the sandbox pick:\n%s", project)
				}
				if strings.Contains(project, "allow_host") {
					t.Fatalf("allow_host leaked into the committed project config:\n%s", project)
				}
			}

			loaded, err := config.Load(config.Input{
				UserPath:    deps.UserConfigPath,
				ProjectPath: deps.ProjectConfigPath,
				Getenv:      func(string) string { return "" },
			})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if loaded.Run.Sandbox != tc.backend || loaded.Run.AllowHost != tc.wantAllowHost {
				t.Fatalf("reloaded Run = %+v, want sandbox %q allow_host %v", loaded.Run, tc.backend, tc.wantAllowHost)
			}
		})
	}
}
