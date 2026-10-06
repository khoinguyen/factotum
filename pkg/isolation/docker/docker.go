// Package docker is the container isolation backend for `ft run`: it runs each
// task in its own short-lived Docker container, created from the harness's
// image and kept alive by a shell init, with the task's resolved workspace
// bind-mounted at the same absolute path inside the container.
//
// It is the second isolating backend behind the pkg/isolation port, a peer of
// OpenShell rather than a special case: the port speaks only generic specs,
// commands, and policies, and no Docker type escapes this package.
//
// Container definition. The environment is a pure function of the spec: the
// image (Spec.Image.Ref, else the backend default), the workspace mount, the
// non-root user, and the labels. There is no timestamp or random content in the
// definition, so the same task reproduces the same environment; only the
// container name is unique per run. The workspace is the single bind mount, at
// its own absolute path, so a command's workdir and absolute paths map through
// unchanged.
//
// Where the devcontainer is specified. The IsolationBackend Spec is the source
// of truth, produced by the harness and the launcher from the task (image,
// workdir, env, labels). A repo- or project-level devcontainer.json override is
// deliberately not read: the port is backend-agnostic, and a committed file
// would let a workspace widen its own definition. A project that needs a
// different base image sets it on the harness; per-repo overrides are a future
// extension.
//
// Limits, stated honestly. Docker's CLI cannot confine a filesystem or network
// after the fact, so Prepare and ApplyPolicy reject a non-empty Policy with
// isolation.ErrUnsupported rather than pretending. An interactive TTY is
// refused for the same reason. Credential values are never placed in container
// metadata or in argv: an attached credential is injected per-exec through the
// docker client's own environment (`docker exec --env KEY`), so it never
// appears in `docker inspect` or a host process list.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
)

const (
	// Name is the registration key of the docker backend.
	Name = "docker"
	// DefaultCLI is the docker executable looked up on PATH.
	DefaultCLI = "docker"
	// DefaultImage is the container image when a Spec names none. It matches
	// the OpenCode harness's shipped image, so a run with no explicit image is
	// still usable and the conformance suite can Prepare an empty spec.
	DefaultImage = "ghcr.io/anomalyco/opencode:latest"
	// KeepAlive is the container's init process. It keeps the container alive
	// between Exec calls; the harness itself runs through `docker exec`.
	KeepAlive = "while :; do sleep 3600; done"
	// DefaultReadyTimeout bounds how long Prepare waits for a container to
	// report that it is running.
	DefaultReadyTimeout = 30 * time.Second

	// readyPollInterval is how often Prepare re-checks that the container is up.
	readyPollInterval = 200 * time.Millisecond
	// eventBuffer bounds the streamed-event channel. Events are best-effort;
	// the complete output is always available from Execution.Wait.
	eventBuffer = 256

	scratchPrefix  = "ft-docker-ws-"
	uploadPrefix   = "ft-docker-upload-"
	downloadPrefix = "ft-docker-download-"
)

// CredentialResolver resolves a provider credential reference to its secret
// value. It is supplied by the launcher, so the backend never reads a secret
// from the host on its own.
type CredentialResolver interface {
	Resolve(ctx context.Context, c isolation.Credential) (string, error)
}

// StartInput is one streaming docker CLI invocation.
type StartInput struct {
	// Stdin is piped to the process.
	Stdin []byte
	// Env is set on the docker CLI process itself. It is how env values,
	// including credentials, reach the container without appearing in argv.
	Env map[string]string
}

// Process is a running docker CLI invocation.
type Process interface {
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	// Wait blocks until the process exits and returns its exit code. A normal
	// non-zero exit is returned as (code, nil); only a failure to run the
	// command yields a non-nil error.
	Wait() (int, error)
	Kill() error
}

// Runner runs the docker CLI. It is an interface so tests inject a fake and
// never touch a daemon.
type Runner interface {
	// Run executes a one-shot command and returns its stdout. The error carries
	// the command's stderr.
	Run(ctx context.Context, args []string, env map[string]string) ([]byte, error)
	// Start starts a streaming command.
	Start(ctx context.Context, args []string, in StartInput) (Process, error)
}

