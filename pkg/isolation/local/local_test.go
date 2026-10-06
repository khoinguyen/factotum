package local_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
)

// newBackend builds an opted-in backend with the warning captured, the common
// setup for contract tests.
func newBackend(t *testing.T, opts local.Options) (*local.Backend, *bytes.Buffer) {
	t.Helper()
	warn := &bytes.Buffer{}
	opts.AllowHost = true
	opts.Warn = warn
	return local.New(opts), warn
}

// prepare stages an environment in a fresh temp workdir and returns the handle.
func prepare(t *testing.T, b *local.Backend, spec isolation.Spec) isolation.Handle {
	t.Helper()
	if spec.Workdir == "" {
		spec.Workdir = t.TempDir()
	}
	h, err := b.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	return h
}

// run executes cmd to completion and returns the result, failing on error.
func run(t *testing.T, b *local.Backend, h isolation.Handle, cmd isolation.Command) isolation.ExecResult {
	t.Helper()
	ex, err := b.Exec(context.Background(), h, cmd)
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	res, err := ex.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	return res
}

func TestName(t *testing.T) {
	if got := local.New(local.Options{}).Name(); got != "local" {
		t.Fatalf("Name() = %q, want local", got)
	}
}

func TestPrepareRefusesWithoutOptIn(t *testing.T) {
	warn := &bytes.Buffer{}
	b := local.New(local.Options{AllowHost: false, Warn: warn})
	_, err := b.Prepare(context.Background(), isolation.Spec{})
	if !errors.Is(err, local.ErrNotOptedIn) {
		t.Fatalf("Prepare() error = %v, want ErrNotOptedIn", err)
	}
	if warn.Len() != 0 {
		t.Fatalf("warn = %q, want no warning before opt-in", warn.String())
	}
}

func TestPrepareWarnsUnsandboxed(t *testing.T) {
	b, warn := newBackend(t, local.Options{})
	prepare(t, b, isolation.Spec{})
	got := strings.ToLower(warn.String())
	for _, want := range []string{"local", "unsandboxed", "host"} {
		if !strings.Contains(got, want) {
			t.Errorf("warning %q does not mention %q", warn.String(), want)
		}
	}
}

func TestPrepareStagesFiles(t *testing.T) {
	dir := t.TempDir()
	b, _ := newBackend(t, local.Options{})
	files := []isolation.File{
		{Path: "prompt.txt", Content: []byte("do the task"), Mode: 0o600},
		{Path: filepath.Join("nested", "cfg"), Content: []byte("x=1"), Mode: 0o644},
	}
	h, err := b.Prepare(context.Background(), isolation.Spec{Workdir: dir, Files: files})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if h.ID() == "" {
		t.Fatal("handle ID is empty")
	}
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil {
			t.Fatalf("read staged %s: %v", f.Path, err)
		}
		if string(data) != string(f.Content) {
			t.Errorf("staged %s = %q, want %q", f.Path, data, f.Content)
		}
	}
}

func TestPrepareRejectsUnsupportedPolicy(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	_, err := b.Prepare(context.Background(), isolation.Spec{
		Workdir: t.TempDir(),
		Policy:  isolation.Policy{DefaultDeny: true},
	})
	if !errors.Is(err, isolation.ErrUnsupported) {
		t.Fatalf("Prepare() error = %v, want ErrUnsupported", err)
	}
}

func TestExecContract(t *testing.T) {
	tests := []struct {
		name     string
		script   string
		stdin    []byte
		wantOut  string
		wantErr  string
		wantExit int
	}{
		{"stdout", `printf hello`, nil, "hello", "", 0},
		{"stderr", `printf oops >&2`, nil, "", "oops", 0},
		{"stdin", `cat`, []byte("piped"), "piped", "", 0},
		{"nonzero exit", `printf bye; exit 3`, nil, "bye", "", 3},
		{"both streams", `printf out; printf err >&2`, nil, "out", "err", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b, _ := newBackend(t, local.Options{})
			h := prepare(t, b, isolation.Spec{})
			ex, err := b.Exec(context.Background(), h, isolation.Command{
				Argv:  []string{"sh", "-c", tc.script},
				Stdin: tc.stdin,
			})
			if err != nil {
				t.Fatalf("Exec() error = %v", err)
			}

			var stdout, stderr strings.Builder
			for ev := range ex.Events() {
				if ev.Kind != isolation.EventOutput {
					continue
				}
				switch ev.Stream {
				case isolation.StreamStdout:
					stdout.WriteString(ev.Message)
				case isolation.StreamStderr:
					stderr.WriteString(ev.Message)
				}
			}
			res, err := ex.Wait(context.Background())
			if err != nil {
				t.Fatalf("Wait() error = %v", err)
			}
			if res.Stdout != nil && string(res.Stdout) != tc.wantOut {
				t.Errorf("Stdout = %q, want %q", res.Stdout, tc.wantOut)
			}
			if string(res.Stderr) != tc.wantErr {
				t.Errorf("Stderr = %q, want %q", res.Stderr, tc.wantErr)
			}
			if res.ExitCode != tc.wantExit {
				t.Errorf("ExitCode = %d, want %d", res.ExitCode, tc.wantExit)
			}
			if stdout.String() != tc.wantOut {
				t.Errorf("streamed stdout = %q, want %q", stdout.String(), tc.wantOut)
			}
			if stderr.String() != tc.wantErr {
				t.Errorf("streamed stderr = %q, want %q", stderr.String(), tc.wantErr)
			}
		})
	}
}

