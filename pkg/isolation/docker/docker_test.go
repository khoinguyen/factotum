package docker_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/docker"
)

// --- test doubles -----------------------------------------------------------

// call records one invocation of the docker CLI.
type call struct {
	args []string
	env  map[string]string
}

// fakeRunner is an injectable docker CLI. It records every invocation and
// replays the programmed results, so the backend is exercised without a daemon.
type fakeRunner struct {
	mu      sync.Mutex
	runs    []call
	starts  []call
	runFn   func(ctx context.Context, args []string, env map[string]string) ([]byte, error)
	startFn func(ctx context.Context, args []string, in docker.StartInput) (docker.Process, error)
}

func (r *fakeRunner) Run(ctx context.Context, args []string, env map[string]string) ([]byte, error) {
	r.mu.Lock()
	r.runs = append(r.runs, call{args: clone(args), env: cloneMap(env)})
	r.mu.Unlock()
	if r.runFn != nil {
		return r.runFn(ctx, args, env)
	}
	if hasPrefix(args, "inspect") {
		return []byte("true\n"), nil
	}
	return nil, nil
}

func (r *fakeRunner) Start(ctx context.Context, args []string, in docker.StartInput) (docker.Process, error) {
	r.mu.Lock()
	r.starts = append(r.starts, call{args: clone(args), env: cloneMap(in.Env)})
	r.mu.Unlock()
	if r.startFn != nil {
		return r.startFn(ctx, args, in)
	}
	return &fakeProcess{}, nil
}

func (r *fakeRunner) ran() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.runs...)
}

func (r *fakeRunner) started() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.starts...)
}

// first returns the first recorded one-shot call whose args begin with prefix.
func (r *fakeRunner) first(prefix ...string) (call, bool) {
	for _, c := range r.ran() {
		if hasPrefix(c.args, prefix...) {
			return c, true
		}
	}
	return call{}, false
}

// count counts the one-shot calls whose args begin with prefix.
func (r *fakeRunner) count(prefix ...string) int {
	n := 0
	for _, c := range r.ran() {
		if hasPrefix(c.args, prefix...) {
			n++
		}
	}
	return n
}

// fakeProcess is a canned streaming process.
type fakeProcess struct {
	stdout io.Reader
	stderr io.Reader
	code   int
	wait   error

	waitCh chan struct{}
	once   sync.Once

	mu     sync.Mutex
	killed bool
}

func (p *fakeProcess) Stdout() io.ReadCloser { return io.NopCloser(orEmpty(p.stdout)) }
func (p *fakeProcess) Stderr() io.ReadCloser { return io.NopCloser(orEmpty(p.stderr)) }

func (p *fakeProcess) Wait() (int, error) {
	if p.waitCh != nil {
		<-p.waitCh
	}
	return p.code, p.wait
}

func (p *fakeProcess) Kill() error {
	p.mu.Lock()
	p.killed = true
	p.mu.Unlock()
	if p.waitCh != nil {
		p.once.Do(func() { close(p.waitCh) })
	}
	return nil
}

func (p *fakeProcess) wasKilled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.killed
}

// resolverFunc adapts a function to the CredentialResolver port.
type resolverFunc func(context.Context, isolation.Credential) (string, error)

func (f resolverFunc) Resolve(ctx context.Context, c isolation.Credential) (string, error) {
	return f(ctx, c)
}

// --- helpers ----------------------------------------------------------------

func orEmpty(r io.Reader) io.Reader {
	if r == nil {
		return strings.NewReader("")
	}
	return r
}

func clone(in []string) []string { return append([]string(nil), in...) }

func cloneMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func hasPrefix(args []string, prefix ...string) bool {
	if len(args) < len(prefix) {
		return false
	}
	for i, want := range prefix {
		if args[i] != want {
			return false
		}
	}
	return true
}

