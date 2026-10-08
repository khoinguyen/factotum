// Package local is the host isolation backend for `ft run`: it executes the
// harness directly on the machine, with no container, virtual machine, or
// sandbox of any kind.
//
// It is a development and reference backend, not a security boundary. A run on
// this backend can read the host's credentials, reach the network, and mutate
// any file the user can. It is therefore:
//
//   - explicitly opt-in: Prepare refuses with ErrNotOptedIn unless Options.AllowHost
//     is set, so it can never be selected (or run) by default;
//   - loud: a successful Prepare writes an unsandboxed warning naming the risk;
//   - honest about its limits: it cannot enforce a filesystem, network, or
//     identity Policy and returns isolation.ErrUnsupported rather than pretending
//     to, and it has no separate environment log stream.
//
// Model credentials are never read from the host by this backend, and the run
// does not inherit the full host environment: only a small allowlist of
// non-secret variables (PATH, HOME, locale, proxy) is passed through. A caller
// attaches a Credential naming a provider and reference, and the configured
// CredentialResolver resolves it through the provider mechanism; with no
// resolver (or an unsupported provider) AttachCredential and Spec.Credentials
// return ErrUnsupported rather than reaching into the host.
//
// The port (pkg/isolation) assumes nothing about sandboxing, so this backend is
// an ordinary implementation of it: the isolating OpenShell backend is a peer
// registration, not a special case here.
package local

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

// Name is the registration key of the host backend.
const Name = "local"

// ErrNotOptedIn means a caller tried to Prepare without Options.AllowHost. It is
// returned instead of running anything, so the unsandboxed backend is never a
// silent default.
var ErrNotOptedIn = errors.New("isolation/local: host execution is not opted in")

// eventBuffer bounds the streamed-event channel. Events are best-effort: a
// consumer that stalls never blocks the process, and the complete output is
// always available from Execution.Wait. 256 is generous for interactive
// tailing while keeping a detached run bounded.
const eventBuffer = 256

// deletedMax bounds the deleted-handle tombstones the backend keeps so Delete
// stays idempotent. Handle ids are unique and never reused, so only recently
// deleted handles need remembering; the cap keeps a long-lived backend (a serve
// loop, an agent fleet) from growing without limit. Once the set is full the
// oldest half is evicted, which still covers a caller's immediate double-delete.
const deletedMax = 1024

