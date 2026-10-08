package openshell_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
)

// --- test doubles -----------------------------------------------------------

// call records one invocation of the openshell CLI.
type call struct {
	args []string
	env  map[string]string
}

// fakeRunner is an injectable openShell CLI. It records every invocation and
// replays the programmed results, so the backend is exercised without a gateway.
type fakeRunner struct {
	mu      sync.Mutex
	runs    []call
	starts  []call
	runFn   func(ctx context.Context, args []string, env map[string]string) ([]byte, error)
	startFn func(ctx context.Context, args []string, in openshell.StartInput) (openshell.Process, error)
}

func (r *fakeRunner) Run(ctx context.Context, args []string, env map[string]string) ([]byte, error) {
	r.mu.Lock()
	r.runs = append(r.runs, call{args: clone(args), env: cloneMap(env)})
	r.mu.Unlock()
	if r.runFn != nil {
		return r.runFn(ctx, args, env)
	}
	return []byte(`{"phase":"Ready"}`), nil
}

func (r *fakeRunner) Start(ctx context.Context, args []string, in openshell.StartInput) (openshell.Process, error) {
	r.mu.Lock()
	r.starts = append(r.starts, call{args: clone(args), env: cloneMap(in.Env)})
	r.mu.Unlock()
	if r.startFn != nil {
		return r.startFn(ctx, args, in)
	}
	return &fakeProcess{}, nil
}

// started returns the streaming invocations recorded so far.
func (r *fakeRunner) started() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.starts...)
}

// ran returns the one-shot invocations recorded so far.
func (r *fakeRunner) ran() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.runs...)
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

// hasPrefix reports whether args begins with the given sequence.
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

// contains reports whether args contains the given sequence anywhere.
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

// flagValue returns the value following flag in args.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
	}
	return "", false
}

// prepared prepares an environment with the fake runner and fails on error.
func prepared(t *testing.T, be *openshell.Backend, spec isolation.Spec) isolation.Handle {
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
	be := openshell.New(openshell.Options{Runner: &fakeRunner{}})
	if be.Name() != openshell.Name {
		t.Fatalf("Name() = %q, want %q", be.Name(), openshell.Name)
	}
}

// TestPrepareCreatesHardenedSandbox pins that Prepare creates a detached
// sandbox from the spec image with the checked-in policy: deny-by-default
// egress and a non-root identity, never auto-approved policy proposals.
func TestPrepareCreatesHardenedSandbox(t *testing.T) {
	r := &fakeRunner{}
	var policy []byte
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		if hasPrefix(args, "sandbox", "create") {
			if path, ok := flagValue(args, "--policy"); ok {
				policy, _ = os.ReadFile(path)
			}
		}
		return []byte(`{"phase":"Ready"}`), nil
	}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})

	prepared(t, be, isolation.Spec{Image: isolation.Image{Ref: "img:1"}})

	create, ok := r.first("sandbox", "create")
	if !ok {
		t.Fatal("Prepare did not create a sandbox")
	}
	for _, want := range [][]string{
		{"--name", "fttest"},
		{"--from", "img:1"},
		{"--no-auto-providers"},
		{"--approval-mode", "manual"},
		{"--detach"},
	} {
		if !hasSeq(create.args, want...) {
			t.Errorf("sandbox create args %v missing %v", create.args, want)
		}
	}
	if _, ok := flagValue(create.args, "--policy"); !ok {
		t.Fatal("sandbox create did not pass --policy")
	}
	// Readiness is confirmed before the handle is returned.
	if r.count("sandbox", "get") == 0 {
		t.Error("Prepare did not wait for the sandbox to become ready")
	}

	if len(policy) == 0 {
		t.Fatal("policy file was not written")
	}
	if bytes.Contains(policy, []byte("network_policies")) {
		t.Errorf("default policy opens egress:\n%s", policy)
	}
	if !bytes.Contains(policy, []byte(`run_as_user: "1000"`)) {
		t.Errorf("default policy is not non-root:\n%s", policy)
	}
}

