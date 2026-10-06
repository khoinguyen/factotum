package opencode_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/harness/opencode"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
)

func TestName(t *testing.T) {
	if got := opencode.New(opencode.Options{}).Name(); got != "opencode" {
		t.Fatalf("Name() = %q, want opencode", got)
	}
}

func TestSpecDeclaresImageEntrypointAndIdentity(t *testing.T) {
	tests := []struct {
		name         string
		opts         opencode.Options
		req          harness.Request
		wantImage    string
		wantEntry    []string
		wantUser     string
		wantCred     bool
		wantProvider string
		wantEnvVar   string
	}{
		{
			name:      "defaults use the shipped image and binary",
			wantImage: opencode.DefaultImage,
			wantEntry: []string{opencode.DefaultBinary},
			wantUser:  opencode.DefaultUser,
		},
		{
			name:      "overrides win",
			opts:      opencode.Options{Image: "ghcr.io/acme/oc:pinned", Binary: "/usr/local/bin/opencode", User: "501:20"},
			req:       harness.Request{Workdir: "/work", Labels: map[string]string{"task": "t-1"}},
			wantImage: "ghcr.io/acme/oc:pinned",
			wantEntry: []string{"/usr/local/bin/opencode"},
			wantUser:  "501:20",
		},
		{
			name:         "provider credential is declared without a secret",
			opts:         opencode.Options{Provider: "openrouter", CredentialEnvVar: "OPENROUTER_API_KEY"},
			wantImage:    opencode.DefaultImage,
			wantEntry:    []string{opencode.DefaultBinary},
			wantUser:     opencode.DefaultUser,
			wantCred:     true,
			wantProvider: "openrouter",
			wantEnvVar:   "OPENROUTER_API_KEY",
		},
		{
			name:      "partial credential config declares nothing",
			opts:      opencode.Options{Provider: "openrouter"},
			wantImage: opencode.DefaultImage,
			wantEntry: []string{opencode.DefaultBinary},
			wantUser:  opencode.DefaultUser,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := opencode.New(tc.opts).Spec(tc.req)
			if err != nil {
				t.Fatalf("Spec() error = %v", err)
			}
			if spec.Image.Ref != tc.wantImage {
				t.Errorf("image = %q, want %q", spec.Image.Ref, tc.wantImage)
			}
			if strings.Join(spec.Image.Entrypoint, " ") != strings.Join(tc.wantEntry, " ") {
				t.Errorf("entrypoint = %v, want %v", spec.Image.Entrypoint, tc.wantEntry)
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

func TestCommandBuildsHeadlessInvocation(t *testing.T) {
	tests := []struct {
		name string
		opts opencode.Options
		req  harness.Request
		want []string
	}{
		{
			name: "model from the request",
			req:  harness.Request{Prompt: "do the thing", Model: "opencode-go/deepseek-v4.1-flash"},
			want: []string{"opencode", "run", "--model", "opencode-go/deepseek-v4.1-flash", "--", "do the thing"},
		},
		{
			name: "configured model is the fallback",
			opts: opencode.Options{Model: "openrouter/nvidia/nemotron"},
			req:  harness.Request{Prompt: "hi"},
			want: []string{"opencode", "run", "--model", "openrouter/nvidia/nemotron", "--", "hi"},
		},
		{
			name: "request model overrides configured model",
			opts: opencode.Options{Model: "provider/configured"},
			req:  harness.Request{Prompt: "hi", Model: "provider/requested"},
			want: []string{"opencode", "run", "--model", "provider/requested", "--", "hi"},
		},
		{
			name: "no model flag when unset",
			req:  harness.Request{Prompt: "hi"},
			want: []string{"opencode", "run", "--", "hi"},
		},
		{
			name: "leading-dash prompt is separated from the options",
			req:  harness.Request{Prompt: "-looks like a flag", Model: "p/m"},
			want: []string{"opencode", "run", "--model", "p/m", "--", "-looks like a flag"},
		},
		{
			name: "configured and per-request args pass through before the prompt",
			opts: opencode.Options{Args: []string{"--agent", "build"}},
			req:  harness.Request{Prompt: "hi", Args: []string{"--pure"}},
			want: []string{"opencode", "run", "--agent", "build", "--pure", "--", "hi"},
		},
		{
			name: "binary and workdir are honored",
			opts: opencode.Options{Binary: "/opt/opencode", Model: "p/m"},
			req:  harness.Request{Prompt: "go", Workdir: "/work"},
			want: []string{"/opt/opencode", "run", "--model", "p/m", "--", "go"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := opencode.New(tc.opts).Command(tc.req)
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

func TestCommandCarriesEnv(t *testing.T) {
	env := map[string]string{"HOME": "/tmp", "NO_COLOR": "1"}
	cmd, err := opencode.New(opencode.Options{}).Command(harness.Request{Prompt: "hi", Env: env})
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
	_, err := opencode.New(opencode.Options{}).Command(harness.Request{Model: "p/m"})
	if !errors.Is(err, opencode.ErrNoPrompt) {
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
		{"no sentinel configured", "", isolation.Event{Kind: isolation.EventOutput, Message: "OPENCODE_DONE"}, false},
		{"sentinel in output", "OPENCODE_DONE", isolation.Event{Kind: isolation.EventOutput, Message: "all done\nOPENCODE_DONE"}, true},
		{"sentinel absent", "OPENCODE_DONE", isolation.Event{Kind: isolation.EventOutput, Message: "still working"}, false},
		{"sentinel only counts on output", "OPENCODE_DONE", isolation.Event{Kind: isolation.EventStatus, Message: "OPENCODE_DONE"}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := opencode.New(opencode.Options{Sentinel: tc.sentinel})
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
		{"strips the sentinel and reports completion", "OPENCODE_DONE", "the answer\nOPENCODE_DONE\n", "the answer", true},
		{"sentinel absent leaves completion false", "OPENCODE_DONE", "partial", "partial", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := opencode.New(opencode.Options{Sentinel: tc.sentinel}).Result([]byte(tc.out))
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
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	t.Setenv("PATH", dir)

	got, err := opencode.LocateBinary("opencode")
	if err != nil {
		t.Fatalf("LocateBinary() error = %v", err)
	}
	if got != bin {
		t.Errorf("LocateBinary() = %q, want %q", got, bin)
	}

	if _, err := opencode.LocateBinary("definitely-not-installed-" + t.Name()); err == nil {
		t.Fatal("LocateBinary(missing) error = nil, want an error")
	}
}

// TestRunsThroughLocalBackend drives the harness end to end against a real
// (echo) process through the local isolation backend, so the port composition
// is exercised without the real opencode binary. The launcher dogfood runs the
// real binary; this pins the argv, prompt delivery, completion, and parsing.
func TestRunsThroughLocalBackend(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	script := "#!/bin/sh\nprintf 'args:%s\\n' \"$*\"\nprintf 'OPENCODE_DONE\\n'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	h := opencode.New(opencode.Options{
		Binary:   bin,
		Model:    "provider/model-1",
		Sentinel: "OPENCODE_DONE",
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
	for _, want := range []string{"run", "--model provider/model-1", "summarize the issue"} {
		if !strings.Contains(got.Output, want) {
			t.Errorf("output %q does not carry %q", got.Output, want)
		}
	}
}

var _ harness.Harness = (*opencode.Harness)(nil)