// Options configures the local backend.
type Options struct {
	// AllowHost is the explicit opt-in. Prepare refuses unless it is true.
	AllowHost bool
	// Warn receives the unsandboxed notice. A nil value writes to os.Stderr.
	Warn io.Writer
	// Credentials resolves provider credential references to secret values.
	// A nil resolver makes AttachCredential return ErrUnsupported, keeping the
	// backend from reaching into the host environment on its own.
	Credentials CredentialResolver
	// Stdin, Stdout, and Stderr are the terminal streams an interactive (TTY)
	// command is attached to. A nil value uses the process's own os.Stdin,
	// os.Stdout, or os.Stderr; tests inject buffers to observe the attachment.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// CredentialResolver resolves a provider credential reference to its secret
// value. It is supplied by the caller (the launcher wiring), so the local
// backend never reads a host secret directly and credentials can be routed
// through whatever provider mechanism the run configured.
type CredentialResolver interface {
	Resolve(ctx context.Context, c isolation.Credential) (string, error)
}

// Backend is the no-isolation host IsolationBackend.
type Backend struct {
	allowHost bool
	warn      io.Writer
	creds     CredentialResolver
	stdin     io.Reader
	stdout    io.Writer
	stderr    io.Writer
	seq       atomic.Int64

	mu      sync.Mutex
	envs    map[string]*environment
	deleted map[string]struct{}
	// deletedOrder records tombstone insertion order so the oldest can be
	// evicted once deleted reaches deletedMax. Guarded by mu.
	deletedOrder []string
}

// New returns a local backend. It is inert until Prepare is called with
// AllowHost set.
func New(opts Options) *Backend {
	return &Backend{
		allowHost: opts.AllowHost,
		warn:      opts.Warn,
		creds:     opts.Credentials,
		stdin:     opts.Stdin,
		stdout:    opts.Stdout,
		stderr:    opts.Stderr,
		envs:      map[string]*environment{},
		deleted:   map[string]struct{}{},
	}
}

// terminalStdin, terminalStdout, and terminalStderr return the streams an
// interactive command is attached to, defaulting to the process's own.
func (b *Backend) terminalStdin() io.Reader {
	if b.stdin != nil {
		return b.stdin
	}
	return os.Stdin
}

func (b *Backend) terminalStdout() io.Writer {
	if b.stdout != nil {
		return b.stdout
	}
	return os.Stdout
}

func (b *Backend) terminalStderr() io.Writer {
	if b.stderr != nil {
		return b.stderr
	}
	return os.Stderr
}

// environment is the per-run host state: a workspace root, the accumulated
// environment (including resolved credentials), and the live processes so Stop
// and Delete can end them.
type environment struct {
	id    string
	root  string
	owned bool
	mu    sync.Mutex
	env   map[string]string
	procs map[*process]struct{}
}

// process is one running command, tracked so Stop can cancel it.
type process struct {
	cancel context.CancelFunc
}

// handle is the opaque isolation.Handle for a prepared local environment.
type handle struct{ id string }

func (h *handle) ID() string { return h.id }

func (b *Backend) Name() string { return Name }

func (b *Backend) Prepare(ctx context.Context, spec isolation.Spec) (isolation.Handle, error) {
	if !b.allowHost {
		return nil, ErrNotOptedIn
	}
	if !emptyPolicy(spec.Policy) {
		return nil, fmt.Errorf("%w: cannot enforce a policy on the host", isolation.ErrUnsupported)
	}
	b.warnUnsandboxed()

	root, owned := spec.Workdir, false
	switch {
	case root == "":
		dir, err := os.MkdirTemp("", "ft-local-")
		if err != nil {
			return nil, fmt.Errorf("isolation/local: create workspace: %w", err)
		}
		root, owned = dir, true
	case !filepath.IsAbs(root):
		return nil, fmt.Errorf("isolation/local: workdir %q must be absolute", root)
	default:
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, fmt.Errorf("isolation/local: create workdir: %w", err)
		}
	}

	cleanup := func() {
		if owned {
			_ = os.RemoveAll(root)
		}
	}
	env := &environment{
		id:    fmt.Sprintf("%s-%d", Name, b.seq.Add(1)),
		root:  root,
		owned: owned,
		env:   clone(spec.Env),
		procs: map[*process]struct{}{},
	}
	for _, f := range spec.Files {
		if err := writeFile(root, f); err != nil {
			cleanup()
			return nil, err
		}
	}
	// The Spec's credentials apply to the whole run; honor them here exactly as
	// AttachCredential would, rather than silently dropping them.
	for _, c := range spec.Credentials {
		if err := b.attach(ctx, env, c); err != nil {
			cleanup()
			return nil, err
		}
	}

	b.mu.Lock()
	b.envs[env.id] = env
	b.mu.Unlock()
	return &handle{id: env.id}, nil
}

func (b *Backend) Exec(ctx context.Context, h isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	if len(cmd.Argv) == 0 {
		return nil, errors.New("isolation/local: empty argv")
	}

	var (
		runCtx context.Context
		cancel context.CancelFunc
	)
	if cmd.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cmd.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}

	var exited atomic.Bool
	c := exec.CommandContext(runCtx, cmd.Argv[0], cmd.Argv[1:]...)
	c.Dir = firstNonEmpty(cmd.Workdir, env.root)
	c.Env = mergeEnv(baseEnv(), env.snapshot(), cmd.Env)

	if cmd.TTY {
		return b.execAttached(c, env, cmd, cancel)
	}

	configureProcessGroup(c)
	c.Cancel = func() error {
		if exited.Load() {
			return nil
		}
		return killProcessGroup(c)
	}
	if cmd.Stdin != nil {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	}

	stdout, err := c.StdoutPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("isolation/local: stdout pipe: %w", err)
	}
	stderr, err := c.StderrPipe()
	if err != nil {
		cancel()
		return nil, fmt.Errorf("isolation/local: stderr pipe: %w", err)
	}

	ex := newExecution()
	if err := c.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("isolation/local: start %q: %w", cmd.Argv[0], err)
	}

	proc := &process{cancel: cancel}
	env.track(proc)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ex.consume(stdout, isolation.StreamStdout) }()
	go func() { defer wg.Done(); ex.consume(stderr, isolation.StreamStderr) }()
	go func() {
		waitErr := c.Wait()
		exited.Store(true)
		wg.Wait()
		env.untrack(proc)
		cancel()
		ex.finish(c.ProcessState, waitErr)
	}()
	return ex, nil
}