// TestPrepareUsesDefaultImage pins that a spec with no image falls back to the
// backend's default, so the conformance suite can Prepare an empty spec.
func TestPrepareUsesDefaultImage(t *testing.T) {
	r := &fakeRunner{}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
	prepared(t, be, isolation.Spec{})

	create, _ := r.first("sandbox", "create")
	if got, _ := flagValue(create.args, "--from"); got != openshell.DefaultImage {
		t.Fatalf("--from = %q, want the default image %q", got, openshell.DefaultImage)
	}
}

// TestPrepareAllowsExplicitHosts pins that per-run allow hosts widen egress in
// the rendered policy.
func TestPrepareAllowsExplicitHosts(t *testing.T) {
	r := &fakeRunner{}
	var policy []byte
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		if hasPrefix(args, "sandbox", "create") {
			if path, ok := flagValue(args, "--policy"); ok {
				policy, _ = os.ReadFile(path)
			}
		}
		return []byte(`{"phase":"Ready"}`), nil
	}
	be := openshell.New(openshell.Options{
		Runner:     r,
		NewName:    func() string { return "fttest" },
		AllowHosts: []string{"example.com"},
	})
	prepared(t, be, isolation.Spec{})

	if !bytes.Contains(policy, []byte("network_policies")) || !bytes.Contains(policy, []byte("example.com")) {
		t.Fatalf("policy did not allow example.com:\n%s", policy)
	}
}

// TestPrepareUploadsHostWorkspace pins that a host workspace named by
// Spec.Workdir is uploaded into the sandbox and becomes the run's workdir.
func TestPrepareUploadsHostWorkspace(t *testing.T) {
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "hello.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write host file: %v", err)
	}
	r := &fakeRunner{}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})

	h := prepared(t, be, isolation.Spec{Workdir: host})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if _, ok := r.first("sandbox", "upload", "fttest", host, openshell.DefaultWorkdir); !ok {
		t.Fatalf("host workspace %q was not uploaded to %q; calls=%v", host, openshell.DefaultWorkdir, r.runs)
	}

	// A command whose workdir is the host path runs in the guest copy.
	r.startFn = func(context.Context, []string, openshell.StartInput) (openshell.Process, error) {
		return &fakeProcess{}, nil
	}
	if _, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh", "-c", "true"}, Workdir: host}); err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	start := r.started()[0]
	want := openshell.DefaultWorkdir + "/" + filepath.Base(host)
	if got, _ := flagValue(start.args, "--workdir"); got != want {
		t.Fatalf("--workdir = %q, want %q", got, want)
	}
}

// TestPrepareUploadsSpecFiles pins that Spec.Files are placed in the workdir.
func TestPrepareUploadsSpecFiles(t *testing.T) {
	r := &fakeRunner{}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})

	prepared(t, be, isolation.Spec{
		Files: []isolation.File{{Path: "stage/hello.txt", Content: []byte("staged"), Mode: 0o644}},
	})

	if r.count("sandbox", "upload") == 0 {
		t.Fatalf("Spec.Files were not uploaded; calls=%v", r.runs)
	}
}

// TestExecStreamsAndWaits pins that Exec streams both streams and reports the
// remote exit code, and that Wait is safe without draining Events.
func TestExecStreamsAndWaits(t *testing.T) {
	r := &fakeRunner{}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	r.startFn = func(context.Context, []string, openshell.StartInput) (openshell.Process, error) {
		return &fakeProcess{
			stdout: strings.NewReader("hello"),
			stderr: strings.NewReader("oops"),
			code:   3,
		}, nil
	}

	ex, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh", "-c", "x"}, Env: map[string]string{"B": "2", "A": "1"}})
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
		{"sandbox", "exec", "-n", "fttest"},
		{"--no-login-shell"},
		{"--workdir", openshell.DefaultWorkdir},
		{"--env", "A=1"},
		{"--env", "B=2"},
		{"--", "sh", "-c", "x"},
	} {
		if !hasSeq(start.args, want...) {
			t.Errorf("exec args %v missing %v", start.args, want)
		}
	}
}

// TestExecTTYUnsupported pins that an interactive command is refused, not
// silently run without a terminal.
func TestExecTTYUnsupported(t *testing.T) {
	be := openshell.New(openshell.Options{Runner: &fakeRunner{}, NewName: func() string { return "fttest" }})
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if _, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh"}, TTY: true}); !isUnsupported(err) {
		t.Fatalf("Exec(TTY) error = %v, want ErrUnsupported", err)
	}
}

