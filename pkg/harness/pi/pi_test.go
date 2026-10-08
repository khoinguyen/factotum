package pi_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/harness/pi"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
)

func TestName(t *testing.T) {
	if got := pi.New(pi.Options{}).Name(); got != "pi" {
		t.Fatalf("Name() = %q, want pi", got)
	}
}

func TestSpecDeclaresImageAndIdentity(t *testing.T) {
	tests := []struct {
		name         string
		opts         pi.Options
		req          harness.Request
		wantImage    string
		wantUser     string
		wantCred     bool
		wantProvider string
		wantEnvVar   string
	}{
		{
			name:      "defaults target the host pi binary",
			wantImage: "",
			wantUser:  "",
		},
		{
			name:      "overrides win",
			opts:      pi.Options{Image: "ghcr.io/acme/pi:pinned", Binary: "/usr/local/bin/pi", User: "501:20"},
			req:       harness.Request{Workdir: "/work", Labels: map[string]string{"task": "t-1"}},
			wantImage: "ghcr.io/acme/pi:pinned",
			wantUser:  "501:20",
		},
		{
			name:         "provider credential is declared without a secret",
			opts:         pi.Options{Provider: "openrouter", CredentialEnvVar: "OPENROUTER_API_KEY"},
			wantImage:    "",
			wantCred:     true,
			wantProvider: "openrouter",
			wantEnvVar:   "OPENROUTER_API_KEY",
		},
		{
			name:      "partial credential config declares nothing",
			opts:      pi.Options{Provider: "openrouter"},
			wantImage: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := pi.New(tc.opts).Spec(tc.req)
			if err != nil {
				t.Fatalf("Spec() error = %v", err)
			}
			if spec.Image.Ref != tc.wantImage {
				t.Errorf("image = %q, want %q", spec.Image.Ref, tc.wantImage)
			}
			// The harness leaves the container entrypoint to the backend: it
			// names the workload binary in Command, and a backend that needs its
			// own init (the docker keep-alive) would otherwise have to refuse it.
			if len(spec.Image.Entrypoint) != 0 {
				t.Errorf("entrypoint = %v, want none (the backend owns the container init)", spec.Image.Entrypoint)
			}
			if spec.Image.User != tc.wantUser {
				t.Errorf("user = %q, want %q", spec.Image.User, tc.wantUser)
			}
			if spec.Workdir != tc.req.Workdir {
				t.Errorf("workdir = %q, want %q", spec.Workdir, tc.req.Workdir)
			}
			if tc.req.Labels != nil && spec.Labels["task"] != tc.req.Labels["task"] {
				t.Errorf("labels = %v, want task=%s", spec.Labels, tc.req.Labels["task"])
			}
			if got := len(spec.Credentials) > 0; got != tc.wantCred {
				t.Fatalf("credentials = %v, want present=%v", spec.Credentials, tc.wantCred)
			}
			if tc.wantCred {
				cred := spec.Credentials[0]
				if cred.Provider != tc.wantProvider || cred.EnvVar != tc.wantEnvVar {
					t.Errorf("credential = %+v, want provider=%s env=%s", cred, tc.wantProvider, tc.wantEnvVar)
				}
				if cred.Ref != "" {
					t.Errorf("credential.Ref = %q, want empty (the backend resolves the provider)", cred.Ref)
				}
			}
		})
	}
}

// TestSpecStagesReceiverExtension proves a run configured with a project and
// actor stages the ft msg extension into the workspace, and an unconfigured run
// does not.
func TestSpecStagesReceiverExtension(t *testing.T) {
	tests := []struct {
		name      string
		env       map[string]string
		wantStage bool
	}{
		{
			name:      "project and actor stage the extension",
			env:       map[string]string{harness.EnvProject: "prj", harness.EnvActor: "act"},
			wantStage: true,
		},
		{
			name:      "actor alone is not enough",
			env:       map[string]string{harness.EnvActor: "act"},
			wantStage: false,
		},
		{
			name:      "no receiver config stages nothing",
			env:       nil,
			wantStage: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := pi.New(pi.Options{Binary: "/opt/pi"}).Spec(harness.Request{Workdir: "/work", Env: tc.env})
			if err != nil {
				t.Fatalf("Spec() error = %v", err)
			}
			found := ""
			for _, file := range spec.Files {
				if file.Path == ".pi/extensions/factotum-msg.js" {
					found = string(file.Content)
				}
			}
			if tc.wantStage && found == "" {
				t.Fatalf("extension not staged; files = %v", spec.Files)
			}
			if !tc.wantStage && found != "" {
				t.Fatalf("extension staged without receiver config; files = %v", spec.Files)
			}
			if tc.wantStage && !strings.Contains(found, "factotumMsg") {
				t.Errorf("staged extension does not export the factory: %s", found)
			}
		})
	}
}

