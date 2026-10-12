package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

// TestUpCommandSurface pins the daemon's flag surface: the flags the wiring
// reads are registered, and the daemon binds loopback by default.
func TestUpCommandSurface(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	cmd := newUpCommand(deps)

	if _, ok := builtinCommands().Lookup("up"); !ok {
		t.Fatal("up is not registered")
	}
	for _, name := range []string{"bind", "project", "unattended", "no-auto-groom"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("up flag --%s is missing", name)
		}
	}
	if bind := cmd.Flags().Lookup("bind").DefValue; !strings.HasPrefix(bind, "127.0.0.1") {
		t.Fatalf("default bind = %q, want a loopback address", bind)
	}
}

// TestUpWiringProjectResolution pins the -p/config project resolution the
// daemon uses: the -p flag wins, else the configured project, else empty (which
// requireProject rejects).
func TestUpWiringProjectResolution(t *testing.T) {
	r := newRunner(t)
	configured := firstField(t, r.run("project", "create", "Acme"))
	other := firstField(t, r.run("project", "create", "Beta"))
	writeUpProjectConfig(t, r.projectPath, "project = \""+configured+"\"\n")

	cases := []struct {
		name string
		args []string
		want string
	}{
		{"flag wins", []string{"-p", other}, other},
		{"config default", nil, configured},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := append([]string{"up", "--bind", "127.0.0.1:0"}, tc.args...)
			settings, _, err := runUpWiring(t, r, nil, args...)
			if err != nil {
				t.Fatalf("ft up error = %v", err)
			}
			if string(settings.project) != tc.want {
				t.Fatalf("resolved project = %q, want %q", settings.project, tc.want)
			}
		})
	}
}

func TestUpWiringRequiresProject(t *testing.T) {
	r := newRunner(t)
	settings, _, err := runUpWiring(t, r, nil, "up", "--bind", "127.0.0.1:0")
	if settings.project != "" {
		t.Fatalf("resolved project = %q, want empty with no -p and no default", settings.project)
	}
	if err == nil || !strings.Contains(err.Error(), "--project is required") {
		t.Fatalf("ft up with no project = %v, want a usage error naming --project", err)
	}
}

// TestUpWiringUnattendedAndAutoGroom pins the flag/config precedence the daemon
// resolves: --unattended overrides up.unattended only when given (so the config
// default survives an omitted flag), and --no-auto-groom always forces
// auto-groom off. interactive is a terminal session that is not unattended.
func TestUpWiringUnattendedAndAutoGroom(t *testing.T) {
	cases := []struct {
		name            string
		config          string
		args            []string
		wantAutoGroom   bool
		wantInteractive bool
	}{
		{
			name:            "defaults: unattended on, auto-groom on",
			config:          "",
			args:            nil,
			wantAutoGroom:   true,
			wantInteractive: false,
		},
		{
			name:            "config unattended off attaches a PO on a terminal",
			config:          "[up]\nunattended = false\n",
			args:            nil,
			wantAutoGroom:   true,
			wantInteractive: true,
		},
		{
			name:            "--unattended overrides config off",
			config:          "[up]\nunattended = false\n",
			args:            []string{"--unattended"},
			wantAutoGroom:   true,
			wantInteractive: false,
		},
		{
			name:            "--unattended=false overrides config on",
			config:          "[up]\nunattended = true\n",
			args:            []string{"--unattended=false"},
			wantAutoGroom:   true,
			wantInteractive: true,
		},
		{
			name:            "--no-auto-groom forces auto-groom off",
			config:          "[up]\nauto_groom = true\n",
			args:            []string{"--no-auto-groom"},
			wantAutoGroom:   false,
			wantInteractive: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			writeUpProjectConfig(t, r.projectPath, "project = \""+projectID+"\"\n"+tc.config)

			terminal := func(d *Deps) {
				d.IsTerminal = func(io.Writer) bool { return true }
				d.IsTerminalReader = func(io.Reader) bool { return true }
			}
			args := append([]string{"up", "--bind", "127.0.0.1:0"}, tc.args...)
			settings, _, err := runUpWiring(t, r, terminal, args...)
			if err != nil {
				t.Fatalf("ft up error = %v", err)
			}
			if settings.autoGroom != tc.wantAutoGroom {
				t.Fatalf("autoGroom = %v, want %v", settings.autoGroom, tc.wantAutoGroom)
			}
			if settings.interactive != tc.wantInteractive {
				t.Fatalf("interactive = %v, want %v", settings.interactive, tc.wantInteractive)
			}
		})
	}
}