// TestUploadStagesFiles pins that Upload places each file at its workdir path.
func TestUploadStagesFiles(t *testing.T) {
	r := &fakeRunner{}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	files := []isolation.File{
		{Path: "a.txt", Content: []byte("alpha"), Mode: 0o644},
		{Path: "nested/b.txt", Content: []byte("beta"), Mode: 0o600},
	}
	if err := be.Upload(context.Background(), h, files); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}
	if r.count("sandbox", "upload") != len(files) {
		t.Fatalf("upload calls = %d, want %d: %v", r.count("sandbox", "upload"), len(files), r.runs)
	}
}

// TestDownloadStreamsViaExec pins the exec-streaming download path: files are
// decoded from base64, a missing path is omitted, and a directory is walked.
func TestDownloadStreamsViaExec(t *testing.T) {
	r := &fakeRunner{}
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, "\x00"), "FTFILE") {
			return []byte(`{"phase":"Ready"}`), nil
		}
		var b strings.Builder
		emit := func(path, content string) {
			b.WriteString("FTFILE\t" + path + "\t" + base64.StdEncoding.EncodeToString([]byte(content)) + "\n")
		}
		emit(openshell.DefaultWorkdir+"/a.txt", "alpha")
		emit(openshell.DefaultWorkdir+"/out/x.txt", "x")
		emit(openshell.DefaultWorkdir+"/out/sub/y.txt", "y")
		return []byte(b.String()), nil
	}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
	h := prepared(t, be, isolation.Spec{})
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
		t.Errorf("missing.txt should be omitted")
	}
}

// TestStopCancelsRunningCommands pins that Stop ends tracked executions and is
// idempotent.
func TestStopCancelsRunningCommands(t *testing.T) {
	r := &fakeRunner{}
	proc := &fakeProcess{waitCh: make(chan struct{})}
	r.startFn = func(context.Context, []string, openshell.StartInput) (openshell.Process, error) {
		return proc, nil
	}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
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
}

// TestDeleteIsIdempotentAndCleansProviders pins that Delete removes the sandbox
// and every provider it created, is idempotent, and leaves the handle unusable.
func TestDeleteIsIdempotentAndCleansProviders(t *testing.T) {
	r := &fakeRunner{}
	be := openshell.New(openshell.Options{
		Runner:      r,
		NewName:     func() string { return "fttest" },
		Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "sekret", nil }),
	})
	h := prepared(t, be, isolation.Spec{
		Credentials: []isolation.Credential{{Provider: "openrouter", Ref: "ref", EnvVar: "OPENROUTER_API_KEY"}},
	})

	// The provider is created with the secret in the child environment, never
	// in argv.
	create, ok := r.first("provider", "create")
	if !ok {
		t.Fatalf("no provider create call: %v", r.runs)
	}
	if create.env["OPENROUTER_API_KEY"] != "sekret" {
		t.Errorf("provider create env = %v, want the secret passed via env", create.env)
	}
	for _, a := range create.args {
		if strings.Contains(a, "sekret") {
			t.Errorf("secret leaked into argv: %v", create.args)
		}
	}
	provider, _ := flagValue(create.args, "--name")
	sandboxCreate, _ := r.first("sandbox", "create")
	if !hasSeq(sandboxCreate.args, "--provider", provider) {
		t.Errorf("sandbox create args %v missing --provider %s", sandboxCreate.args, provider)
	}

	if err := be.Delete(context.Background(), h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, ok := r.first("sandbox", "delete", "fttest"); !ok {
		t.Errorf("Delete did not delete the sandbox: %v", r.runs)
	}
	if _, ok := r.first("provider", "delete", provider); !ok {
		t.Errorf("Delete did not delete provider %s: %v", provider, r.runs)
	}
	if err := be.Delete(context.Background(), h); err != nil {
		t.Fatalf("second Delete() error = %v, want nil", err)
	}
	if _, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh"}}); err == nil {
		t.Error("Exec(after Delete) error = nil, want an error")
	}
}