// TestCommandBuildsHeadlessInvocation pins the non-interactive invocation:
// `pi --approve --print`, the model flag, passthrough args, then `--` and the
// prompt. `--approve` trusts the project so the staged receiver extension loads
// (project-local extensions load only after trust); `--` separates a
// leading-dash prompt from the options.
func TestCommandBuildsHeadlessInvocation(t *testing.T) {
	tests := []struct {
		name string
		opts pi.Options
		req  harness.Request
		want []string
	}{
		{
			name: "model from the request",
			req:  harness.Request{Prompt: "do the thing", Model: "openai/gpt-5"},
			want: []string{"pi", "--approve", "--print", "--model", "openai/gpt-5", "--", "do the thing"},
		},
		{
			name: "configured model is the fallback",
			opts: pi.Options{Model: "openrouter/nvidia/nemotron"},
			req:  harness.Request{Prompt: "hi"},
			want: []string{"pi", "--approve", "--print", "--model", "openrouter/nvidia/nemotron", "--", "hi"},
		},
		{
			name: "request model overrides configured model",
			opts: pi.Options{Model: "provider/configured"},
			req:  harness.Request{Prompt: "hi", Model: "provider/requested"},
			want: []string{"pi", "--approve", "--print", "--model", "provider/requested", "--", "hi"},
		},
		{
			name: "no model flag when unset",
			req:  harness.Request{Prompt: "hi"},
			want: []string{"pi", "--approve", "--print", "--", "hi"},
		},
		{
			name: "leading-dash prompt is separated from the options",
			req:  harness.Request{Prompt: "-looks like a flag", Model: "p/m"},
			want: []string{"pi", "--approve", "--print", "--model", "p/m", "--", "-looks like a flag"},
		},
		{
			name: "configured and per-request args pass through before the prompt",
			opts: pi.Options{Args: []string{"--thinking", "high"}},
			req:  harness.Request{Prompt: "hi", Args: []string{"--no-session"}},
			want: []string{"pi", "--approve", "--print", "--thinking", "high", "--no-session", "--", "hi"},
		},
		{
			name: "binary and workdir are honored",
			opts: pi.Options{Binary: "/opt/pi", Model: "p/m"},
			req:  harness.Request{Prompt: "go", Workdir: "/work"},
			want: []string{"/opt/pi", "--approve", "--print", "--model", "p/m", "--", "go"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := pi.New(tc.opts).Command(tc.req)
			if err != nil {
				t.Fatalf("Command() error = %v", err)
			}
			if strings.Join(cmd.Argv, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("argv = %q, want %q", cmd.Argv, tc.want)
			}
			if tc.req.Workdir != "" && cmd.Workdir != tc.req.Workdir {
				t.Errorf("workdir = %q, want %q", cmd.Workdir, tc.req.Workdir)
			}
		})
	}
}

// TestCommandBuildsInteractiveInvocation pins the conversational mode: an
// interactive request drops `--print` (pi's TUI), keeps the model flag and the
// kickoff as the initial message, and marks the command as needing a terminal.
func TestCommandBuildsInteractiveInvocation(t *testing.T) {
	tests := []struct {
		name string
		opts pi.Options
		req  harness.Request
		want []string
	}{
		{
			name: "tui with the kickoff as the initial message",
			req:  harness.Request{Prompt: "groom these items", Interactive: true, Model: "openai/gpt-5"},
			want: []string{"pi", "--approve", "--model", "openai/gpt-5", "--", "groom these items"},
		},
		{
			name: "configured model and args pass through",
			opts: pi.Options{Model: "provider/configured", Args: []string{"--thinking", "high"}},
			req:  harness.Request{Prompt: "hi", Interactive: true, Args: []string{"--no-session"}},
			want: []string{"pi", "--approve", "--model", "provider/configured", "--thinking", "high", "--no-session", "--", "hi"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := pi.New(tc.opts).Command(tc.req)
			if err != nil {
				t.Fatalf("Command() error = %v", err)
			}
			if strings.Join(cmd.Argv, "\x00") != strings.Join(tc.want, "\x00") {
				t.Errorf("argv = %q, want %q", cmd.Argv, tc.want)
			}
			if !cmd.TTY {
				t.Error("TTY = false, want true for an interactive request")
			}
		})
	}
}

func TestCommandHeadlessIsNotTTY(t *testing.T) {
	cmd, err := pi.New(pi.Options{}).Command(harness.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if cmd.TTY {
		t.Error("TTY = true, want false for a headless request")
	}
}

// TestCommandCarriesReceiverEnv proves the msg hub environment (project/actor/
// task and the FACTOTUM_MSG_URL/FACTOTUM_SERVE_TOKEN pair) reaches pi's process,
// so a launched pi receiver speaks the remote hub transport.
func TestCommandCarriesReceiverEnv(t *testing.T) {
	env := map[string]string{
		harness.EnvProject:    "prj",
		harness.EnvActor:      "act",
		harness.EnvTask:       "t-1",
		harness.EnvMsgURL:     "http://hub:8484",
		harness.EnvServeToken: "tok",
	}
	cmd, err := pi.New(pi.Options{}).Command(harness.Request{Prompt: "hi", Env: env})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	for k, v := range env {
		if cmd.Env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, cmd.Env[k], v)
		}
	}
}