// execAttached runs an interactive command with the host terminal attached, so a
// TUI agent can render and read input. The child keeps the caller's process
// group: the terminal's foreground group owns job control, so signals like
// Ctrl-C reach the agent. Its output is shown live rather than captured through
// a pipe; a session that must leave a record writes it inside the environment.
func (b *Backend) execAttached(c *exec.Cmd, env *environment, cmd isolation.Command, cancel context.CancelFunc) (isolation.Execution, error) {
	if cmd.Stdin != nil {
		c.Stdin = bytes.NewReader(cmd.Stdin)
	} else {
		c.Stdin = b.terminalStdin()
	}
	c.Stdout = b.terminalStdout()
	c.Stderr = b.terminalStderr()

	ex := newExecution()
	if err := c.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("isolation/local: start %q: %w", cmd.Argv[0], err)
	}

	proc := &process{cancel: cancel}
	env.track(proc)
	go func() {
		waitErr := c.Wait()
		env.untrack(proc)
		cancel()
		ex.finish(c.ProcessState, waitErr)
	}()
	return ex, nil
}

func (b *Backend) Upload(_ context.Context, h isolation.Handle, files []isolation.File) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := writeFile(env.root, f); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) Download(_ context.Context, h isolation.Handle, paths []string) ([]isolation.File, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	out := make([]isolation.File, 0, len(paths))
	for _, p := range paths {
		full := resolvePath(env.root, p)
		info, err := os.Stat(full)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("isolation/local: stat %q: %w", p, err)
		}
		if info.IsDir() {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return nil, fmt.Errorf("isolation/local: read %q: %w", p, err)
		}
		out = append(out, isolation.File{Path: p, Content: data, Mode: info.Mode()})
	}
	return out, nil
}

// Logs is unsupported: the host has no separate environment-level event stream.
// Process output is streamed by Exec and returned by Execution.Wait.
func (b *Backend) Logs(_ context.Context, h isolation.Handle, _ isolation.LogOptions) (<-chan isolation.Event, error) {
	if _, err := b.lookup(h); err != nil {
		return nil, err
	}
	return nil, fmt.Errorf("%w: no environment log stream on the host", isolation.ErrUnsupported)
}

func (b *Backend) Stop(_ context.Context, h isolation.Handle) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	env.stop()
	return nil
}

// Delete stops and removes the environment. It is idempotent: deleting an
// already-deleted handle is a no-op, so a caller's deferred teardown never
// races a retry. A foreign or never-prepared handle is still an error.
func (b *Backend) Delete(_ context.Context, h isolation.Handle) error {
	id, err := handleID(h)
	if err != nil {
		return err
	}

	b.mu.Lock()
	if _, gone := b.deleted[id]; gone {
		b.mu.Unlock()
		return nil
	}
	env, ok := b.envs[id]
	if !ok {
		b.mu.Unlock()
		return fmt.Errorf("isolation/local: unknown handle %q", id)
	}
	delete(b.envs, id)
	b.rememberDeleted(id)
	b.mu.Unlock()

	env.stop()
	if env.owned {
		if err := os.RemoveAll(env.root); err != nil {
			return fmt.Errorf("isolation/local: remove environment: %w", err)
		}
	}
	return nil
}