// TestAttachCredential pins credential attachment and its honest refusal.
func TestAttachCredential(t *testing.T) {
	t.Run("with resolver", func(t *testing.T) {
		r := &fakeRunner{}
		be := openshell.New(openshell.Options{
			Runner:      r,
			NewName:     func() string { return "fttest" },
			Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "v", nil }),
		})
		h := prepared(t, be, isolation.Spec{})
		t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

		if err := be.AttachCredential(context.Background(), h, isolation.Credential{Provider: "openrouter", Ref: "r", EnvVar: "KEY"}); err != nil {
			t.Fatalf("AttachCredential() error = %v", err)
		}
		if _, ok := r.first("provider", "create"); !ok {
			t.Fatalf("no provider created: %v", r.runs)
		}
		provider, _ := flagValue(r.ran()[len(r.ran())-1].args, "--name")
		if r.count("sandbox", "provider", "attach") == 0 {
			t.Errorf("provider not attached: %v", r.runs)
		}
		_ = provider
	})
	t.Run("no env var", func(t *testing.T) {
		r := &fakeRunner{}
		be := openshell.New(openshell.Options{
			Runner:      r,
			NewName:     func() string { return "fttest" },
			Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "v", nil }),
		})
		h := prepared(t, be, isolation.Spec{})
		t.Cleanup(func() { _ = be.Delete(context.Background(), h) })
		if err := be.AttachCredential(context.Background(), h, isolation.Credential{Provider: "openrouter", Ref: "r"}); err == nil {
			t.Fatal("AttachCredential(no EnvVar) error = nil, want an error")
		}
	})
	t.Run("no resolver", func(t *testing.T) {
		be := openshell.New(openshell.Options{Runner: &fakeRunner{}, NewName: func() string { return "fttest" }})
		h := prepared(t, be, isolation.Spec{})
		t.Cleanup(func() { _ = be.Delete(context.Background(), h) })
		err := be.AttachCredential(context.Background(), h, isolation.Credential{Provider: "openrouter", Ref: "r", EnvVar: "KEY"})
		if !isUnsupported(err) {
			t.Fatalf("AttachCredential(no resolver) error = %v, want ErrUnsupported", err)
		}
	})
}

// TestProviderProfileProvisioning pins the fresh-gateway path: when the gateway
// rejects a provider create because its profile is not imported, the backend
// imports the profile from the catalog and retries; if the import cannot run,
// the error names the exact import command.
func TestProviderProfileProvisioning(t *testing.T) {
	const (
		providerType = "openrouter"
		wantURL      = "https://raw.githubusercontent.com/NVIDIA/OpenShell/main/providers/openrouter.yaml"
	)
	missing := errors.New("openshell provider create: exit status 1: provider profile 'openrouter' not found; import a matching profile before using this provider type")

	newBackend := func(r *fakeRunner) *openshell.Backend {
		return openshell.New(openshell.Options{
			Runner:      r,
			NewName:     func() string { return "fttest" },
			Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "sekret", nil }),
		})
	}
	spec := isolation.Spec{
		Credentials: []isolation.Credential{{Provider: providerType, Ref: "ref", EnvVar: "OPENROUTER_API_KEY"}},
	}

	t.Run("imports the missing profile then retries", func(t *testing.T) {
		r := &fakeRunner{}
		var creates int
		r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
			if hasPrefix(args, "provider", "create") {
				creates++
				if creates == 1 {
					return nil, missing
				}
			}
			return []byte(`{"phase":"Ready"}`), nil
		}
		be := newBackend(r)
		h := prepared(t, be, spec)
		t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

		imp, ok := r.first("profile", "import")
		if !ok {
			t.Fatalf("missing profile was not imported: %v", r.ran())
		}
		if got, _ := flagValue(imp.args, "--url"); got != wantURL {
			t.Errorf("import url = %q, want %q", got, wantURL)
		}
		if creates != 2 {
			t.Errorf("provider create ran %d times, want 2 (fail, import, retry)", creates)
		}
	})

	t.Run("names the exact import command when auto-import fails", func(t *testing.T) {
		r := &fakeRunner{}
		r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
			switch {
			case hasPrefix(args, "provider", "create"):
				return nil, missing
			case hasPrefix(args, "profile", "import"):
				return nil, errors.New("network unreachable")
			}
			return []byte(`{"phase":"Ready"}`), nil
		}
		_, err := newBackend(r).Prepare(context.Background(), spec)
		if err == nil {
			t.Fatal("Prepare() error = nil, want an actionable error")
		}
		wantCmd := "openshell profile import --url " + wantURL
		if !strings.Contains(err.Error(), wantCmd) {
			t.Errorf("error %q does not name the import command %q", err, wantCmd)
		}
		if !strings.Contains(err.Error(), providerType) {
			t.Errorf("error %q does not name the provider type %q", err, providerType)
		}
	})

	t.Run("an unrelated create failure is not retried", func(t *testing.T) {
		r := &fakeRunner{}
		r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
			if hasPrefix(args, "provider", "create") {
				return nil, errors.New("provider already exists")
			}
			return []byte(`{"phase":"Ready"}`), nil
		}
		_, err := newBackend(r).Prepare(context.Background(), spec)
		if err == nil {
			t.Fatal("Prepare() error = nil, want an error")
		}
		if r.count("profile", "import") != 0 {
			t.Errorf("unrelated failure triggered a profile import: %v", r.ran())
		}
	})
}

