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
		projectID, taskID := runContext(t, r)
		mustWrite(t, r.projectPath, "project = \""+projectID+"\"\n")
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
		if asked := strings.Join(p.asked, "\n"); !strings.Contains(asked, "project/user") {
			t.Fatalf("a configured project must still offer both scopes:\n%s", asked)
		}
		got := readFile(t, r.userPath)
		for _, want := range []string{"[run]", `sandbox = "fake"`, `harness = "fake"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("persisted user config missing %q:\n%s", want, got)
			}
		}
		if got := readFile(t, r.projectPath); strings.Contains(got, "[run]") {
			t.Fatalf("the user scope wrote the project config:\n%s", got)
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
		p := &scriptedPrompter{t: t, inputs: []string{"fake", "fake", "project"}}
		r.prompt = p

		out := r.run("run", taskID, "--workspace", t.TempDir())
		if !strings.Contains(out, "run: finished") {
			t.Fatalf("prompted project run did not finish:\n%s", out)
		}
		if asked := strings.Join(p.asked, "\n"); !strings.Contains(asked, "project/user") {
			t.Fatalf("a configured project must still offer both scopes:\n%s", asked)
		}
		got := readFile(t, r.projectPath)
		for _, want := range []string{`project = "` + projectID + `"`, "[run]", `sandbox = "fake"`, `harness = "fake"`} {
			if !strings.Contains(got, want) {
				t.Fatalf("persisted project config missing %q:\n%s", want, got)
			}
		}
	})
}

// TestRunPromptOmitsProjectScopeWithoutProject pins that the prompt never
// offers a scope that cannot succeed: with no configured project a project-
// scope write is refused, so the pick lands in the machine config and the run
// says why project scope is unavailable instead of asking and then failing.
func TestRunPromptOmitsProjectScopeWithoutProject(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)
	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")
	r.isTerminal = terminal
	// Only the two dimensions are answered: an unexpected scope prompt would
	// fail the scripted prompter, so this also proves the prompt is skipped.
	p := &scriptedPrompter{t: t, inputs: []string{"fake", "fake"}}
	r.prompt = p

	stdout, stderr := r.runSplit("run", taskID, "--workspace", t.TempDir())
	if !strings.Contains(stdout, "run: finished") {
		t.Fatalf("prompted run did not finish:\n%s", stdout)
	}
	if asked := strings.Join(p.asked, "\n"); strings.Contains(asked, "save run defaults to") {
		t.Fatalf("the scope prompt asked with no project to save to:\n%s", asked)
	}
	for _, want := range []string{"project scope", "machine config"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr does not say why project scope is unavailable (%q):\n%s", want, stderr)
		}
	}
	got := readFile(t, r.userPath)
	for _, want := range []string{"[run]", `sandbox = "fake"`, `harness = "fake"`} {
		if !strings.Contains(got, want) {
			t.Fatalf("persisted machine config missing %q:\n%s", want, got)
		}
	}
	if _, err := os.Stat(r.projectPath); !os.IsNotExist(err) {
		t.Fatalf("a no-project pick wrote the project config (err=%v)", err)
	}
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

// TestGroomSelectionNeedsTerminalStdin pins that `ft groom` inherits the run
// selection guard: a terminal stdout with a redirected stdin is non-interactive,
// so an unset sandbox/harness errors clearly instead of reading EOF at the
// prompt and saving defaults nobody chose.
func TestGroomSelectionNeedsTerminalStdin(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n")
	r.isTerminal = terminal
	r.stdinTerminal = func(io.Reader) bool { return false }

	err := r.runErr("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath, "--workspace", t.TempDir())
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("groom error = %v, want a usage error", err)
	}
	if !strings.Contains(err.Error(), "run.sandbox") {
		t.Fatalf("groom error does not name run.sandbox as the way out: %v", err)
	}
	if _, statErr := os.Stat(r.userPath); !os.IsNotExist(statErr) {
		t.Fatalf("a non-interactive groom wrote the user config: %v", statErr)
	}
	if got := readFile(t, cfgPath); strings.Contains(got, "[run]") {
		t.Fatalf("a non-interactive groom wrote run defaults:\n%s", got)
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

// TestResolveRunSelectionNeedsTerminalStdin pins that the selection prompt
// needs a terminal on stdin as well as stdout. Prompts that read a redirected
// stdin hit EOF, accept every default, and persist a choice nobody made, so a
// run with stdout attached but stdin not is non-interactive: it fails with the
// same clear error and writes nothing. Only a real terminal asks once and saves.
func TestResolveRunSelectionNeedsTerminalStdin(t *testing.T) {
	tests := []struct {
		name           string
		stdoutTerminal bool
		stdinTerminal  bool
		wantErr        bool
	}{
		{"terminal stdout with redirected stdin errors", true, false, true},
		{"piped stdout errors", false, true, true},
		{"both terminals prompt and save", true, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// No injected prompter: the terminal seam must be what decides,
			// which is the combination the bug slipped through.
			deps := newOptInDeps(t, nil)
			deps.IsTerminal = func(io.Writer) bool { return tc.stdoutTerminal }
			deps.IsTerminalReader = func(io.Reader) bool { return tc.stdinTerminal }

			cmd := &cobra.Command{Use: "run"}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetIn(strings.NewReader("docker\nopencode\nuser\n"))
			cfg := config.Run{}
			err := deps.resolveRunSelection(cmd, &cfg)
			if tc.wantErr {
				if !errors.Is(err, ErrUsage) {
					t.Fatalf("resolveRunSelection() error = %v, want a usage error", err)
				}
				if !strings.Contains(err.Error(), "run.sandbox") {
					t.Fatalf("error does not name run.sandbox as the way out: %v", err)
				}
				for _, path := range []string{deps.UserConfigPath, deps.ProjectConfigPath} {
					if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
						t.Fatalf("a non-interactive selection wrote %s: %v", path, statErr)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveRunSelection() error = %v", err)
			}
			if cfg.Sandbox != "docker" || cfg.Harness != "opencode" {
				t.Fatalf("resolved = %q/%q, want docker/opencode", cfg.Sandbox, cfg.Harness)
			}
			user := readFile(t, deps.UserConfigPath)
			for _, want := range []string{`sandbox = "docker"`, `harness = "opencode"`} {
				if !strings.Contains(user, want) {
					t.Fatalf("persisted user config missing %q:\n%s", want, user)
				}
			}

			// The saved pick resolves the next run with no prompt and no flag,
			// even from a redirected stdin.
			loaded, err := config.Load(config.Input{
				UserPath:    deps.UserConfigPath,
				ProjectPath: deps.ProjectConfigPath,
				Getenv:      func(string) string { return "" },
			})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			deps.IsTerminalReader = func(io.Reader) bool { return false }
			if err := deps.resolveRunSelection(cmd, &loaded.Run); err != nil {
				t.Fatalf("second run did not resolve the persisted pick: %v", err)
			}
		})
	}
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

// TestResolveRunSelectionOptInForConfiguredLocal pins that the allow_host
// opt-in belongs to the resolved sandbox, not to choosing it: local pinned in
// config (or passed as a flag) is just as unsandboxed as local chosen at the
// prompt, so an unset allow_host is offered either way. With both dimensions
// already resolved only the opt-in is asked, and it is written to the
// host-scoped user config without a scope prompt. An opted-in local run asks
// nothing, and declining aborts without writing anything.
func TestResolveRunSelectionOptInForConfiguredLocal(t *testing.T) {
	tests := []struct {
		name        string
		sandbox     string
		harness     string
		allowHost   bool
		inputs      []string
		confirms    []bool
		wantConfirm bool
		wantInputs  bool
		wantErr     bool
	}{
		{
			name:        "configured local with an unset harness opts in",
			sandbox:     "local",
			inputs:      []string{"opencode", "user"},
			confirms:    []bool{true},
			wantConfirm: true,
			wantInputs:  true,
		},
		{
			name:        "configured local with a configured harness still opts in",
			sandbox:     "local",
			harness:     "opencode",
			confirms:    []bool{true},
			wantConfirm: true,
		},
		{
			name:        "configured local declined aborts without saving",
			sandbox:     "local",
			harness:     "opencode",
			confirms:    []bool{false},
			wantConfirm: true,
			wantErr:     true,
		},
		{
			name:        "opted-in local asks nothing",
			sandbox:     "local",
			harness:     "opencode",
			allowHost:   true,
			wantConfirm: false,
		},
		{
			name:        "configured non-local asks nothing",
			sandbox:     "docker",
			harness:     "opencode",
			wantConfirm: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := &scriptedPrompter{t: t, inputs: tc.inputs, confirms: tc.confirms}
			deps := newOptInDeps(t, p)

			cmd := &cobra.Command{Use: "run"}
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cfg := config.Run{Sandbox: tc.sandbox, Harness: tc.harness, AllowHost: tc.allowHost}
			err := deps.resolveRunSelection(cmd, &cfg)
			asked := strings.Join(p.asked, "\n")
			if got := strings.Contains(asked, "confirm:"); got != tc.wantConfirm {
				t.Fatalf("opt-in prompt asked = %v, want %v (asked=%v)", got, tc.wantConfirm, p.asked)
			}
			if got := strings.Contains(asked, "input:"); got != tc.wantInputs {
				t.Fatalf("dimension/scope prompt asked = %v, want %v (asked=%v)", got, tc.wantInputs, p.asked)
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
			wantHarness := tc.harness
			if wantHarness == "" {
				wantHarness = "opencode"
			}
			if cfg.Sandbox != tc.sandbox || cfg.Harness != wantHarness {
				t.Fatalf("resolved = %q/%q, want %q/%q", cfg.Sandbox, cfg.Harness, tc.sandbox, wantHarness)
			}
			wantAllowHost := tc.allowHost || (tc.sandbox == "local" && tc.wantConfirm)
			if cfg.AllowHost != wantAllowHost {
				t.Fatalf("cfg.AllowHost = %v, want %v", cfg.AllowHost, wantAllowHost)
			}
			if !wantAllowHost || tc.allowHost {
				// Nothing was asked, so nothing should be written: an already
				// recorded opt-in and a non-local sandbox both save nothing.
				for _, path := range []string{deps.UserConfigPath, deps.ProjectConfigPath} {
					if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
						t.Fatalf("a run with nothing to save wrote %s: %v", path, statErr)
					}
				}
				return
			}
			user := readFile(t, deps.UserConfigPath)
			if !strings.Contains(user, "allow_host = true") {
				t.Fatalf("user config missing the recorded opt-in:\n%s", user)
			}
			// A dimension that came from config is already saved: the opt-in
			// path must not restate it.
			if strings.Contains(user, `sandbox = `) {
				t.Fatalf("the opt-in path persisted the already-configured sandbox:\n%s", user)
			}
			if tc.harness != "" && strings.Contains(user, `harness = `) {
				t.Fatalf("the opt-in path persisted the already-configured harness:\n%s", user)
			}
			loaded, err := config.Load(config.Input{
				UserPath:    deps.UserConfigPath,
				ProjectPath: deps.ProjectConfigPath,
				Getenv:      func(string) string { return "" },
			})
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if !loaded.Run.AllowHost {
				t.Fatalf("reloaded Run.AllowHost = false, want the recorded opt-in")
			}
		})
	}
}

// TestResolveRunSelectionNonInteractiveLocalOptIn pins that a non-interactive
// run never blocks on the opt-in: with both dimensions resolved, it leaves the
// decision to the run's own not-opted-in error instead of prompting.
func TestResolveRunSelectionNonInteractiveLocalOptIn(t *testing.T) {
	deps := newOptInDeps(t, nil)
	deps.Prompt = nil
	deps.IsTerminal = func(io.Writer) bool { return false }

	cmd := &cobra.Command{Use: "run"}
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cfg := config.Run{Sandbox: "local", Harness: "opencode"}
	if err := deps.resolveRunSelection(cmd, &cfg); err != nil {
		t.Fatalf("resolveRunSelection() error = %v, want the selection error to surface later", err)
	}
	if cfg.AllowHost {
		t.Fatal("a non-interactive run granted the host opt-in")
	}
	for _, path := range []string{deps.UserConfigPath, deps.ProjectConfigPath} {
		if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
			t.Fatalf("a non-interactive run wrote %s: %v", path, statErr)
		}
	}
}

// TestPersistRunDefaultsResolvesUserConfigFirst pins that a project-scope write
// with the host-scoped opt-in resolves the machine config path before writing
// the project file: a resolution failure (no $HOME, no --user-config) must
// leave no partial write behind.
func TestPersistRunDefaultsResolvesUserConfigFirst(t *testing.T) {
	deps := newOptInDeps(t, &scriptedPrompter{t: t})
	deps.UserConfigPath = ""

	path, optInPath, err := deps.persistRunDefaults("project", config.RunDefaults{Sandbox: "local", AllowHost: true})
	if err == nil {
		t.Fatalf("persistRunDefaults() error = nil, want a user-config resolution failure")
	}
	if path != "" || optInPath != "" {
		t.Fatalf("failed write reported paths %q/%q, want none", path, optInPath)
	}
	if _, statErr := os.Stat(deps.ProjectConfigPath); !os.IsNotExist(statErr) {
		t.Fatalf("the project config was written before the user config resolved: %v", statErr)
	}
}

// TestPersistRunDefaultsProjectScopeNeedsProject pins the refusal a requested
// project scope hits when no project is configured: it fails clearly, names the
// machine (user) config as the way out, and writes nothing, rather than leaving
// a [run] table in a project config with no project pin. The interactive prompt
// no longer offers that scope, but the guard still covers any caller that asks
// for it directly. It must not quietly fall back to the user config either: the
// caller asked for the project scope.
func TestPersistRunDefaultsProjectScopeNeedsProject(t *testing.T) {
	deps := newOptInDeps(t, &scriptedPrompter{t: t})
	deps.Config.Project = ""

	path, optInPath, err := deps.persistRunDefaults("project", config.RunDefaults{Sandbox: "docker", Harness: "opencode"})
	if err == nil {
		t.Fatalf("persistRunDefaults() error = nil, want a refusal")
	}
	if !strings.Contains(err.Error(), "user config") {
		t.Fatalf("error does not point at the user config as the way out: %v", err)
	}
	if path != "" || optInPath != "" {
		t.Fatalf("refused write reported paths %q/%q, want none", path, optInPath)
	}
	for _, p := range []string{deps.ProjectConfigPath, deps.UserConfigPath} {
		if _, statErr := os.Stat(p); !os.IsNotExist(statErr) {
			t.Fatalf("a refused project-scope write wrote %s: %v", p, statErr)
		}
	}
}