// rememberDeleted records id as a deleted handle for idempotent Delete,
// evicting the oldest tombstones once the set is full. Callers must hold b.mu.
func (b *Backend) rememberDeleted(id string) {
	if len(b.deletedOrder) >= deletedMax {
		half := len(b.deletedOrder) / 2
		for _, gone := range b.deletedOrder[:half] {
			delete(b.deleted, gone)
		}
		n := copy(b.deletedOrder, b.deletedOrder[half:])
		b.deletedOrder = b.deletedOrder[:n]
	}
	b.deleted[id] = struct{}{}
	b.deletedOrder = append(b.deletedOrder, id)
}

// ApplyPolicy accepts an empty policy (nothing to enforce) and returns
// ErrUnsupported otherwise: the host has no mechanism to confine the run.
func (b *Backend) ApplyPolicy(_ context.Context, h isolation.Handle, p isolation.Policy) error {
	if _, err := b.lookup(h); err != nil {
		return err
	}
	if !emptyPolicy(p) {
		return fmt.Errorf("%w: cannot enforce a policy on the host", isolation.ErrUnsupported)
	}
	return nil
}

// AttachCredential resolves c through the configured resolver and makes the value
// available to the run's environment. Without a resolver the backend refuses
// (ErrUnsupported) rather than reading the value from the host.
func (b *Backend) AttachCredential(ctx context.Context, h isolation.Handle, c isolation.Credential) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	return b.attach(ctx, env, c)
}

// attach is the shared credential path for Prepare (Spec.Credentials) and
// AttachCredential: resolve the provider reference and inject the value under
// the credential's EnvVar, or refuse when no resolver is configured.
func (b *Backend) attach(ctx context.Context, env *environment, c isolation.Credential) error {
	if c.EnvVar == "" {
		return errors.New("isolation/local: credential has no EnvVar")
	}
	if b.creds == nil {
		return fmt.Errorf("%w: no credential resolver configured", isolation.ErrUnsupported)
	}
	value, err := b.creds.Resolve(ctx, c)
	if err != nil {
		return fmt.Errorf("isolation/local: resolve credential %q: %w", c.Provider, err)
	}
	env.set(c.EnvVar, value)
	return nil
}

func (b *Backend) lookup(h isolation.Handle) (*environment, error) {
	id, err := handleID(h)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	env, ok := b.envs[id]
	if !ok {
		return nil, fmt.Errorf("isolation/local: unknown handle %q", id)
	}
	return env, nil
}

// handleID validates that h is a handle this backend created and returns its
// id. A nil or foreign handle is an error, never a panic.
func handleID(h isolation.Handle) (string, error) {
	if h == nil {
		return "", errors.New("isolation/local: nil handle")
	}
	hh, ok := h.(*handle)
	if !ok {
		return "", fmt.Errorf("isolation/local: foreign handle %q", h.ID())
	}
	return hh.id, nil
}

func (b *Backend) warnUnsandboxed() {
	w := b.warn
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, "ft: warning: isolation backend %q is UNSANDBOXED: it runs the harness "+
		"directly on the host with NO isolation; it can read host credentials, reach the network, "+
		"and mutate files. Dev-only and explicitly opt-in; never use it for untrusted work.\n", Name)
}

// snapshot returns a copy of the accumulated environment.
func (e *environment) snapshot() map[string]string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make(map[string]string, len(e.env))
	for k, v := range e.env {
		out[k] = v
	}
	return out
}

func (e *environment) set(key, value string) {
	e.mu.Lock()
	e.env[key] = value
	e.mu.Unlock()
}

func (e *environment) track(p *process) {
	e.mu.Lock()
	e.procs[p] = struct{}{}
	e.mu.Unlock()
}

func (e *environment) untrack(p *process) {
	e.mu.Lock()
	delete(e.procs, p)
	e.mu.Unlock()
}

func (e *environment) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for p := range e.procs {
		p.cancel()
	}
}

// execution is one running command: it streams output events and, independently,
// buffers the full output for Wait. Events are best-effort, so Wait is safe
// without draining Events and a stalled consumer never blocks the process.
type execution struct {
	events chan isolation.Event
	done   chan struct{}

	mu     sync.Mutex
	stdout bytes.Buffer
	stderr bytes.Buffer
	result isolation.ExecResult
	err    error
}

func newExecution() *execution {
	return &execution{events: make(chan isolation.Event, eventBuffer), done: make(chan struct{})}
}