// TestWaitWithoutDrainingEvents pins the port contract that Wait is safe to call
// without consuming Events: the backend must not deadlock on an unread channel.
func TestWaitWithoutDrainingEvents(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	ex, err := b.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh", "-c", `printf hello`}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	res, err := ex.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if string(res.Stdout) != "hello" {
		t.Fatalf("Stdout = %q, want hello", res.Stdout)
	}
}

func TestExecEnvAndWorkdir(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	dir := t.TempDir()
	h := prepare(t, b, isolation.Spec{Workdir: dir})
	res := run(t, b, h, isolation.Command{
		Argv:    []string{"sh", "-c", `printf %s "$FOO"; printf ":"; pwd`},
		Env:     map[string]string{"FOO": "bar"},
		Workdir: dir,
	})
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", dir, err)
	}
	if got := strings.TrimSpace(string(res.Stdout)); got != "bar:"+resolved {
		t.Fatalf("Stdout = %q, want bar:%s", got, resolved)
	}
}

func TestUploadDownloadRoundTrip(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	files := []isolation.File{{Path: "out/result", Content: []byte("done"), Mode: 0o644}}
	if err := b.Upload(context.Background(), h, files); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	got, err := b.Download(context.Background(), h, []string{"out/result", "missing"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Download() returned %d files, want 1 (missing omitted)", len(got))
	}
	if string(got[0].Content) != "done" {
		t.Errorf("downloaded content = %q, want done", got[0].Content)
	}
}

func TestApplyPolicyUnsupported(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	if err := b.ApplyPolicy(context.Background(), h, isolation.Policy{}); err != nil {
		t.Fatalf("ApplyPolicy(zero) error = %v, want nil", err)
	}
	err := b.ApplyPolicy(context.Background(), h, isolation.Policy{AllowHosts: []string{"example.com"}, DefaultDeny: true})
	if !errors.Is(err, isolation.ErrUnsupported) {
		t.Fatalf("ApplyPolicy(non-zero) error = %v, want ErrUnsupported", err)
	}
}

func TestLogsUnsupported(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	_, err := b.Logs(context.Background(), h, isolation.LogOptions{})
	if !errors.Is(err, isolation.ErrUnsupported) {
		t.Fatalf("Logs() error = %v, want ErrUnsupported", err)
	}
}

func TestExecTTYUnsupported(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	_, err := b.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh", "-c", "true"}, TTY: true})
	if !errors.Is(err, isolation.ErrUnsupported) {
		t.Fatalf("Exec(TTY) error = %v, want ErrUnsupported", err)
	}
}

type fakeResolver struct {
	value string
	err   error
	got   isolation.Credential
}

func (r *fakeResolver) Resolve(_ context.Context, c isolation.Credential) (string, error) {
	r.got = c
	if r.err != nil {
		return "", r.err
	}
	return r.value, nil
}

func TestAttachCredentialRequiresResolver(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	err := b.AttachCredential(context.Background(), h, isolation.Credential{Provider: "openrouter", Ref: "ref", EnvVar: "K"})
	if !errors.Is(err, isolation.ErrUnsupported) {
		t.Fatalf("AttachCredential() error = %v, want ErrUnsupported without a resolver", err)
	}
}

func TestAttachCredentialRoutesThroughResolver(t *testing.T) {
	resolver := &fakeResolver{value: "secret"}
	b, _ := newBackend(t, local.Options{Credentials: resolver})
	h := prepare(t, b, isolation.Spec{})
	cred := isolation.Credential{Provider: "openrouter", Ref: "models/key", EnvVar: "OPENROUTER_API_KEY"}
	if err := b.AttachCredential(context.Background(), h, cred); err != nil {
		t.Fatalf("AttachCredential() error = %v", err)
	}
	if resolver.got != cred {
		t.Errorf("resolver got %+v, want %+v", resolver.got, cred)
	}
	res := run(t, b, h, isolation.Command{Argv: []string{"sh", "-c", `printf %s "$OPENROUTER_API_KEY"`}})
	if string(res.Stdout) != "secret" {
		t.Fatalf("credential in env = %q, want secret", res.Stdout)
	}
}

func TestAttachCredentialResolverErrorPropagates(t *testing.T) {
	wantErr := errors.New("no such provider")
	b, _ := newBackend(t, local.Options{Credentials: &fakeResolver{err: wantErr}})
	h := prepare(t, b, isolation.Spec{})
	err := b.AttachCredential(context.Background(), h, isolation.Credential{Provider: "x", Ref: "y", EnvVar: "K"})
	if !errors.Is(err, wantErr) {
		t.Fatalf("AttachCredential() error = %v, want %v", err, wantErr)
	}
}

func TestAttachCredentialRequiresEnvVar(t *testing.T) {
	b, _ := newBackend(t, local.Options{Credentials: &fakeResolver{value: "s"}})
	h := prepare(t, b, isolation.Spec{})
	if err := b.AttachCredential(context.Background(), h, isolation.Credential{Provider: "x", Ref: "y"}); err == nil {
		t.Fatal("AttachCredential() error = nil, want an error for a missing EnvVar")
	}
}

func TestStopKillsProcess(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	ex, err := b.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh", "-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	if err := b.Stop(context.Background(), h); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}

	done := make(chan isolation.ExecResult, 1)
	go func() {
		res, _ := ex.Wait(context.Background())
		done <- res
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Wait() did not return after Stop")
	}
}