// TestUpWiringInteractiveNeedsBothTerminals pins runUp's tty-detection branch:
// even when the config asks for a PO (up.unattended=false), the daemon attaches
// one only on a full interactive session (stdout and stdin both terminals).
// A daemon started headless, or with stdin piped, falls back to unattended and
// defers product questions. This is the tty half of runInteractive's wiring,
// which TestUpWiringUnattendedAndAutoGroom's always-terminal cases do not cover.
func TestUpWiringInteractiveNeedsBothTerminals(t *testing.T) {
	cases := []struct {
		name            string
		stdoutTerminal  bool
		stdinTerminal   bool
		wantInteractive bool
	}{
		{"redirected stdout stays unattended", false, true, false},
		{"piped stdin stays unattended", true, false, false},
		{"full terminal attaches a PO", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			projectID := firstField(t, r.run("project", "create", "Acme"))
			writeUpProjectConfig(t, r.projectPath,
				"project = \""+projectID+"\"\n[up]\nunattended = false\n")

			terminal := func(d *Deps) {
				d.IsTerminal = func(io.Writer) bool { return tc.stdoutTerminal }
				d.IsTerminalReader = func(io.Reader) bool { return tc.stdinTerminal }
			}
			settings, _, err := runUpWiring(t, r, terminal, "up", "--bind", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("ft up error = %v", err)
			}
			if settings.interactive != tc.wantInteractive {
				t.Fatalf("interactive = %v, want %v", settings.interactive, tc.wantInteractive)
			}
		})
	}
}

// TestUpWiringStoreOverrideReachesDaemon proves the CLI store override wins over
// the project file: the file names an unopenable backend, so the daemon only
// starts because --store/--store-opt (from the runner's base args) overrode it
// and the daemon opened that same store.
func TestUpWiringStoreOverrideReachesDaemon(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	writeUpProjectConfig(t, r.projectPath,
		"project = \""+projectID+"\"\n[store]\nbackend = \"nope\"\n")

	settings, stderr, err := runUpWiring(t, r, nil, "up", "--bind", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ft up error = %v; stderr:\n%s", err, stderr)
	}
	if settings.project != core.ProjectID(projectID) {
		t.Fatalf("resolved project = %q, want %q", settings.project, projectID)
	}
	if !strings.Contains(stderr, "factotum factory: http://127.0.0.1:") {
		t.Fatalf("daemon did not serve on the overridden store; stderr:\n%s", stderr)
	}
}

// runUpWiring drives `ft up` through the real command tree far enough to resolve
// its wiring, capturing the resolved settings with the injected probe, then
// cancels the otherwise-blocking serve loop and waits for the command to return.
// customize, when set, adjusts the deps before the command runs (for example to
// simulate a terminal).
func runUpWiring(t *testing.T, r *runner, customize func(*Deps), args ...string) (upSettings, string, error) {
	t.Helper()
	var stdout, stderr syncBuffer
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, r.getenv)
	if customize != nil {
		customize(deps)
	}
	base := r.setup(deps)
	settingsCh := make(chan upSettings, 1)
	deps.upProbe = func(s upSettings) { settingsCh <- s }

	root := NewRoot(deps)
	root.SetArgs(append(append([]string{}, base...), args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	root.SetContext(ctx)

	done := make(chan error, 1)
	go func() { done <- root.Execute() }()

	var settings upSettings
	select {
	case settings = <-settingsCh:
	case <-time.After(5 * time.Second):
		t.Fatalf("ft up never resolved its wiring; stderr:\n%s", stderr.String())
	}
	cancel()
	select {
	case err := <-done:
		return settings, stderr.String(), err
	case <-time.After(5 * time.Second):
		t.Fatalf("ft up did not stop after cancel; stderr:\n%s", stderr.String())
		return settings, stderr.String(), nil
	}
}

// syncBuffer is a bytes.Buffer safe for the daemon goroutine to write while the
// test polls it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func writeUpProjectConfig(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write project config: %v", err)
	}
}
