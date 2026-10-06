// Package conformance is the shared contract suite every isolation backend must
// pass. It is the definition of an IsolationBackend.
//
// A backend wires in from its own package with a Factory that returns a fresh,
// ready-to-use instance, so a backend with an opt-in step (for example the
// dev-only local host backend) can satisfy the suite without the suite knowing
// about that step:
//
//	func TestConformance(t *testing.T) {
//		conformance.Run(t, func(t *testing.T) isolation.IsolationBackend {
//			return local.New(local.Options{AllowHost: true, Warn: io.Discard})
//		})
//	}
//
// The suite exercises the mandatory lifecycle (Prepare, Exec, Upload, Download,
// Stop, Delete) and the port's error contract: unknown handles are rejected,
// operations a backend does not implement return ErrUnsupported, Wait is safe
// without draining Events, and Stop/Delete are idempotent. An optional operation
// a backend does implement is declared with an Option (WithLogs, WithPolicy); an
// undeclared one must return ErrUnsupported, never another error or a silent
// success.
package conformance

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// shell is the interpreter the suite uses to produce deterministic output.
// Every viable execution environment for an agent ships a POSIX shell.
const shell = "sh"

// waitTimeout bounds every Wait so a backend that fails to honor Stop, Delete,
// or a context deadline fails the suite instead of hanging it.
const waitTimeout = 30 * time.Second

// Factory builds a fresh, ready-to-use backend. It is called once per subtest
// and must return a backend that can Prepare a spec with no policy and no
// credentials. The suite never closes the backend.
type Factory func(t *testing.T) isolation.IsolationBackend

// config records the optional capabilities a backend declares.
type config struct {
	logs   bool
	policy bool
}

// Option declares an optional capability so the suite requires it to work,
// rather than tolerating ErrUnsupported.
type Option func(*config)

// WithLogs declares that the backend implements Logs.
func WithLogs() Option { return func(c *config) { c.logs = true } }

// WithPolicy declares that the backend enforces a non-empty Policy.
func WithPolicy() Option { return func(c *config) { c.policy = true } }

// Run runs the full contract against the backend factory.
func Run(t *testing.T, factory Factory, opts ...Option) {
	t.Helper()
	var cfg config
	for _, opt := range opts {
		opt(&cfg)
	}
	t.Run("Name", func(t *testing.T) { testName(t, factory(t)) })
	t.Run("Prepare", func(t *testing.T) { testPrepare(t, factory(t)) })
	t.Run("Exec", func(t *testing.T) { testExec(t, factory(t)) })
	t.Run("UploadDownload", func(t *testing.T) { testUploadDownload(t, factory(t)) })
	t.Run("WaitWithoutDrain", func(t *testing.T) { testWaitWithoutDrain(t, factory(t)) })
	t.Run("Stop", func(t *testing.T) { testStop(t, factory(t)) })
	t.Run("Delete", func(t *testing.T) { testDelete(t, factory(t)) })
	t.Run("HandleHygiene", func(t *testing.T) { testHandleHygiene(t, factory(t)) })
	t.Run("Unsupported", func(t *testing.T) { testUnsupported(t, factory(t), cfg) })
	t.Run("Credentials", func(t *testing.T) { testCredentials(t, factory(t)) })
}

func testName(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	if strings.TrimSpace(be.Name()) == "" {
		t.Fatal("Name() is empty")
	}
}

// testPrepare pins that Prepare returns a distinct, usable handle, and that the
// Spec's staged files and environment reach the environment.
func testPrepare(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	ctx := context.Background()

	h1 := prepareEnv(t, be, isolation.Spec{
		Env:   map[string]string{"FT_CONFORMANCE": "yes"},
		Files: []isolation.File{{Path: "stage/hello.txt", Content: []byte("staged"), Mode: 0o644}},
	})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h1) })
	h2 := prepareEnv(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h2) })

	if h1.ID() == h2.ID() {
		t.Errorf("two Prepare calls returned the same handle ID %q", h1.ID())
	}

	got, err := be.Download(ctx, h1, []string{"stage/hello.txt"})
	if err != nil {
		t.Fatalf("Download(staged) error = %v", err)
	}
	if len(got) != 1 || string(got[0].Content) != "staged" {
		t.Fatalf("Download(staged) = %+v, want the staged content", got)
	}

	res := run(t, be, h1, isolation.Command{Argv: []string{shell, "-c", `printf %s "$FT_CONFORMANCE"`}})
	if string(res.Stdout) != "yes" {
		t.Fatalf("Spec.Env not applied: Stdout = %q, want yes", res.Stdout)
	}
}