func TestExecTimeoutStopsProcess(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	start := time.Now()
	res := run(t, b, h, isolation.Command{Argv: []string{"sh", "-c", "sleep 30"}, Timeout: 100 * time.Millisecond})
	if time.Since(start) > 10*time.Second {
		t.Fatal("timeout did not stop the process")
	}
	if res.ExitCode == 0 {
		t.Errorf("ExitCode = 0, want non-zero for a timed-out process")
	}
}

func TestDeleteKeepsCallerWorkdir(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	dir := t.TempDir()
	h := prepare(t, b, isolation.Spec{
		Workdir: dir,
		Files:   []isolation.File{{Path: "keep", Content: []byte("x"), Mode: 0o644}},
	})
	if err := b.Delete(context.Background(), h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Fatalf("caller workdir file gone after Delete: %v", err)
	}
}

func TestUnknownHandleErrors(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := otherHandle{}
	if err := b.Upload(context.Background(), h, nil); err == nil {
		t.Error("Upload(unknown handle) error = nil, want an error")
	}
	if _, err := b.Exec(context.Background(), h, isolation.Command{Argv: []string{"true"}}); err == nil {
		t.Error("Exec(unknown handle) error = nil, want an error")
	}
	if _, err := b.Download(context.Background(), h, nil); err == nil {
		t.Error("Download(unknown handle) error = nil, want an error")
	}
	if err := b.Stop(context.Background(), h); err == nil {
		t.Error("Stop(unknown handle) error = nil, want an error")
	}
	if err := b.Delete(context.Background(), h); err == nil {
		t.Error("Delete(unknown handle) error = nil, want an error")
	}
	if err := b.ApplyPolicy(context.Background(), h, isolation.Policy{}); err == nil {
		t.Error("ApplyPolicy(unknown handle) error = nil, want an error")
	}
	if err := b.AttachCredential(context.Background(), h, isolation.Credential{EnvVar: "K"}); err == nil {
		t.Error("AttachCredential(unknown handle) error = nil, want an error")
	}
}

type otherHandle struct{}

func (otherHandle) ID() string { return "other" }

func TestPrepareRejectsRelativeWorkdir(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	_, err := b.Prepare(context.Background(), isolation.Spec{Workdir: "relative/dir"})
	if err == nil {
		t.Fatal("Prepare(relative workdir) error = nil, want an error")
	}
}

func TestExecEmptyArgv(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	if _, err := b.Exec(context.Background(), h, isolation.Command{}); err == nil {
		t.Fatal("Exec(empty argv) error = nil, want an error")
	}
}

func TestNilHandleErrors(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	if _, err := b.Exec(context.Background(), nil, isolation.Command{Argv: []string{"true"}}); err == nil {
		t.Error("Exec(nil handle) error = nil, want an error")
	}
	if err := b.Upload(context.Background(), nil, nil); err == nil {
		t.Error("Upload(nil handle) error = nil, want an error")
	}
}

func TestUploadAbsolutePath(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	target := filepath.Join(t.TempDir(), "abs.txt")
	if err := b.Upload(context.Background(), h, []isolation.File{{Path: target, Content: []byte("abs"), Mode: 0o644}}); err != nil {
		t.Fatalf("Upload(absolute) error = %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read absolute upload: %v", err)
	}
	if string(data) != "abs" {
		t.Fatalf("absolute upload content = %q, want abs", data)
	}
}

func TestDownloadOmitsDirectory(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	if err := b.Upload(context.Background(), h, []isolation.File{{Path: "d/x", Content: []byte("x"), Mode: 0o644}}); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	got, err := b.Download(context.Background(), h, []string{"d"})
	if err != nil {
		t.Fatalf("Download(dir) error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Download(dir) returned %d files, want directories omitted", len(got))
	}
}

func TestDeleteThenUseFails(t *testing.T) {
	b, _ := newBackend(t, local.Options{})
	h := prepare(t, b, isolation.Spec{})
	if err := b.Delete(context.Background(), h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := b.Exec(context.Background(), h, isolation.Command{Argv: []string{"true"}}); err == nil {
		t.Fatal("Exec(after Delete) error = nil, want an error")
	}
}