// Options configures the docker backend.
type Options struct {
	// CLI is the docker executable. Defaults to DefaultCLI.
	CLI string
	// Image is the base image when a Spec names none. Defaults to DefaultImage.
	Image string
	// Credentials resolves provider credential references. A nil resolver makes
	// AttachCredential return ErrUnsupported, keeping the backend from reaching
	// into the host environment on its own.
	Credentials CredentialResolver
	// Runner runs the CLI. Nil runs the real docker binary.
	Runner Runner
	// NewName returns a unique container name. Nil uses a random name.
	NewName func() string
	// ReadyTimeout bounds the wait for a new container to report running.
	ReadyTimeout time.Duration
}

// Backend is the docker implementation of isolation.IsolationBackend.
type Backend struct {
	opts Options
	run  Runner

	mu      sync.Mutex
	envs    map[string]*environment
	deleted map[string]struct{}
}

// New returns a docker backend with defaults applied.
func New(opts Options) *Backend {
	if opts.CLI == "" {
		opts.CLI = DefaultCLI
	}
	if opts.Image == "" {
		opts.Image = DefaultImage
	}
	if opts.NewName == nil {
		opts.NewName = randomName
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = DefaultReadyTimeout
	}
	b := &Backend{
		opts:    opts,
		run:     opts.Runner,
		envs:    map[string]*environment{},
		deleted: map[string]struct{}{},
	}
	if b.run == nil {
		b.run = execRunner{name: opts.CLI}
	}
	return b
}

func (b *Backend) Name() string { return Name }

// environment is the per-run container state.
type environment struct {
	id      string
	name    string
	workdir string
	owned   bool

	mu    sync.Mutex
	env   map[string]string
	procs map[*execution]*runningProcess
}

// runningProcess is a live command tracked so Stop can end it.
type runningProcess struct {
	proc   Process
	cancel context.CancelFunc
}

// handle is the opaque isolation.Handle for a prepared container.
type handle struct{ id string }

func (h *handle) ID() string { return h.id }

// Prepare creates a detached container with the workspace mounted, waits for it
// to be running, and stages the spec's files inside it.
func (b *Backend) Prepare(ctx context.Context, spec isolation.Spec) (isolation.Handle, error) {
	if !emptyPolicy(spec.Policy) {
		return nil, fmt.Errorf("%w: cannot enforce a filesystem or network policy with docker", isolation.ErrUnsupported)
	}
	workdir, owned, err := b.workspace(spec.Workdir)
	if err != nil {
		return nil, err
	}
	cleanup := func() {
		if owned {
			_ = os.RemoveAll(workdir)
		}
	}

	name := b.opts.NewName()
	env := &environment{
		id:      name,
		name:    name,
		workdir: workdir,
		owned:   owned,
		env:     clone(spec.Env),
		procs:   map[*execution]*runningProcess{},
	}
	for _, c := range spec.Credentials {
		if err := b.attach(ctx, env, c); err != nil {
			cleanup()
			return nil, err
		}
	}

	image := firstNonEmpty(spec.Image.Ref, b.opts.Image)
	if _, err := b.run.Run(ctx, runArgs(name, spec, workdir, image), nil); err != nil {
		cleanup()
		return nil, fmt.Errorf("isolation/docker: create container %q: %w", name, err)
	}
	if err := b.waitRunning(ctx, name); err != nil {
		b.removeContainer(ctx, name)
		cleanup()
		return nil, err
	}
	if err := b.uploadFiles(ctx, env, spec.Files); err != nil {
		b.removeContainer(ctx, name)
		cleanup()
		return nil, err
	}

	b.mu.Lock()
	b.envs[name] = env
	b.mu.Unlock()
	return &handle{id: name}, nil
}