func hasSeq(args []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(args); i++ {
		match := true
		for j, want := range seq {
			if args[i+j] != want {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// newBackend returns a backend with a deterministic name and the fake runner.
func newBackend(r docker.Runner) *docker.Backend {
	return docker.New(docker.Options{
		Runner:  r,
		NewName: func() string { return "fttest" },
	})
}

// prepared prepares an environment and fails on error.
func prepared(t *testing.T, be *docker.Backend, spec isolation.Spec) isolation.Handle {
	t.Helper()
	h, err := be.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if h == nil || h.ID() == "" {
		t.Fatal("Prepare() returned an empty handle")
	}
	return h
}

// --- tests ------------------------------------------------------------------

func TestName(t *testing.T) {
	if got := docker.New(docker.Options{}).Name(); got != docker.Name {
		t.Fatalf("Name() = %q, want %q", got, docker.Name)
	}
}

// TestPrepareRunsDetachedContainer pins the container definition: a detached
// container from the spec image, kept alive by a shell init so Exec can run the
// harness with `docker exec`, and started with the workspace as its workdir.
func TestPrepareRunsDetachedContainer(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	prepared(t, be, isolation.Spec{Image: isolation.Image{Ref: "img:1"}})

	run, ok := r.first("run")
	if !ok {
		t.Fatalf("Prepare did not run a container: %v", r.ran())
	}
	for _, want := range [][]string{
		{"--detach"},
		{"--name", "fttest"},
		{"--entrypoint", "sh"},
	} {
		if !hasSeq(run.args, want...) {
			t.Errorf("run args %v missing %v", run.args, want)
		}
	}
	if !hasSeq(run.args, "img:1", "-c", docker.KeepAlive) {
		t.Errorf("run args %v do not start the image with the keep-alive init", run.args)
	}
	if r.count("inspect") == 0 {
		t.Error("Prepare did not verify the container is running")
	}
}

// TestPrepareMountsOnlyWorkspace pins the isolation boundary: the resolved
// workspace is the only bind mount, at the same absolute path inside the
// container, and no host control socket or root is mounted.
func TestPrepareMountsOnlyWorkspace(t *testing.T) {
	ws := t.TempDir()
	r := &fakeRunner{}
	be := newBackend(r)
	prepared(t, be, isolation.Spec{Workdir: ws})

	run, ok := r.first("run")
	if !ok {
		t.Fatal("Prepare did not run a container")
	}
	var mounts []string
	for i, a := range run.args {
		if a == "--mount" && i+1 < len(run.args) {
			mounts = append(mounts, run.args[i+1])
		}
	}
	if len(mounts) != 1 {
		t.Fatalf("mounts = %v, want exactly the workspace", mounts)
	}
	if want := "type=bind,source=" + ws + ",target=" + ws; mounts[0] != want {
		t.Errorf("mount = %q, want %q", mounts[0], want)
	}
	for _, a := range run.args {
		if strings.Contains(a, "docker.sock") || a == "/" {
			t.Errorf("run args %v mount the host", run.args)
		}
	}
	if got, _ := flagValue(run.args, "--workdir"); got != ws {
		t.Errorf("--workdir = %q, want the workspace %q", got, ws)
	}
}

// TestPrepareCreatesOwnedWorkspaceWhenEmpty pins that a spec with no workdir
// gets a fresh, owned workspace that Delete removes.
func TestPrepareCreatesOwnedWorkspaceWhenEmpty(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{})

	run, _ := r.first("run")
	ws, _ := flagValue(run.args, "--workdir")
	if ws == "" {
		t.Fatal("Prepare did not choose a workspace")
	}
	if _, err := os.Stat(ws); err != nil {
		t.Fatalf("workspace %q was not created: %v", ws, err)
	}
	if err := be.Delete(context.Background(), h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(ws); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned workspace %q survived Delete", ws)
	}
}

// TestPrepareRejectsNonEmptyPolicy pins the honest boundary: docker cannot
// enforce a filesystem or network policy, so it refuses instead of pretending.
func TestPrepareRejectsNonEmptyPolicy(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	_, err := be.Prepare(context.Background(), isolation.Spec{
		Policy: isolation.Policy{DefaultDeny: true, AllowHosts: []string{"example.com"}},
	})
	if !errors.Is(err, isolation.ErrUnsupported) {
		t.Fatalf("Prepare(policy) error = %v, want ErrUnsupported", err)
	}
	if r.count("run") != 0 {
		t.Error("Prepare(policy) started a container before rejecting the policy")
	}
}

// TestPrepareHonorsEnvLabelsAndUser pins that the spec's env, labels, and
// non-root identity reach the container.
func TestPrepareHonorsEnvLabelsAndUser(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	prepared(t, be, isolation.Spec{
		Env:    map[string]string{"B": "2", "A": "1"},
		Labels: map[string]string{"task": "t-1"},
		Image:  isolation.Image{Ref: "img", User: "1000:1000"},
	})

	run, _ := r.first("run")
	if !hasSeq(run.args, "--user", "1000:1000") {
		t.Errorf("run args %v missing --user", run.args)
	}
	if !hasSeq(run.args, "--label", "task=t-1") {
		t.Errorf("run args %v missing the label", run.args)
	}
	// Environment is injected per-exec, never into container metadata, so the
	// run itself carries no --env.
	if hasSeq(run.args, "--env") {
		t.Errorf("run args %v leak env into container metadata", run.args)
	}
}

// TestExecStreamsAndWaits pins that Exec runs `docker exec`, streams both
// streams, injects env and workdir, and reports the remote exit code.
func TestExecStreamsAndWaits(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{Env: map[string]string{"A": "1"}})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	r.startFn = func(context.Context, []string, docker.StartInput) (docker.Process, error) {
		return &fakeProcess{
			stdout: strings.NewReader("hello"),
			stderr: strings.NewReader("oops"),
			code:   3,
		}, nil
	}
	ex, err := be.Exec(context.Background(), h, isolation.Command{
		Argv:    []string{"sh", "-c", "x"},
		Workdir: "/host/ws",
		Env:     map[string]string{"B": "2"},
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	var out, errOut strings.Builder
	for ev := range ex.Events() {
		if ev.Kind != isolation.EventOutput {
			continue
		}
		switch ev.Stream {
		case isolation.StreamStdout:
			out.WriteString(ev.Message)
		case isolation.StreamStderr:
			errOut.WriteString(ev.Message)
		}
	}
	res, err := ex.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if res.ExitCode != 3 || string(res.Stdout) != "hello" || string(res.Stderr) != "oops" {
		t.Fatalf("result = %+v, want code 3 stdout hello stderr oops", res)
	}
	if out.String() != "hello" || errOut.String() != "oops" {
		t.Fatalf("streamed = %q/%q, want hello/oops", out.String(), errOut.String())
	}

	start := r.started()[0]
	for _, want := range [][]string{
		{"exec", "--workdir", "/host/ws"},
		{"--env", "A"},
		{"--env", "B"},
		{"fttest", "sh", "-c", "x"},
	} {
		if !hasSeq(start.args, want...) {
			t.Errorf("exec args %v missing %v", start.args, want)
		}
	}
	// The values ride in the CLI's environment, never argv.
	if !hasSeq(start.args, "--env", "A") || start.env["A"] != "1" {
		t.Errorf("exec env = %v, want A=1 via the client environment", start.env)
	}
	if strings.Contains(strings.Join(start.args, " "), "=") {
		t.Errorf("exec args %v embed a value", start.args)
	}
}

// TestExecStdinAddsInteractive pins that a command with stdin is run
// interactively so the piped input reaches the process.
func TestExecStdinAddsInteractive(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if _, err := be.Exec(context.Background(), h, isolation.Command{
		Argv:  []string{"sh", "-c", "cat"},
		Stdin: []byte("piped"),
	}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	start := r.started()[0]
	if !hasSeq(start.args, "exec", "--interactive") {
		t.Errorf("exec args %v missing --interactive for stdin", start.args)
	}
}

// TestExecTTYUnsupported pins that an interactive terminal is refused, not
// silently run without one.
func TestExecTTYUnsupported(t *testing.T) {
	be := newBackend(&fakeRunner{})
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if _, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh"}, TTY: true}); !isUnsupported(err) {
		t.Fatalf("Exec(TTY) error = %v, want ErrUnsupported", err)
	}
}

// TestUploadCopiesFiles pins that Upload creates parents and copies each file
// into the container.
func TestUploadCopiesFiles(t *testing.T) {
	ws := t.TempDir()
	r := &fakeRunner{}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{Workdir: ws})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	files := []isolation.File{
		{Path: "a.txt", Content: []byte("alpha"), Mode: 0o644},
		{Path: "nested/b.txt", Content: []byte("beta"), Mode: 0o600},
	}
	if err := be.Upload(context.Background(), h, files); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if got := r.count("cp"); got != len(files) {
		t.Fatalf("cp calls = %d, want %d: %v", got, len(files), r.ran())
	}
	if r.count("exec") == 0 {
		t.Error("Upload did not create parent directories")
	}
}

// TestDownloadCopiesOutAndOmitsMissing pins that Download copies paths out,
// maps directory walks back to the requested path, and omits missing paths.
func TestDownloadCopiesOutAndOmitsMissing(t *testing.T) {
	ws := t.TempDir()
	r := &fakeRunner{}
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		switch {
		case hasPrefix(args, "inspect"):
			return []byte("true\n"), nil
		case hasPrefix(args, "cp"):
			guest := strings.SplitN(args[1], ":", 2)[1]
			dest := args[2]
			base := path.Base(guest)
			if strings.HasSuffix(guest, "missing.txt") {
				return nil, errors.New("no such file")
			}
			if base == "out" {
				_ = os.MkdirAll(filepath.Join(dest, "out", "sub"), 0o755)
				_ = os.WriteFile(filepath.Join(dest, "out", "x.txt"), []byte("x"), 0o644)
				_ = os.WriteFile(filepath.Join(dest, "out", "sub", "y.txt"), []byte("y"), 0o644)
				return nil, nil
			}
			return nil, os.WriteFile(filepath.Join(dest, base), []byte("alpha"), 0o644)
		}
		return nil, nil
	}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{Workdir: ws})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	got, err := be.Download(context.Background(), h, []string{"a.txt", "out", "missing.txt"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	byPath := map[string]string{}
	for _, f := range got {
		byPath[f.Path] = string(f.Content)
	}
	if byPath["a.txt"] != "alpha" {
		t.Errorf("a.txt = %q, want alpha (got %v)", byPath["a.txt"], byPath)
	}
	if byPath["out/x.txt"] != "x" || byPath["out/sub/y.txt"] != "y" {
		t.Errorf("directory walk = %v, want out/x.txt and out/sub/y.txt", byPath)
	}
	if _, ok := byPath["missing.txt"]; ok {
		t.Error("missing.txt should be omitted")
	}
}

// TestStopCancelsRunningCommands pins that Stop ends tracked executions,
// keeps the container, and is idempotent.
func TestStopCancelsRunningCommands(t *testing.T) {
	r := &fakeRunner{}
	proc := &fakeProcess{waitCh: make(chan struct{})}
	r.startFn = func(context.Context, []string, docker.StartInput) (docker.Process, error) {
		return proc, nil
	}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	ex, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh", "-c", "sleep 30"}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	if err := be.Stop(context.Background(), h); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if !proc.wasKilled() {
		t.Fatal("Stop() did not cancel the running command")
	}
	if _, err := ex.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after Stop error = %v", err)
	}
	if err := be.Stop(context.Background(), h); err != nil {
		t.Fatalf("second Stop() error = %v, want nil", err)
	}
	if r.count("rm") != 0 {
		t.Error("Stop() removed the container, want it kept for restart")
	}
}

// TestDeleteRemovesContainerAndIsIdempotent pins that Delete force-removes the
// container, is idempotent, and leaves the handle unusable.
func TestDeleteRemovesContainerAndIsIdempotent(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{})

	if err := be.Delete(context.Background(), h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok := r.first("rm", "-f", "fttest"); !ok {
		t.Errorf("Delete did not remove the container: %v", r.ran())
	}
	if err := be.Delete(context.Background(), h); err != nil {
		t.Fatalf("second Delete() error = %v, want nil", err)
	}
	if _, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh"}}); err == nil {
		t.Error("Exec(after Delete) error = nil, want an error")
	}
}