// TestApplyPolicy pins the policy contract: empty is a no-op, a real policy is
// installed on the live sandbox.
func TestApplyPolicy(t *testing.T) {
	r := &fakeRunner{}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
	h := prepared(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if err := be.ApplyPolicy(context.Background(), h, isolation.Policy{}); err != nil {
		t.Fatalf("ApplyPolicy(empty) error = %v", err)
	}
	if r.count("policy", "set") != 0 {
		t.Fatal("ApplyPolicy(empty) installed a policy, want a no-op")
	}

	err := be.ApplyPolicy(context.Background(), h, isolation.Policy{AllowHosts: []string{"example.com"}, DefaultDeny: true})
	if err != nil {
		t.Fatalf("ApplyPolicy() error = %v", err)
	}
	if r.count("policy", "set") == 0 {
		t.Fatalf("ApplyPolicy did not set the policy: %v", r.runs)
	}
}

// TestHandleHygiene pins that a foreign or nil handle is rejected, never
// dereferenced.
func TestHandleHygiene(t *testing.T) {
	be := openshell.New(openshell.Options{Runner: &fakeRunner{}, NewName: func() string { return "fttest" }})
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
			if err := call(foreignHandle{}); err == nil {
				t.Errorf("%s(foreign handle) error = nil, want an error", name)
			}
			if err := call(nil); err == nil {
				t.Errorf("%s(nil handle) error = nil, want an error", name)
			}
		})
	}
}

// TestLogsStreamsEvents pins that Logs returns a non-nil event channel.
func TestLogsStreamsEvents(t *testing.T) {
	r := &fakeRunner{}
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		if hasPrefix(args, "logs") {
			return []byte("first\nsecond\n"), nil
		}
		return []byte(`{"phase":"Ready"}`), nil
	}
	be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
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

// --- cleanup and identity regression tests ---------------------------------

// TestPrepareHonorsImageUser pins that a harness-requested non-root identity
// carried by Spec.Image.User reaches the sandbox policy, and that a root image
// is rejected rather than run.
func TestPrepareHonorsImageUser(t *testing.T) {
	t.Run("uid:gid", func(t *testing.T) {
		r := &fakeRunner{}
		var policy []byte
		r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
			if hasPrefix(args, "sandbox", "create") {
				if p, ok := flagValue(args, "--policy"); ok {
					policy, _ = os.ReadFile(p)
				}
			}
			return []byte(`{"phase":"Ready"}`), nil
		}
		be := openshell.New(openshell.Options{Runner: r, NewName: func() string { return "fttest" }})
		prepared(t, be, isolation.Spec{Image: isolation.Image{Ref: "img", User: "1001:1002"}})

		for _, want := range []string{`run_as_user: "1001"`, `run_as_group: "1002"`} {
			if !bytes.Contains(policy, []byte(want)) {
				t.Errorf("policy missing %s:\n%s", want, policy)
			}
		}
	})
	t.Run("root rejected", func(t *testing.T) {
		be := openshell.New(openshell.Options{Runner: &fakeRunner{}, NewName: func() string { return "fttest" }})
		if _, err := be.Prepare(context.Background(), isolation.Spec{Image: isolation.Image{Ref: "img", User: "0"}}); err == nil {
			t.Fatal("Prepare(root image) error = nil, want a rejection")
		}
	})
}