// runArgs is the `docker run` invocation: a detached container with a shell
// keep-alive init, the workspace as the only bind mount at its own absolute
// path, the spec's non-root user and labels, all before the image and command.
// It is a pure function of its inputs so a task reproduces the same definition.
func runArgs(name string, spec isolation.Spec, workdir, image string) []string {
	args := []string{
		"run",
		"--detach",
		"--name", name,
		"--workdir", workdir,
		"--mount", fmt.Sprintf("type=bind,source=%s,target=%s", workdir, workdir),
	}
	if spec.Image.User != "" {
		args = append(args, "--user", spec.Image.User)
	}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	args = append(args, "--entrypoint", "sh", image, "-c", KeepAlive)
	return args
}

// execArgs is one `docker exec` invocation. Environment keys are injected
// without values (`--env KEY`); the values travel in the docker client's
// environment, so no secret is ever in argv.
func execArgs(name, workdir string, env map[string]string, argv []string, interactive bool) []string {
	args := []string{"exec"}
	if interactive {
		args = append(args, "--interactive")
	}
	if workdir != "" {
		args = append(args, "--workdir", workdir)
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "--env", k)
	}
	args = append(args, name)
	args = append(args, argv...)
	return args
}

// Exec starts cmd in the container and streams its output.
func (b *Backend) Exec(ctx context.Context, h isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	if len(cmd.Argv) == 0 {
		return nil, errors.New("isolation/docker: empty argv")
	}
	if cmd.TTY {
		return nil, fmt.Errorf("%w: interactive tty over the docker CLI", isolation.ErrUnsupported)
	}

	var runCtx context.Context
	var cancel context.CancelFunc
	if cmd.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cmd.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}

	effective := mergeEnv(env.snapshot(), cmd.Env)
	args := execArgs(env.name, cmd.Workdir, effective, cmd.Argv, cmd.Stdin != nil)
	proc, err := b.run.Start(runCtx, args, StartInput{Stdin: cmd.Stdin, Env: effective})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("isolation/docker: exec: %w", err)
	}

	ex := newExecution()
	env.track(ex, &runningProcess{proc: proc, cancel: cancel})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ex.consume(proc.Stdout(), isolation.StreamStdout) }()
	go func() { defer wg.Done(); ex.consume(proc.Stderr(), isolation.StreamStderr) }()
	go func() {
		code, waitErr := proc.Wait()
		wg.Wait()
		env.untrack(ex)
		cancel()
		ex.finish(code, waitErr)
	}()
	return ex, nil
}

// Upload copies files into the container, creating parent directories.
func (b *Backend) Upload(ctx context.Context, h isolation.Handle, files []isolation.File) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	return b.uploadFiles(ctx, env, files)
}