// TestHandleHygiene pins that a foreign or nil handle is rejected by every
// operation before any capability check.
func TestHandleHygiene(t *testing.T) {
	be := newBackend(&fakeRunner{})
	ctx := context.Background()
	ops := map[string]func(isolation.Handle) error{
		"Exec": func(h isolation.Handle) error {
			_, err := be.Exec(ctx, h, isolation.Command{Argv: []string{"sh"}})
			return err
		},
		"Upload":      func(h isolation.Handle) error { return be.Upload(ctx, h, nil) },
		"Download":    func(h isolation.Handle) error { _, err := be.Download(ctx, h, []string{"x"}); return err },
		"Logs":        func(h isolation.Handle) error { _, err := be.Logs(ctx, h, isolation.LogOptions{}); return err },
		"Stop":        func(h isolation.Handle) error { return be.Stop(ctx, h) },
		"Delete":      func(h isolation.Handle) error { return be.Delete(ctx, h) },
		"ApplyPolicy": func(h isolation.Handle) error { return be.ApplyPolicy(ctx, h, isolation.Policy{}) },
		"AttachCredential": func(h isolation.Handle) error {
			return be.AttachCredential(ctx, h, isolation.Credential{Provider: "p", Ref: "r", EnvVar: "K"})
		},
	}
	for name, call := range ops {
		t.Run(name, func(t *testing.T) {
			if err := call(foreignHandle{}); !handleRejected(err) {
				t.Errorf("%s(foreign handle) error = %v, want a handle rejection", name, err)
			}
			if err := call(nil); !handleRejected(err) {
				t.Errorf("%s(nil handle) error = %v, want a handle rejection", name, err)
			}
		})
	}
}