// testExec is the table of the Exec contract: streams, exit codes, stdin, and
// per-command env all round-trip through Events and Wait.
func testExec(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	tests := []struct {
		name     string
		cmd      isolation.Command
		wantOut  string
		wantErr  string
		wantExit int
	}{
		{"stdout", isolation.Command{Argv: []string{shell, "-c", "printf hello"}}, "hello", "", 0},
		{"stderr", isolation.Command{Argv: []string{shell, "-c", "printf oops >&2"}}, "", "oops", 0},
		{"both streams", isolation.Command{Argv: []string{shell, "-c", "printf out; printf err >&2"}}, "out", "err", 0},
		{"stdin", isolation.Command{Argv: []string{shell, "-c", "cat"}, Stdin: []byte("piped")}, "piped", "", 0},
		{"nonzero exit", isolation.Command{Argv: []string{shell, "-c", "printf bye; exit 3"}}, "bye", "", 3},
		{"env", isolation.Command{Argv: []string{shell, "-c", `printf %s "$FOO"`}, Env: map[string]string{"FOO": "bar"}}, "bar", "", 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := prepareEnv(t, be, isolation.Spec{})
			t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

			ex, err := be.Exec(context.Background(), h, tc.cmd)
			if err != nil {
				t.Fatalf("Exec() error = %v", err)
			}
			var streamOut, streamErr strings.Builder
			for ev := range ex.Events() {
				if ev.Kind != isolation.EventOutput {
					continue
				}
				switch ev.Stream {
				case isolation.StreamStdout:
					streamOut.WriteString(ev.Message)
				case isolation.StreamStderr:
					streamErr.WriteString(ev.Message)
				}
			}
			res := waitResult(t, ex)

			if res.ExitCode != tc.wantExit {
				t.Errorf("ExitCode = %d, want %d", res.ExitCode, tc.wantExit)
			}
			if string(res.Stdout) != tc.wantOut {
				t.Errorf("Stdout = %q, want %q", res.Stdout, tc.wantOut)
			}
			if string(res.Stderr) != tc.wantErr {
				t.Errorf("Stderr = %q, want %q", res.Stderr, tc.wantErr)
			}
			if streamOut.String() != tc.wantOut {
				t.Errorf("streamed stdout = %q, want %q", streamOut.String(), tc.wantOut)
			}
			if streamErr.String() != tc.wantErr {
				t.Errorf("streamed stderr = %q, want %q", streamErr.String(), tc.wantErr)
			}
		})
	}
}