// uploadFiles writes each file to a host staging directory and copies it into
// the container with `docker cp`.
func (b *Backend) uploadFiles(ctx context.Context, env *environment, files []isolation.File) error {
	if len(files) == 0 {
		return nil
	}
	stage, err := os.MkdirTemp("", uploadPrefix)
	if err != nil {
		return fmt.Errorf("isolation/docker: stage files: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	for _, f := range files {
		guest := resolvePath(env.workdir, f.Path)
		if err := b.mkdir(ctx, env.name, path.Dir(guest)); err != nil {
			return err
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		local := filepath.Join(stage, filepath.Base(guest))
		if err := os.WriteFile(local, f.Content, mode); err != nil {
			return fmt.Errorf("isolation/docker: stage %q: %w", f.Path, err)
		}
		if _, err := b.run.Run(ctx, []string{"cp", local, env.name + ":" + guest}, nil); err != nil {
			return fmt.Errorf("isolation/docker: upload %q: %w", f.Path, err)
		}
	}
	return nil
}

// mkdir creates a directory inside the container.
func (b *Backend) mkdir(ctx context.Context, name, dir string) error {
	args := execArgs(name, "", nil, []string{"sh", "-c", `mkdir -p "$1"`, "--", dir}, false)
	if _, err := b.run.Run(ctx, args, nil); err != nil {
		return fmt.Errorf("isolation/docker: mkdir %q: %w", dir, err)
	}
	return nil
}

// Download copies the requested paths out of the container. Missing paths are
// omitted rather than an error.
func (b *Backend) Download(ctx context.Context, h isolation.Handle, paths []string) ([]isolation.File, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	var out []isolation.File
	for _, p := range paths {
		files, err := b.downloadOne(ctx, env, p, resolvePath(env.workdir, p))
		if err != nil {
			continue // a missing path is omitted, not an error
		}
		out = append(out, files...)
	}
	return out, nil
}

// downloadOne copies one path into a staging directory and walks it back into
// the caller's requested path space.
func (b *Backend) downloadOne(ctx context.Context, env *environment, display, guest string) ([]isolation.File, error) {
	stage, err := os.MkdirTemp("", downloadPrefix)
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if _, err := b.run.Run(ctx, []string{"cp", env.name + ":" + guest, stage}, nil); err != nil {
		return nil, err
	}

	base := filepath.Join(stage, path.Base(guest))
	var files []isolation.File
	err = filepath.WalkDir(stage, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(base, p)
		if err != nil {
			return err
		}
		name := display
		if rel != "." {
			name = path.Join(display, filepath.ToSlash(rel))
		}
		files = append(files, isolation.File{Path: name, Content: data, Mode: info.Mode()})
		return nil
	})
	return files, err
}

// Logs streams the container's own log lines. Follow streams until the context
// is cancelled; otherwise it returns the recent tail.
func (b *Backend) Logs(ctx context.Context, h isolation.Handle, opts isolation.LogOptions) (<-chan isolation.Event, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	events := make(chan isolation.Event, eventBuffer)
	if opts.Follow {
		proc, err := b.run.Start(ctx, []string{"logs", "--follow", env.name}, StartInput{})
		if err != nil {
			return nil, fmt.Errorf("isolation/docker: logs: %w", err)
		}
		go streamLines(proc.Stdout(), events)
		return events, nil
	}
	out, err := b.run.Run(ctx, []string{"logs", env.name}, nil)
	if err != nil {
		return nil, fmt.Errorf("isolation/docker: logs: %w", err)
	}
	go func() {
		defer close(events)
		for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
			if line == "" {
				continue
			}
			events <- isolation.Event{Time: time.Now(), Kind: isolation.EventOutput, Message: line}
		}
	}()
	return events, nil
}

// Stop cancels the run's live commands and is idempotent. It keeps the
// container so the handle stays usable.
func (b *Backend) Stop(_ context.Context, h isolation.Handle) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	env.stop()
	return nil
}

// Delete force-removes the container and any owned workspace. It is idempotent:
// deleting an already-deleted handle returns nil, so a deferred teardown never
// races a retry. A foreign or never-prepared handle is still an error.
func (b *Backend) Delete(ctx context.Context, h isolation.Handle) error {
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
	b.mu.Unlock()
	if !ok {
		return fmt.Errorf("isolation/docker: unknown handle %q", id)
	}

	env.stop()
	if _, err := b.run.Run(ctx, []string{"rm", "-f", env.name}, nil); err != nil {
		return fmt.Errorf("isolation/docker: remove container %q: %w", env.name, err)
	}
	if env.owned {
		if err := os.RemoveAll(env.workdir); err != nil {
			return fmt.Errorf("isolation/docker: remove workspace: %w", err)
		}
	}

	b.mu.Lock()
	delete(b.envs, id)
	b.deleted[id] = struct{}{}
	b.mu.Unlock()
	return nil
}

// removeContainer best-effort removes a container after a failed Prepare. The
// context is detached so a cancelled Prepare still cleans up.
func (b *Backend) removeContainer(ctx context.Context, name string) {
	_, _ = b.run.Run(context.WithoutCancel(ctx), []string{"rm", "-f", name}, nil)
}