// TestPrepareTeardownRetriesProviderCleanup pins finding 1: a partial Prepare
// failure must not orphan a provider. The retrying cleanup is used and the
// provider is eventually deleted.
func TestPrepareTeardownRetriesProviderCleanup(t *testing.T) {
	r := &fakeRunner{}
	var deleteAttempts int
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		switch {
		case hasPrefix(args, "sandbox", "get"):
			return []byte(`{"phase":"Pending"}`), nil // never ready
		case hasPrefix(args, "provider", "get"):
			return []byte("exists"), nil
		case hasPrefix(args, "provider", "delete"):
			deleteAttempts++
			if deleteAttempts < 3 {
				return nil, errors.New("still attached")
			}
			return nil, nil
		}
		return nil, nil
	}
	be := openshell.New(openshell.Options{
		Runner:                  r,
		NewName:                 func() string { return "fttest" },
		ReadyTimeout:            20 * time.Millisecond,
		ProviderCleanupTimeout:  200 * time.Millisecond,
		ProviderCleanupInterval: 5 * time.Millisecond,
		Credentials:             resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "v", nil }),
	})

	_, err := be.Prepare(context.Background(), isolation.Spec{
		Credentials: []isolation.Credential{{Provider: "openrouter", Ref: "r", EnvVar: "KEY"}},
	})
	if err == nil {
		t.Fatal("Prepare() error = nil, want the readiness failure")
	}
	if deleteAttempts < 3 {
		t.Fatalf("provider delete attempts = %d, want the cleanup to retry past the transient failures", deleteAttempts)
	}
	if _, ok := r.first("sandbox", "delete", "fttest"); !ok {
		t.Fatalf("teardown did not delete the created sandbox: %v", r.runs)
	}
}

// TestDeleteRetriesAfterSandboxDeleteFailure pins finding 2: a transient sandbox
// delete failure must not turn a retry into a no-op; the second Delete completes
// the cleanup.
func TestDeleteRetriesAfterSandboxDeleteFailure(t *testing.T) {
	r := &fakeRunner{}
	var sandboxDeletes, providerDeletes int
	r.runFn = func(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
		switch {
		case hasPrefix(args, "sandbox", "delete"):
			sandboxDeletes++
			if sandboxDeletes == 1 {
				return nil, errors.New("transient")
			}
			return nil, nil
		case hasPrefix(args, "provider", "get"):
			return []byte("exists"), nil
		case hasPrefix(args, "provider", "delete"):
			providerDeletes++
			return nil, nil
		case hasPrefix(args, "sandbox", "get"):
			return []byte(`{"phase":"Ready"}`), nil
		}
		return nil, nil
	}
	be := openshell.New(openshell.Options{
		Runner:      r,
		NewName:     func() string { return "fttest" },
		Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) { return "v", nil }),
	})
	h := prepared(t, be, isolation.Spec{
		Credentials: []isolation.Credential{{Provider: "openrouter", Ref: "r", EnvVar: "KEY"}},
	})

	if err := be.Delete(context.Background(), h); err == nil {
		t.Fatal("first Delete() error = nil, want the transient failure")
	}
	if providerDeletes != 0 {
		t.Fatalf("provider deletes after a failed sandbox delete = %d, want 0", providerDeletes)
	}
	if err := be.Delete(context.Background(), h); err != nil {
		t.Fatalf("second Delete() error = %v, want the retry to complete", err)
	}
	if sandboxDeletes != 2 {
		t.Fatalf("sandbox deletes = %d, want 2 (retried)", sandboxDeletes)
	}
	if providerDeletes != 1 {
		t.Fatalf("provider deletes = %d, want 1 (cleanup completed on retry)", providerDeletes)
	}
	if _, err := be.Exec(context.Background(), h, isolation.Command{Argv: []string{"sh"}}); err == nil {
		t.Error("Exec(after a completed retry) error = nil, want an error")
	}
}

// --- small helpers ----------------------------------------------------------

// foreignHandle is a handle the backend did not create.
type foreignHandle struct{}

func (foreignHandle) ID() string { return "foreign" }

func isUnsupported(err error) bool {
	return err != nil && strings.Contains(err.Error(), isolation.ErrUnsupported.Error())
}