// TestAttachCredential pins credential handling: a configured resolver injects
// the secret into later execs via the CLI environment, never argv; without a
// resolver the backend honestly reports ErrUnsupported.
func TestAttachCredential(t *testing.T) {
	t.Run("with resolver", func(t *testing.T) {
		r := &fakeRunner{}
		be := docker.New(docker.Options{
			Runner:      r,
			NewName:     func() string { return "fttest" },
			Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "sekret", nil }),
		})
		h := prepared(t, be, isolation.Spec{})
		t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

		if err := be.AttachCredential(context.Background(), h, isolation.Credential{Provider: "openrouter", Ref: "r", EnvVar: "KEY"}); err != nil {
			t.Fatalf("AttachCredential() error = %v", err)
		}
		if _, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh"}}); err != nil {
			t.Fatalf("Exec() error = %v", err)
		}
		start := r.started()[0]
		if !hasSeq(start.args, "--env", "KEY") {
			t.Errorf("exec args %v missing --env KEY", start.args)
		}
		if start.env["KEY"] != "sekret" {
			t.Errorf("exec env = %v, want the secret via the client environment", start.env)
		}
		for _, a := range start.args {
			if strings.Contains(a, "sekret") {
				t.Errorf("secret leaked into argv: %v", start.args)
			}
		}
	})
	t.Run("no env var", func(t *testing.T) {
		be := docker.New(docker.Options{
			Runner:      &fakeRunner{},
			NewName:     func() string { return "fttest" },
			Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "v", nil }),
		})
		h := prepared(t, be, isolation.Spec{})
		t.Cleanup(func() { _ = be.Delete(context.Background(), h) })
		if err := be.AttachCredential(context.Background(), h, isolation.Credential{Provider: "p", Ref: "r"}); err == nil {
			t.Fatal("AttachCredential(no EnvVar) error = nil, want an error")
		}
	})
	t.Run("no resolver", func(t *testing.T) {
		be := newBackend(&fakeRunner{})
		h := prepared(t, be, isolation.Spec{})
		t.Cleanup(func() { _ = be.Delete(context.Background(), h) })
		err := be.AttachCredential(context.Background(), h, isolation.Credential{Provider: "p", Ref: "r", EnvVar: "K"})
		if !isUnsupported(err) {
			t.Fatalf("AttachCredential(no resolver) error = %v, want ErrUnsupported", err)
		}
	})
}