// ApplyPolicy accepts an empty policy (nothing to enforce) and returns
// ErrUnsupported otherwise: the docker CLI cannot confine a running container.
func (b *Backend) ApplyPolicy(_ context.Context, h isolation.Handle, p isolation.Policy) error {
	if _, err := b.lookup(h); err != nil {
		return err
	}
	if !emptyPolicy(p) {
		return fmt.Errorf("%w: cannot enforce a filesystem or network policy with docker", isolation.ErrUnsupported)
	}
	return nil
}

// AttachCredential resolves c through the configured resolver and makes the
// value available to later execs through the docker client's environment.
// Without a resolver the backend refuses (ErrUnsupported) rather than reading
// the value from the host.
func (b *Backend) AttachCredential(ctx context.Context, h isolation.Handle, c isolation.Credential) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	return b.attach(ctx, env, c)
}

// attach is the shared credential path for Prepare (Spec.Credentials) and
// AttachCredential: resolve the provider reference and store the value under
// the credential's EnvVar, or refuse when no resolver is configured.
func (b *Backend) attach(ctx context.Context, env *environment, c isolation.Credential) error {
	if c.EnvVar == "" {
		return errors.New("isolation/docker: credential has no EnvVar")
	}
	if b.opts.Credentials == nil {
		return fmt.Errorf("%w: no credential resolver configured", isolation.ErrUnsupported)
	}
	value, err := b.opts.Credentials.Resolve(ctx, c)
	if err != nil {
		return fmt.Errorf("isolation/docker: resolve credential %q: %w", c.Provider, err)
	}
	env.set(c.EnvVar, value)
	return nil
}

// workspace resolves the host directory to mount. An empty workdir gets a
// fresh, owned scratch directory; a relative workdir is rejected; an existing
// workdir is created if needed and never owned (it is the caller's).
func (b *Backend) workspace(workdir string) (string, bool, error) {
	switch {
	case workdir == "":
		dir, err := os.MkdirTemp("", scratchPrefix)
		if err != nil {
			return "", false, fmt.Errorf("isolation/docker: create workspace: %w", err)
		}
		return dir, true, nil
	case !filepath.IsAbs(workdir):
		return "", false, fmt.Errorf("isolation/docker: workdir %q must be absolute", workdir)
	default:
		if err := os.MkdirAll(workdir, 0o755); err != nil {
			return "", false, fmt.Errorf("isolation/docker: create workdir: %w", err)
		}
		return workdir, false, nil
	}
}

// waitRunning polls the container until it reports running or the deadline
// passes, so a container that dies immediately fails Prepare.
func (b *Backend) waitRunning(ctx context.Context, name string) error {
	deadline, cancel := context.WithTimeout(ctx, b.opts.ReadyTimeout)
	defer cancel()
	for {
		if out, err := b.run.Run(deadline, []string{"inspect", "--format", "{{.State.Running}}", name}, nil); err == nil {
			if strings.TrimSpace(string(out)) == "true" {
				return nil
			}
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("isolation/docker: container %q did not stay running: %w", name, deadline.Err())
		case <-time.After(readyPollInterval):
		}
	}
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
		return nil, fmt.Errorf("isolation/docker: unknown handle %q", id)
	}
	return env, nil
}

// handleID validates that h is a handle this backend created and returns its
// id. A nil or foreign handle is an error, never a panic.
func handleID(h isolation.Handle) (string, error) {
	if h == nil {
		return "", errors.New("isolation/docker: nil handle")
	}
	hh, ok := h.(*handle)
	if !ok {
		return "", fmt.Errorf("isolation/docker: foreign handle %q", h.ID())
	}
	return hh.id, nil
}

// resolvePath roots a relative path at the workspace; an absolute path is used
// as given (it is already a container path).
func resolvePath(workdir, p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(workdir, p)
}

// mergeEnv layers environment maps, later values winning.
func mergeEnv(layers ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, layer := range layers {
		for k, v := range layer {
			out[k] = v
		}
	}
	return out
}

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