func (e *execution) Events() <-chan isolation.Event { return e.events }

func (e *execution) consume(r io.Reader, stream isolation.Stream) {
	buf := make([]byte, 4096)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			e.record(stream, chunk)
			e.emit(isolation.Event{Time: time.Now(), Kind: isolation.EventOutput, Stream: stream, Message: string(chunk)})
		}
		if err != nil {
			return
		}
	}
}

func (e *execution) record(stream isolation.Stream, b []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if stream == isolation.StreamStdout {
		e.stdout.Write(b)
		return
	}
	e.stderr.Write(b)
}

func (e *execution) emit(ev isolation.Event) {
	select {
	case e.events <- ev:
	default:
	}
}

func (e *execution) finish(state *os.ProcessState, waitErr error) {
	e.mu.Lock()
	e.result = isolation.ExecResult{
		Stdout:   append([]byte(nil), e.stdout.Bytes()...),
		Stderr:   append([]byte(nil), e.stderr.Bytes()...),
		ExitCode: exitCode(state, waitErr),
	}
	if waitErr != nil && !isExitError(waitErr) {
		e.err = waitErr
	}
	e.mu.Unlock()
	close(e.events)
	close(e.done)
}

func (e *execution) Wait(ctx context.Context) (isolation.ExecResult, error) {
	select {
	case <-e.done:
		e.mu.Lock()
		defer e.mu.Unlock()
		return e.result, e.err
	case <-ctx.Done():
		return isolation.ExecResult{}, ctx.Err()
	}
}

func exitCode(state *os.ProcessState, err error) int {
	if state != nil {
		return state.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

func isExitError(err error) bool {
	var ee *exec.ExitError
	return errors.As(err, &ee)
}

// writeFile places f in the environment: a relative Path is rooted at root, an
// absolute Path is used as-is. Parent directories are created.
func writeFile(root string, f isolation.File) error {
	full := resolvePath(root, f.Path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return fmt.Errorf("isolation/local: make dir for %q: %w", f.Path, err)
	}
	mode := f.Mode
	if mode == 0 {
		mode = 0o644
	}
	if err := os.WriteFile(full, f.Content, mode); err != nil {
		return fmt.Errorf("isolation/local: write %q: %w", f.Path, err)
	}
	return nil
}

func resolvePath(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, path)
}

// baseEnv is the minimal host environment a local run inherits. The full host
// environment is deliberately not passed through: it routinely carries model API
// keys and other secrets, which must instead arrive as an attached Credential.
// Only the variables a CLI needs to run are allowed.
func baseEnv() []string {
	allowed := []string{
		"PATH", "HOME", "TMPDIR", "TMP", "TEMP",
		"LANG", "LC_ALL", "LC_CTYPE", "TERM", "USER", "SHELL",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "no_proxy", "all_proxy",
	}
	out := make([]string, 0, len(allowed))
	for _, key := range allowed {
		if value := os.Getenv(key); value != "" {
			out = append(out, key+"="+value)
		}
	}
	return out
}

// mergeEnv layers environment maps over base, later values winning, and returns
// a deterministic slice safe to pass to exec.Cmd.Env.
func mergeEnv(base []string, layers ...map[string]string) []string {
	index := make(map[string]int, len(base))
	out := make([]string, 0, len(base))
	add := func(kv string) {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			out = append(out, kv)
			return
		}
		key := kv[:eq]
		if i, ok := index[key]; ok {
			out[i] = kv
			return
		}
		index[key] = len(out)
		out = append(out, kv)
	}
	for _, kv := range base {
		add(kv)
	}
	for _, layer := range layers {
		keys := make([]string, 0, len(layer))
		for k := range layer {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			add(k + "=" + layer[k])
		}
	}
	return out
}

func emptyPolicy(p isolation.Policy) bool {
	return len(p.ReadOnly) == 0 &&
		len(p.ReadWrite) == 0 &&
		len(p.AllowHosts) == 0 &&
		!p.DefaultDeny &&
		p.RunAsUser == "" &&
		p.RunAsGroup == "" &&
		len(p.Raw) == 0
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func clone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

var _ isolation.IsolationBackend = (*Backend)(nil)