// TestApplyPolicy pins the policy contract: empty is a no-op, a real policy is
// honestly unsupported.
func TestApplyPolicy(t *testing.T) {
	r := &fakeRunner{}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if err := be.ApplyPolicy(context.Background(), h, isolation.Policy{}); err != nil {
		t.Fatalf("ApplyPolicy(empty) error = %v", err)
	}
	err := be.ApplyPolicy(context.Background(), h, isolation.Policy{DefaultDeny: true})
	if !isUnsupported(err) {
		t.Fatalf("ApplyPolicy(policy) error = %v, want ErrUnsupported", err)
	}
}

// TestLogsStreamsEvents pins that Logs returns a non-nil channel and emits the
// container's lines.
func TestLogsStreamsEvents(t *testing.T) {
	r := &fakeRunner{}
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		if hasPrefix(args, "logs") {
			return []byte("first\nsecond\n"), nil
		}
		if hasPrefix(args, "inspect") {
			return []byte("true\n"), nil
		}
		return nil, nil
	}
	be := newBackend(r)
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	events, err := be.Logs(context.Background(), h, isolation.LogOptions{})
	if err != nil {
		t.Fatalf("Logs() error = %v", err)
	}
	if events == nil {
		t.Fatal("Logs() channel = nil")
	}
	var lines int
	for ev := range events {
		if ev.Kind == isolation.EventOutput {
			lines++
		}
	}
	if lines != 2 {
		t.Fatalf("Logs() emitted %d output events, want 2", lines)
	}
}

// TestPrepareFailsWhenContainerDies pins that a container that exits immediately
// is reported, and its resources are cleaned up, rather than returned as ready.
func TestPrepareFailsWhenContainerDies(t *testing.T) {
	r := &fakeRunner{}
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		if hasPrefix(args, "inspect") {
			return []byte("false\n"), nil
		}
		return nil, nil
	}
	be := docker.New(docker.Options{
		Runner:       r,
		NewName:      func() string { return "fttest" },
		ReadyTimeout: 20 * time.Millisecond,
	})
	if _, err := be.Prepare(context.Background(), isolation.Spec{}); err == nil {
		t.Fatal("Prepare() error = nil, want the container-death failure")
	}
	if _, ok := r.first("rm", "-f", "fttest"); !ok {
		t.Errorf("failed Prepare did not clean up the container: %v", r.ran())
	}
}

// --- small helpers ----------------------------------------------------------

// foreignHandle is a handle the backend did not create.
type foreignHandle struct{}

func (foreignHandle) ID() string { return "foreign" }

func isUnsupported(err error) bool {
	return err != nil && errors.Is(err, isolation.ErrUnsupported)
}

func handleRejected(err error) bool {
	return err != nil && !errors.Is(err, isolation.ErrUnsupported)
}