func (e *environment) track(ex *execution, rp *runningProcess) {
	e.mu.Lock()
	e.procs[ex] = rp
	e.mu.Unlock()
}

func (e *environment) untrack(ex *execution) {
	e.mu.Lock()
	delete(e.procs, ex)
	e.mu.Unlock()
}

// stop ends every live command, keeping the container itself alive.
func (e *environment) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rp := range e.procs {
		rp.cancel()
		_ = rp.proc.Kill()
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
	if r == nil {
		return
	}
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

func (e *execution) finish(code int, waitErr error) {
	e.mu.Lock()
	e.result = isolation.ExecResult{
		Stdout:   append([]byte(nil), e.stdout.Bytes()...),
		Stderr:   append([]byte(nil), e.stderr.Bytes()...),
		ExitCode: code,
	}
	e.err = waitErr
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

// streamLines emits each line read from r as an output event and closes events.
func streamLines(r io.Reader, events chan isolation.Event) {
	defer close(events)
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		if line := scanner.Text(); line != "" {
			events <- isolation.Event{Time: time.Now(), Kind: isolation.EventOutput, Message: line}
		}
	}
}

// execRunner runs the real docker binary.
type execRunner struct{ name string }

func (e execRunner) Run(ctx context.Context, args []string, env map[string]string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.name, args...)
	cmd.Env = envSlice(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("docker %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func (e execRunner) Start(ctx context.Context, args []string, in StartInput) (Process, error) {
	cmd := exec.CommandContext(ctx, e.name, args...)
	cmd.Env = envSlice(in.Env)
	if in.Stdin != nil {
		cmd.Stdin = bytes.NewReader(in.Stdin)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd}
	p.outR, p.outW = io.Pipe()
	p.errR, p.errW = io.Pipe()
	p.wg.Add(2)
	go func() { defer p.wg.Done(); _, _ = io.Copy(p.outW, stdout); _ = p.outW.Close() }()
	go func() { defer p.wg.Done(); _, _ = io.Copy(p.errW, stderr); _ = p.errW.Close() }()
	return p, nil
}

// execProcess is a real running CLI process. Standard output is copied through
// io.Pipe so Wait's cmd.Wait cannot close a pipe the caller is mid-read on.
type execProcess struct {
	cmd  *exec.Cmd
	outR *io.PipeReader
	outW *io.PipeWriter
	errR *io.PipeReader
	errW *io.PipeWriter
	wg   sync.WaitGroup
}

func (p *execProcess) Stdout() io.ReadCloser { return p.outR }
func (p *execProcess) Stderr() io.ReadCloser { return p.errR }

func (p *execProcess) Wait() (int, error) {
	p.wg.Wait()
	err := p.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), nil
	}
	return 0, err
}

func (p *execProcess) Kill() error {
	if p.cmd.Process == nil {
		return nil
	}
	return p.cmd.Process.Kill()
}

// envSlice layers overrides over the current environment, so the docker CLI
// keeps the variables it needs and env values (including credentials) reach it
// without ever appearing in argv. An override replaces any same-named host
// variable, so a real value already in the environment can never shadow the
// intended one.
func envSlice(env map[string]string) []string {
	values := map[string]string{}
	order := make([]string, 0, len(env))
	for _, kv := range os.Environ() {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			continue
		}
		key := kv[:eq]
		if _, seen := values[key]; !seen {
			order = append(order, key)
		}
		values[key] = kv[eq+1:]
	}
	for _, k := range sortedKeys(env) {
		if _, seen := values[k]; !seen {
			order = append(order, k)
		}
		values[k] = env[k]
	}
	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+values[k])
	}
	return out
}

// randomName returns a docker-safe unique container name.
func randomName() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ftd%d", time.Now().UnixNano()%1_000_000_000)
	}
	return "ftd" + hex.EncodeToString(b[:])
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// emptyPolicy reports whether p asks a backend to enforce nothing.
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