func testUploadDownload(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	ctx := context.Background()
	h := prepareEnv(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	files := []isolation.File{
		{Path: "a.txt", Content: []byte("alpha"), Mode: 0o644},
		{Path: "nested/b.txt", Content: []byte("beta"), Mode: 0o600},
	}
	if err := be.Upload(ctx, h, files); err != nil {
		t.Fatalf("Upload() error = %v", err)
	}

	got, err := be.Download(ctx, h, []string{"a.txt", "nested/b.txt", "missing.txt"})
	if err != nil {
		t.Fatalf("Download() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Download() returned %d files, want 2 (missing omitted)", len(got))
	}
	byPath := map[string]string{}
	for _, f := range got {
		byPath[f.Path] = string(f.Content)
	}
	for _, want := range files {
		if byPath[want.Path] != string(want.Content) {
			t.Errorf("Download(%s) = %q, want %q", want.Path, byPath[want.Path], want.Content)
		}
	}
}

// testWaitWithoutDrain pins the port contract that Wait is safe to call without
// consuming Events: a backend that blocks on an unread event channel deadlocks
// here, because the output is far larger than any reasonable event buffer.
func testWaitWithoutDrain(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	h := prepareEnv(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	const output = 10000
	ex, err := be.Exec(context.Background(), h, isolation.Command{
		Argv: []string{shell, "-c", "i=0; while [ $i -lt 10000 ]; do printf x; i=$((i+1)); done"},
	})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	res := waitResult(t, ex)
	if len(res.Stdout) != output {
		t.Fatalf("Stdout length = %d, want %d (Wait must not require draining Events)", len(res.Stdout), output)
	}
}

// testStop pins that Stop ends a running command and is idempotent, including
// when nothing is running.
func testStop(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	ctx := context.Background()
	h := prepareEnv(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	if err := be.Stop(ctx, h); err != nil {
		t.Fatalf("Stop() error = %v, want nil with nothing running", err)
	}
	if err := be.Stop(ctx, h); err != nil {
		t.Fatalf("second Stop() error = %v, want idempotent nil", err)
	}

	ex, err := be.Exec(ctx, h, isolation.Command{Argv: []string{shell, "-c", "touch .ft-stop-started; sleep 30"}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	waitForFile(t, be, h, ".ft-stop-started")
	if err := be.Stop(ctx, h); err != nil {
		t.Fatalf("Stop(running) error = %v", err)
	}
	waitResult(t, ex)
}

// testDelete pins that Delete tears down a running command, is idempotent, and
// leaves the handle unusable.
func testDelete(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	ctx := context.Background()
	h := prepareEnv(t, be, isolation.Spec{})

	ex, err := be.Exec(ctx, h, isolation.Command{Argv: []string{shell, "-c", "touch .ft-delete-started; sleep 30"}})
	if err != nil {
		t.Fatalf("Exec() error = %v", err)
	}
	waitForFile(t, be, h, ".ft-delete-started")

	if err := be.Delete(ctx, h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := be.Delete(ctx, h); err != nil {
		t.Fatalf("second Delete() error = %v, want idempotent nil", err)
	}
	waitResult(t, ex)

	if _, err := be.Exec(ctx, h, isolation.Command{Argv: []string{shell, "-c", "true"}}); err == nil {
		t.Error("Exec(after Delete) error = nil, want an error")
	}
	if err := be.Stop(ctx, h); err == nil {
		t.Error("Stop(after Delete) error = nil, want an error")
	}
}

// testHandleHygiene pins that a foreign or nil handle is rejected by every
// operation, never dereferenced or silently accepted.
func testHandleHygiene(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	ctx := context.Background()
	ops := []struct {
		name string
		call func(isolation.Handle) error
	}{
		{"Exec", func(h isolation.Handle) error {
			_, err := be.Exec(ctx, h, isolation.Command{Argv: []string{shell, "-c", "true"}})
			return err
		}},
		{"Upload", func(h isolation.Handle) error { return be.Upload(ctx, h, nil) }},
		{"Download", func(h isolation.Handle) error {
			_, err := be.Download(ctx, h, []string{"x"})
			return err
		}},
		{"Logs", func(h isolation.Handle) error {
			_, err := be.Logs(ctx, h, isolation.LogOptions{})
			return err
		}},
		{"Stop", func(h isolation.Handle) error { return be.Stop(ctx, h) }},
		{"Delete", func(h isolation.Handle) error { return be.Delete(ctx, h) }},
		{"ApplyPolicy", func(h isolation.Handle) error { return be.ApplyPolicy(ctx, h, isolation.Policy{}) }},
		{"AttachCredential", func(h isolation.Handle) error {
			return be.AttachCredential(ctx, h, isolation.Credential{Provider: "p", Ref: "r", EnvVar: "K"})
		}},
	}
	for _, op := range ops {
		t.Run(op.name, func(t *testing.T) {
			if err := op.call(foreignHandle{}); err == nil {
				t.Errorf("%s(foreign handle) error = nil, want an error", op.name)
			}
			if err := op.call(nil); err == nil {
				t.Errorf("%s(nil handle) error = nil, want an error", op.name)
			}
		})
	}
}

// testUnsupported pins the error contract for optional operations: a declared
// capability must work, and an undeclared one must return ErrUnsupported rather
// than fail some other way or silently succeed. Applying an empty policy is a
// no-op every backend must accept.
func testUnsupported(t *testing.T, be isolation.IsolationBackend, cfg config) {
	t.Helper()
	ctx := context.Background()
	h := prepareEnv(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	policy := isolation.Policy{
		ReadOnly:    []string{"/usr"},
		ReadWrite:   []string{"/tmp"},
		AllowHosts:  []string{"example.com"},
		DefaultDeny: true,
	}

	t.Run("Logs", func(t *testing.T) {
		events, err := be.Logs(ctx, h, isolation.LogOptions{})
		if !cfg.logs {
			assertUnsupported(t, "Logs", err)
			return
		}
		if err != nil {
			t.Fatalf("Logs() error = %v, want nil", err)
		}
		if events == nil {
			t.Fatal("Logs() events channel = nil")
		}
	})

	t.Run("ApplyPolicyEmpty", func(t *testing.T) {
		if err := be.ApplyPolicy(ctx, h, isolation.Policy{}); err != nil {
			t.Fatalf("ApplyPolicy(empty) error = %v, want nil", err)
		}
	})

	t.Run("ApplyPolicy", func(t *testing.T) {
		err := be.ApplyPolicy(ctx, h, policy)
		if cfg.policy {
			if err != nil {
				t.Fatalf("ApplyPolicy() error = %v, want nil", err)
			}
			return
		}
		assertUnsupported(t, "ApplyPolicy", err)
	})

	t.Run("PreparePolicy", func(t *testing.T) {
		ph, err := be.Prepare(ctx, isolation.Spec{Policy: policy})
		if !cfg.policy {
			assertUnsupported(t, "Prepare(policy)", err)
			return
		}
		if err != nil {
			t.Fatalf("Prepare(policy) error = %v, want nil", err)
		}
		if err := be.Delete(context.Background(), ph); err != nil {
			t.Fatalf("Delete(policy env) error = %v", err)
		}
	})

	t.Run("ExecTTY", func(t *testing.T) {
		ex, err := be.Exec(ctx, h, isolation.Command{Argv: []string{shell, "-c", "true"}, TTY: true})
		if err == nil {
			// The backend supports an interactive command; it must still finish.
			drainWait(t, ex)
			return
		}
		assertUnsupported(t, "Exec(TTY)", err)
	})
}

// testCredentials pins credential handling: a backend either attaches a
// well-formed credential or honestly reports ErrUnsupported; one that attaches
// must reject a credential with no destination variable.
func testCredentials(t *testing.T, be isolation.IsolationBackend) {
	t.Helper()
	ctx := context.Background()
	h := prepareEnv(t, be, isolation.Spec{})
	t.Cleanup(func() { _ = be.Delete(context.Background(), h) })

	err := be.AttachCredential(ctx, h, isolation.Credential{Provider: "conformance", Ref: "ref", EnvVar: "FT_CONFORMANCE_CRED"})
	switch {
	case err == nil:
		if err := be.AttachCredential(ctx, h, isolation.Credential{Provider: "conformance", Ref: "ref"}); err == nil {
			t.Error("AttachCredential(no EnvVar) error = nil, want an error")
		}
	case errors.Is(err, isolation.ErrUnsupported):
		// Honest: this backend does not attach credentials.
	default:
		t.Fatalf("AttachCredential() error = %v, want nil or ErrUnsupported", err)
	}
}

// prepareEnv prepares an environment and fails the test on error.
func prepareEnv(t *testing.T, be isolation.IsolationBackend, spec isolation.Spec) isolation.Handle {
	t.Helper()
	h, err := be.Prepare(context.Background(), spec)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	if h == nil {
		t.Fatal("Prepare() handle = nil")
	}
	if h.ID() == "" {
		t.Fatal("Prepare() handle ID is empty")
	}
	return h
}

// run drains Events, waits, and returns the result.
func run(t *testing.T, be isolation.IsolationBackend, h isolation.Handle, cmd isolation.Command) isolation.ExecResult {
	t.Helper()
	ex, err := be.Exec(context.Background(), h, cmd)
	if err != nil {
		t.Fatalf("Exec(%v) error = %v", cmd.Argv, err)
	}
	for range ex.Events() {
	}
	return waitResult(t, ex)
}

// waitResult waits for an execution with a bounded context.
func waitResult(t *testing.T, ex isolation.Execution) isolation.ExecResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
	defer cancel()
	res, err := ex.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	return res
}

// drainWait drains Events and waits, failing if the execution never finishes.
func drainWait(t *testing.T, ex isolation.Execution) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		for range ex.Events() {
		}
		ctx, cancel := context.WithTimeout(context.Background(), waitTimeout)
		defer cancel()
		_, _ = ex.Wait(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(waitTimeout + 5*time.Second):
		t.Fatal("execution did not finish")
	}
}

// waitForFile polls Download until path appears, so a test can observe that a
// command actually started before stopping or deleting it.
func waitForFile(t *testing.T, be isolation.IsolationBackend, h isolation.Handle, path string) {
	t.Helper()
	deadline := time.Now().Add(waitTimeout)
	for {
		files, err := be.Download(context.Background(), h, []string{path})
		if err != nil {
			t.Fatalf("Download(%s) error = %v", path, err)
		}
		if len(files) > 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertUnsupported(t *testing.T, op string, err error) {
	t.Helper()
	if !errors.Is(err, isolation.ErrUnsupported) {
		t.Fatalf("%s() error = %v, want isolation.ErrUnsupported", op, err)
	}
}

// foreignHandle is a handle a backend did not create. A backend must reject it
// rather than treat it as one of its own.
type foreignHandle struct{}

func (foreignHandle) ID() string { return "foreign" }