func TestCommandCarriesEnv(t *testing.T) {
	env := map[string]string{"HOME": "/tmp", "NO_COLOR": "1"}
	cmd, err := pi.New(pi.Options{}).Command(harness.Request{Prompt: "hi", Env: env})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	for k, v := range env {
		if cmd.Env[k] != v {
			t.Errorf("env[%s] = %q, want %q", k, cmd.Env[k], v)
		}
	}
	if _, ok := cmd.Env["OPENROUTER_API_KEY"]; ok {
		t.Errorf("env carries a credential; secrets must come from an attached Credential")
	}
}

func TestCommandRequiresPrompt(t *testing.T) {
	_, err := pi.New(pi.Options{}).Command(harness.Request{Model: "p/m"})
	if !errors.Is(err, pi.ErrNoPrompt) {
		t.Fatalf("Command() error = %v, want ErrNoPrompt", err)
	}
}

func TestDoneDetectsSentinel(t *testing.T) {
	tests := []struct {
		name     string
		sentinel string
		ev       isolation.Event
		want     bool
	}{
		{"no sentinel configured", "", isolation.Event{Kind: isolation.EventOutput, Message: "PI_DONE"}, false},
		{"sentinel in output", "PI_DONE", isolation.Event{Kind: isolation.EventOutput, Message: "all done\nPI_DONE"}, true},
		{"sentinel absent", "PI_DONE", isolation.Event{Kind: isolation.EventOutput, Message: "still working"}, false},
		{"sentinel only counts on output", "PI_DONE", isolation.Event{Kind: isolation.EventStatus, Message: "PI_DONE"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := pi.New(pi.Options{Sentinel: tc.sentinel})
			if got := h.Done(tc.ev); got != tc.want {
				t.Fatalf("Done(%v) = %v, want %v", tc.ev, got, tc.want)
			}
		})
	}
}

func TestResultParsesOutput(t *testing.T) {
	tests := []struct {
		name         string
		sentinel     string
		out          string
		wantOutput   string
		wantComplete bool
	}{
		{"trims whitespace", "", "  the answer  \n", "the answer", false},
		{"strips the sentinel and reports completion", "PI_DONE", "the answer\nPI_DONE\n", "the answer", true},
		{"sentinel absent leaves completion false", "PI_DONE", "partial", "partial", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := pi.New(pi.Options{Sentinel: tc.sentinel}).Result([]byte(tc.out))
			if err != nil {
				t.Fatalf("Result() error = %v", err)
			}
			if got.Output != tc.wantOutput {
				t.Errorf("Output = %q, want %q", got.Output, tc.wantOutput)
			}
			if got.Complete != tc.wantComplete {
				t.Errorf("Complete = %v, want %v", got.Complete, tc.wantComplete)
			}
		})
	}
}

func TestLocateBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := pi.LocateBinary("pi")
	if err != nil {
		t.Fatalf("LocateBinary() error = %v", err)
	}
	if got != bin {
		t.Errorf("LocateBinary() = %q, want %q", got, bin)
	}

	if _, err := pi.LocateBinary("definitely-not-installed-" + t.Name()); err == nil {
		t.Fatal("LocateBinary(missing) error = nil, want an error")
	}
}

// TestRunsThroughLocalBackend drives the harness end to end against a real
// (echo) process through the local isolation backend, so the port composition is
// exercised without the real pi binary. This pins the argv, prompt delivery,
// completion, and parsing.
func TestRunsThroughLocalBackend(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "pi")
	script := "#!/bin/sh\nprintf 'args:%s\\n' \"$*\"\nprintf 'PI_DONE\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	h := pi.New(pi.Options{
		Binary:   bin,
		Model:    "provider/model-1",
		Sentinel: "PI_DONE",
	})
	req := harness.Request{Prompt: "summarize the issue", Workdir: dir}

	backend := local.New(local.Options{AllowHost: true})
	spec, err := h.Spec(req)
	if err != nil {
		t.Fatalf("Spec() error = %v", err)
	}
	handle, err := backend.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	defer func() { _ = backend.Delete(context.Background(), handle) }()

	cmd, err := h.Command(req)
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	exec, err := backend.Exec(context.Background(), handle, cmd)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}

	complete := false
	for ev := range exec.Events() {
		if h.Done(ev) {
			complete = true
		}
	}
	res, err := exec.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	got, err := h.Result(res.Stdout)
	if err != nil {
		t.Fatalf("Result() error = %v", err)
	}
	if !complete || !got.Complete {
		t.Errorf("Complete: events=%v result=%v, want both true", complete, got.Complete)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", res.ExitCode)
	}
	for _, want := range []string{"--approve --print --model provider/model-1", "summarize the issue"} {
		if !strings.Contains(got.Output, want) {
			t.Errorf("output %q does not carry %q", got.Output, want)
		}
	}
}

var _ harness.Harness = (*pi.Harness)(nil)
