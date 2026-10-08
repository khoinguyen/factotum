// This file implements the IsolationBackend port on top of the NVIDIA OpenShell
// CLI. It shells out to `openshell` (the decision recorded in the spike,
// t-k3duthkrxo): the Go SDK has no file transport and refuses the local
// gateway's mTLS, while the CLI moves a workspace and streams results.
//
// The backend is deliberately small and gateway-shaped:
//
//   - Prepare creates a detached sandbox from the spec image with the hardened
//     policy in policy.go (deny-by-default egress, read-only system paths,
//     non-root), uploads a host workspace, and stages the spec's files.
//   - Exec runs a command with `sandbox exec`, streaming stdout and stderr.
//   - Upload uses `sandbox upload` (the only working bulk path); Download
//     streams file contents out with `exec` + base64, because the CLI's
//     `sandbox download` is broken against BusyBox images.
//   - Stop cancels the run's live commands; Delete tears the sandbox and every
//     provider it created down, retrying the asynchronous provider cleanup so
//     no credential material leaks into gateway state.
//   - A custom Spec.Image.Entrypoint is rejected with ErrUnsupported: the
//     sandbox runs the backend's own keep-alive init and the workload is
//     started with `sandbox exec`.
//
// No OpenShell type escapes this package: the port (pkg/isolation) speaks only
// generic specs, commands, and policies.
package openshell

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// Name is the registration key of the OpenShell backend.
	Name = "openshell"
	// DefaultCLI is the openshell executable looked up on PATH.
	DefaultCLI = "openshell"
	// DefaultImage is the sandbox image when a Spec names none. It matches the
	// OpenCode harness's shipped image, so the conformance suite can Prepare an
	// empty spec.
	DefaultImage = "ghcr.io/anomalyco/opencode:latest"
	// DefaultWorkdir is the in-sandbox working directory. It is writable under
	// the hardened policy (filesystem_policy.include_workdir is true).
	DefaultWorkdir = "/sandbox"
	// DefaultReadyTimeout bounds how long Prepare waits for a sandbox to become
	// ready.
	DefaultReadyTimeout = 90 * time.Second
	// DefaultProfileCatalogRef pins the provider-profile source to an immutable
	// commit: the NVIDIA/OpenShell v0.1.2 release tree the adapter was verified
	// against. A branch (such as main) is mutable, so fetching a profile from it
	// would let upstream silently change the profile's binaries scope and thus a
	// sandbox's egress policy. A commit cannot move; bumping the pin is an
	// explicit, reviewable change.
	DefaultProfileCatalogRef = "6648bd0c290efbc41ba131ee9831ee45cd431f94"
	// DefaultProfileCatalog is the OpenShell provider-profile catalog. A profile
	// the gateway does not carry is imported from <catalog>/<type>.yaml, the
	// documented `openshell profile import --url ...` recipe.
	DefaultProfileCatalog = "https://raw.githubusercontent.com/NVIDIA/OpenShell/" + DefaultProfileCatalogRef + "/providers"

	// readyPollInterval is how often Prepare re-checks readiness.
	readyPollInterval = 500 * time.Millisecond
	// providerDeleteAttempts and providerDeleteDelay bound the retry of the
	// asynchronous provider cleanup after a sandbox delete. The spike found
	// that `sandbox delete` returns before its provider detaches, so a single
	// immediate `provider delete` can fail; swallowing it would leak the
	// provider (and its credential material) into gateway state.
	providerDeleteAttempts = 60
	providerDeleteDelay    = time.Second
	// eventBuffer bounds the streamed-event channel. Events are best-effort;
	// the complete output is always available from Execution.Wait.
	eventBuffer = 256

	// keepAlive is the detached main process that keeps a sandbox alive between
	// Exec calls. The harness's own command is run through `sandbox exec`, not
	// as the sandbox's init process.
	keepAlive = "while :; do sleep 3600; done"

	// downloadScript walks the requested paths inside the sandbox and prints one
	// framed record per regular file: "FTFILE\t<path>\t<base64>". It streams
	// through exec because `sandbox download` resolves the source with
	// `realpath -e --`, which BusyBox images reject.
	downloadScript = `for g in "$@"; do
  if [ -f "$g" ]; then
    printf 'FTFILE\t%s\t' "$g"; base64 -w0 "$g"; printf '\n'
  elif [ -d "$g" ]; then
    find "$g" -type f | while IFS= read -r f; do
      printf 'FTFILE\t%s\t' "$f"; base64 -w0 "$f"; printf '\n'
    done
  fi
done`

	// downloadFrame is the record prefix the download script emits.
	downloadFrame = "FTFILE\t"
)

// CredentialResolver resolves a provider credential reference to its secret
// value. It is supplied by the launcher, so the backend never reads a secret
// from the host on its own.
type CredentialResolver interface {
	Resolve(ctx context.Context, c isolation.Credential) (string, error)
}

// StartInput is one streaming CLI invocation.
type StartInput struct {
	// Env is extra environment for the CLI process itself (non-secret, or a
	// credential passed to `provider create`).
	Env map[string]string
	// Stdin is piped to the process.
	Stdin []byte
}

// Process is a running CLI invocation.
type Process interface {
	Stdout() io.ReadCloser
	Stderr() io.ReadCloser
	// Wait blocks until the process exits and returns its exit code. A normal
	// non-zero exit is returned as (code, nil); only a failure to run the
	// command yields a non-nil error.
	Wait() (int, error)
	Kill() error
}

// Runner runs the openshell CLI. It is an interface so tests inject a fake and
// never touch a gateway.
type Runner interface {
	// Run executes a one-shot command and returns its stdout. The error carries
	// the command's stderr.
	Run(ctx context.Context, args []string, env map[string]string) ([]byte, error)
	// Start starts a streaming command.
	Start(ctx context.Context, args []string, in StartInput) (Process, error)
}

// Backend is the OpenShell implementation of isolation.IsolationBackend.
type Backend struct {
	opts Options
	run  Runner

	mu      sync.Mutex
	envs    map[string]*environment
	deleted map[string]struct{}
}

// New returns an OpenShell backend with defaults applied.
func New(opts Options) *Backend {
	if opts.CLI == "" {
		opts.CLI = DefaultCLI
	}
	if opts.Image == "" {
		opts.Image = DefaultImage
	}
	if opts.Workdir == "" {
		opts.Workdir = DefaultWorkdir
	}
	if opts.ReadyTimeout <= 0 {
		opts.ReadyTimeout = DefaultReadyTimeout
	}
	if opts.ProviderCleanupTimeout <= 0 {
		opts.ProviderCleanupTimeout = providerDeleteAttempts * providerDeleteDelay
	}
	if opts.ProviderCleanupInterval <= 0 {
		opts.ProviderCleanupInterval = providerDeleteDelay
	}
	if opts.NewName == nil {
		opts.NewName = randomName
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

// environment is the per-run sandbox state.
type environment struct {
	id          string
	name        string
	workdir     string
	hostWorkdir string

	mu        sync.Mutex
	providers []string
	procs     map[*execution]*runningProcess
	// created records that a sandbox create was attempted, so teardown knows
	// whether a sandbox delete is warranted.
	created bool
	// sandboxGone records a completed `sandbox delete`, so a retried Delete
	// skips it instead of failing on an already-removed sandbox.
	sandboxGone bool
}

// runningProcess is a live command tracked so Stop can end it.
type runningProcess struct {
	proc   Process
	cancel context.CancelFunc
}

// handle is the opaque isolation.Handle for a prepared sandbox.
type handle struct{ id string }

func (h *handle) ID() string { return h.id }

// Prepare creates a detached sandbox, applies the hardened policy, and places
// the spec's workspace and files inside it.
func (b *Backend) Prepare(ctx context.Context, spec isolation.Spec) (isolation.Handle, error) {
	if len(spec.Image.Entrypoint) > 0 {
		return nil, fmt.Errorf("%w: cannot override the image entrypoint with openshell; the sandbox runs its own keep-alive init and the workload is started with sandbox exec", isolation.ErrUnsupported)
	}
	policyPath, cleanup, err := b.writePolicy(spec.Policy, spec.Image.User)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	image := firstNonEmpty(spec.Image.Ref, b.opts.Image)
	name := b.opts.NewName()
	env := &environment{
		id:      name,
		name:    name,
		workdir: b.opts.Workdir,
		procs:   map[*execution]*runningProcess{},
	}

	for _, c := range spec.Credentials {
		provider, err := b.createProvider(ctx, name, c)
		if err != nil {
			return nil, errors.Join(err, b.teardown(ctx, env))
		}
		env.providers = append(env.providers, provider)
	}

	env.created = true
	if _, err := b.run.Run(ctx, b.cmd(b.createArgs(name, image, policyPath, env, spec)...), nil); err != nil {
		return nil, errors.Join(fmt.Errorf("openshell: create sandbox %q: %w", name, err), b.teardown(ctx, env))
	}
	if err := b.waitReady(ctx, name); err != nil {
		return nil, errors.Join(err, b.teardown(ctx, env))
	}
	if err := b.mkdir(ctx, name, env.workdir); err != nil {
		return nil, errors.Join(err, b.teardown(ctx, env))
	}
	if err := b.uploadWorkspace(ctx, env, spec.Workdir); err != nil {
		return nil, errors.Join(err, b.teardown(ctx, env))
	}
	if err := b.uploadFiles(ctx, env, spec.Files); err != nil {
		return nil, errors.Join(err, b.teardown(ctx, env))
	}

	b.mu.Lock()
	b.envs[env.id] = env
	b.mu.Unlock()
	return &handle{id: env.id}, nil
}

// createArgs is the `sandbox create` invocation: a detached, non-root sandbox
// with the rendered policy, the spec's providers, env, and labels, kept alive
// by a sleep loop.
func (b *Backend) createArgs(name, image, policyPath string, env *environment, spec isolation.Spec) []string {
	args := []string{"sandbox", "create", "--name", name, "--from", image, "--policy", policyPath}
	args = append(args, ApprovalArgs()...)
	args = append(args, "--no-auto-providers", "--detach")
	for _, provider := range env.providerList() {
		args = append(args, "--provider", provider)
	}
	for _, k := range sortedKeys(spec.Env) {
		args = append(args, "--env", k+"="+spec.Env[k])
	}
	for _, k := range sortedKeys(spec.Labels) {
		args = append(args, "--label", k+"="+spec.Labels[k])
	}
	args = append(args, "--", "sh", "-c", keepAlive)
	return b.cmd(args...)
}

// Exec starts cmd in the sandbox and streams its output.
func (b *Backend) Exec(ctx context.Context, h isolation.Handle, cmd isolation.Command) (isolation.Execution, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	if len(cmd.Argv) == 0 {
		return nil, errors.New("isolation/openshell: empty argv")
	}
	if cmd.TTY {
		return nil, fmt.Errorf("%w: interactive tty over the openShell CLI", isolation.ErrNoTerminal)
	}

	var runCtx context.Context
	var cancel context.CancelFunc
	if cmd.Timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, cmd.Timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}

	args := b.execArgs(env.name, env.resolveWorkdir(cmd.Workdir), cmd.Env, cmd.Argv)
	proc, err := b.run.Start(runCtx, args, StartInput{Stdin: cmd.Stdin})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("openshell: exec: %w", err)
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

// execArgs is one `sandbox exec` invocation.
func (b *Backend) execArgs(name, workdir string, env map[string]string, argv []string) []string {
	args := []string{"sandbox", "exec", "-n", name, "--no-login-shell"}
	if workdir != "" {
		args = append(args, "--workdir", workdir)
	}
	for _, k := range sortedKeys(env) {
		args = append(args, "--env", k+"="+env[k])
	}
	args = append(args, "--")
	args = append(args, argv...)
	return b.cmd(args...)
}

// Upload places files in the sandbox, creating parent directories.
func (b *Backend) Upload(ctx context.Context, h isolation.Handle, files []isolation.File) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	return b.uploadFiles(ctx, env, files)
}

// uploadFiles writes files to a staging directory and uploads each into its
// destination with the CLI's `sandbox upload` (the working transfer path).
func (b *Backend) uploadFiles(ctx context.Context, env *environment, files []isolation.File) error {
	if len(files) == 0 {
		return nil
	}
	stage, err := os.MkdirTemp("", "ft-openshell-upload-")
	if err != nil {
		return fmt.Errorf("isolation/openshell: stage files: %w", err)
	}
	defer func() { _ = os.RemoveAll(stage) }()

	for _, f := range files {
		guest := env.resolvePath(f.Path)
		dir := path.Dir(guest)
		if err := b.mkdir(ctx, env.name, dir); err != nil {
			return err
		}
		mode := f.Mode
		if mode == 0 {
			mode = 0o644
		}
		local := filepath.Join(stage, filepath.Base(guest))
		if err := os.WriteFile(local, f.Content, mode); err != nil {
			return fmt.Errorf("isolation/openshell: stage %q: %w", f.Path, err)
		}
		// The CLI treats a destination as the file's final path unless it ends
		// in a slash; the slash makes it place the file inside the directory.
		if _, err := b.run.Run(ctx, b.cmd("sandbox", "upload", env.name, local, withSlash(dir)), nil); err != nil {
			return fmt.Errorf("openshell: upload %q: %w", f.Path, err)
		}
	}
	return nil
}

// withSlash marks a path as a directory for `sandbox upload`.
func withSlash(dir string) string {
	if strings.HasSuffix(dir, "/") {
		return dir
	}
	return dir + "/"
}

// uploadWorkspace uploads a host workspace named by Spec.Workdir into the
// sandbox. OpenShell has no host bind-mount, so the workspace moves by upload;
// uploading the work tree itself (not a copy) keeps `.gitignore` filtering. The
// workspace's directory name is preserved and becomes the run's workdir.
func (b *Backend) uploadWorkspace(ctx context.Context, env *environment, hostDir string) error {
	if hostDir == "" {
		return nil
	}
	info, err := os.Stat(hostDir)
	if err != nil || !info.IsDir() {
		return nil // a guest path, or nothing to upload
	}
	if _, err := b.run.Run(ctx, b.cmd("sandbox", "upload", env.name, hostDir, env.workdir), nil); err != nil {
		return fmt.Errorf("openshell: upload workspace %q: %w", hostDir, err)
	}
	env.hostWorkdir = hostDir
	env.workdir = path.Join(env.workdir, filepath.Base(hostDir))
	return nil
}

// Download copies the requested paths out of the sandbox, streaming file
// contents over `exec` + base64. A missing path is omitted, not an error.
func (b *Backend) Download(ctx context.Context, h isolation.Handle, paths []string) ([]isolation.File, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, nil
	}
	requests := make([]pathRequest, 0, len(paths))
	args := []string{"sandbox", "exec", "-n", env.name, "--no-login-shell", "--", "sh", "-c", downloadScript, "--"}
	for _, p := range paths {
		guest := env.resolvePath(p)
		requests = append(requests, pathRequest{display: p, guest: guest})
		args = append(args, guest)
	}
	out, err := b.run.Run(ctx, b.cmd(args...), nil)
	if err != nil {
		return nil, fmt.Errorf("openshell: download: %w", err)
	}

	var files []isolation.File
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, downloadFrame) {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 {
			continue
		}
		content, err := base64.StdEncoding.DecodeString(parts[2])
		if err != nil {
			continue
		}
		files = append(files, isolation.File{Path: displayPath(requests, parts[1]), Content: content, Mode: 0o644})
	}
	return files, nil
}

// pathRequest pairs a requested path with its resolved guest path.
type pathRequest struct {
	display string
	guest   string
}

// displayPath maps a guest path back to the path the caller asked for, so a
// directory walk returns paths rooted at the request.
func displayPath(requests []pathRequest, guest string) string {
	for _, r := range requests {
		switch {
		case guest == r.guest:
			return r.display
		case strings.HasPrefix(guest, r.guest+"/"):
			return path.Join(r.display, strings.TrimPrefix(guest, r.guest+"/"))
		}
	}
	return guest
}

// Logs streams the sandbox's own log lines. Follow streams until the context is
// cancelled; otherwise it returns the recent tail.
func (b *Backend) Logs(ctx context.Context, h isolation.Handle, opts isolation.LogOptions) (<-chan isolation.Event, error) {
	env, err := b.lookup(h)
	if err != nil {
		return nil, err
	}
	args := []string{"logs", env.name, "--source", "sandbox", "-n", "200"}
	events := make(chan isolation.Event, eventBuffer)

	if opts.Follow {
		args = append(args, "--tail")
		proc, err := b.run.Start(ctx, b.cmd(args...), StartInput{})
		if err != nil {
			return nil, fmt.Errorf("openshell: logs: %w", err)
		}
		go streamLines(proc.Stdout(), events)
		return events, nil
	}

	out, err := b.run.Run(ctx, b.cmd(args...), nil)
	if err != nil {
		return nil, fmt.Errorf("openshell: logs: %w", err)
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

// Stop cancels the run's live commands and is idempotent. It keeps the sandbox
// (and its workspace) alive, so the handle stays usable.
func (b *Backend) Stop(_ context.Context, h isolation.Handle) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	env.stop()
	return nil
}

// Delete stops the sandbox, removes it and its workspace, and deletes every
// provider it created. It is idempotent; a foreign or never-prepared handle is
// an error. A handle is only recorded as deleted once both the sandbox and its
// providers are gone, so a transient failure is retried rather than turned into
// a no-op that leaks the sandbox or a provider.
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
		return fmt.Errorf("isolation/openshell: unknown handle %q", id)
	}

	env.stop()
	if err := b.deleteSandbox(ctx, env); err != nil {
		return err
	}
	var errs error
	for _, provider := range env.providerList() {
		if err := b.deleteProvider(ctx, provider); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	if errs != nil {
		return errs
	}

	b.mu.Lock()
	delete(b.envs, id)
	b.deleted[id] = struct{}{}
	b.mu.Unlock()
	return nil
}

// deleteSandbox removes the sandbox once, recording completion so a retried
// Delete skips an already-removed sandbox.
func (b *Backend) deleteSandbox(ctx context.Context, env *environment) error {
	if env.isSandboxGone() {
		return nil
	}
	if _, err := b.run.Run(ctx, b.cmd("sandbox", "delete", env.name), nil); err != nil {
		return fmt.Errorf("openshell: delete sandbox %q: %w", env.name, err)
	}
	env.markSandboxGone()
	return nil
}

// ApplyPolicy installs a policy on the live sandbox. An empty policy (nothing
// to enforce) is a no-op. Egress hosts and a non-root identity are applied; the
// hardened filesystem baseline from the checked-in template is always kept, so
// an apply can add egress but never widen the sandbox's static controls.
func (b *Backend) ApplyPolicy(ctx context.Context, h isolation.Handle, p isolation.Policy) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	if emptyPolicy(p) {
		return nil
	}
	policyPath, cleanup, err := b.writePolicy(p, "")
	if err != nil {
		return err
	}
	defer cleanup()

	args := []string{"policy", "set", "--policy", policyPath, "--wait", "--timeout", "60", env.name}
	if _, err := b.run.Run(ctx, b.cmd(args...), nil); err != nil {
		return fmt.Errorf("openshell: set policy: %w", err)
	}
	return nil
}

// AttachCredential creates a provider from the resolved credential and attaches
// it to the sandbox. Without a resolver it refuses (ErrUnsupported) rather than
// reading the secret from the host.
func (b *Backend) AttachCredential(ctx context.Context, h isolation.Handle, c isolation.Credential) error {
	env, err := b.lookup(h)
	if err != nil {
		return err
	}
	if c.EnvVar == "" {
		return errors.New("isolation/openshell: credential has no EnvVar")
	}
	if b.opts.Credentials == nil {
		return fmt.Errorf("%w: no credential resolver configured", isolation.ErrUnsupported)
	}
	provider, err := b.createProvider(ctx, env.name, c)
	if err != nil {
		return err
	}
	if _, err := b.run.Run(ctx, b.cmd("sandbox", "provider", "attach", "--wait", env.name, provider), nil); err != nil {
		return fmt.Errorf("openshell: attach provider %q: %w", provider, err)
	}
	env.addProvider(provider)
	return nil
}

// createProvider creates an OpenShell provider for a credential. The secret is
// passed in the child environment (never as an argv value) and the gateway
// injects a placeholder into the sandbox, substituting the real value only at
// the provider-authorized endpoint.
//
// A fresh gateway does not carry every provider profile. When the gateway
// rejects the create because the profile is missing, the profile is imported
// from the catalog and the create is retried once, so a fresh gateway can run
// with a provider without an out-of-band import. If the profile is still
// missing the error names the exact import command to run by hand; if the retry
// failed for a different reason, that reason is surfaced.
func (b *Backend) createProvider(ctx context.Context, sandbox string, c isolation.Credential) (string, error) {
	if c.EnvVar == "" {
		return "", errors.New("isolation/openshell: credential has no EnvVar")
	}
	if b.opts.Credentials == nil {
		return "", fmt.Errorf("%w: no credential resolver configured", isolation.ErrUnsupported)
	}
	value, err := b.opts.Credentials.Resolve(ctx, c)
	if err != nil {
		return "", fmt.Errorf("openshell: resolve credential %q: %w", c.Provider, err)
	}
	provider := providerName(sandbox, c.EnvVar)
	args := []string{"provider", "create", "--name", provider, "--type", c.Provider, "--credential", c.EnvVar}
	env := map[string]string{c.EnvVar: value}

	if _, err := b.run.Run(ctx, b.cmd(args...), env); err == nil {
		return provider, nil
	} else if !providerProfileMissing(err) {
		return "", fmt.Errorf("openshell: create provider %q: %w", provider, err)
	}

	importErr := b.importProviderProfile(ctx, c.Provider)
	_, retryErr := b.run.Run(ctx, b.cmd(args...), env)
	switch {
	case retryErr == nil:
		return provider, nil
	case providerProfileMissing(retryErr):
		// The import did not provision a usable profile, so the manual
		// import command is still the action.
		return "", b.missingProfileError(c.Provider, importErr)
	default:
		// The import ran but the create failed for another reason (for
		// example, the credential is not declared by the imported profile).
		// Surface that cause instead of repeating "not imported".
		return "", fmt.Errorf("openshell: create provider %q after importing profile %q: %w", provider, c.Provider, retryErr)
	}
}

// importProviderProfile imports a provider profile from the catalog so a fresh
// gateway can create a provider whose profile is not built in. It is
// best-effort: the caller retries the create and, if that still fails, reports
// the exact manual import command.
func (b *Backend) importProviderProfile(ctx context.Context, providerType string) error {
	url := profileCatalogURL(providerType)
	if url == "" {
		return fmt.Errorf("no catalog URL for provider type %q", providerType)
	}
	if _, err := b.run.Run(ctx, b.cmd("profile", "import", "--url", url), nil); err != nil {
		return err
	}
	return nil
}

// missingProfileError is the actionable failure for a provider whose profile is
// absent from the gateway: it names the exact import command, plus the automatic
// import failure when there was one.
func (b *Backend) missingProfileError(providerType string, importErr error) error {
	cmd := b.profileImportCommand(providerType)
	if importErr != nil {
		return fmt.Errorf("openshell: provider profile %q is not imported into the gateway and automatic import failed (%w); import it with:\n  %s", providerType, importErr, cmd)
	}
	return fmt.Errorf("openshell: provider profile %q is not imported into the gateway; import it with:\n  %s", providerType, cmd)
}

// profileImportCommand renders the copy-pasteable import command for a provider
// type, mirroring the selected gateway.
func (b *Backend) profileImportCommand(providerType string) string {
	if url := profileCatalogURL(providerType); url != "" {
		return b.cliCommand("profile", "import", "--url", url)
	}
	return b.cliCommand("profile", "import", "-f", providerType+".yaml")
}

// cliCommand renders a copy-pasteable openshell invocation.
func (b *Backend) cliCommand(args ...string) string {
	return strings.Join(append([]string{b.opts.CLI}, b.cmd(args...)...), " ")
}

// providerProfileMissing reports whether err is the gateway's rejection of a
// provider whose profile is not imported, which a profile import can repair.
// Both the CLI's rendered message and the gateway's own text carry this phrase.
func providerProfileMissing(err error) bool {
	return err != nil && strings.Contains(err.Error(), "import a matching profile")
}

// profileCatalogURL is the OpenShell profile catalog URL for a provider type, or
// "" when the type is not a safe single path segment.
func profileCatalogURL(providerType string) string {
	if !safeProfileID(providerType) {
		return ""
	}
	return DefaultProfileCatalog + "/" + providerType + ".yaml"
}

// safeProfileID reports whether id is a single safe path segment for a catalog
// URL: lowercase letters, digits, dot, dash, and underscore, and never "." or
// "..".
func safeProfileID(id string) bool {
	if id == "" || id == "." || id == ".." {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
		default:
			return false
		}
	}
	return true
}

// deleteProvider removes a provider, retrying while the asynchronous sandbox
// deletion settles. A provider that no longer exists counts as removed. A
// provider that cannot be removed is reported, never swallowed: it may still
// hold credential material.
func (b *Backend) deleteProvider(ctx context.Context, provider string) error {
	deadline := time.Now().Add(b.opts.ProviderCleanupTimeout)
	var last error
	for {
		// A provider that is already gone is a success, so a retried Delete
		// does not spin on "not found".
		if _, err := b.run.Run(ctx, b.cmd("provider", "get", provider), nil); err != nil {
			return nil
		}
		if _, err := b.run.Run(ctx, b.cmd("provider", "delete", provider), nil); err == nil {
			return nil
		} else {
			last = err
		}
		if !time.Now().After(deadline) {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(b.opts.ProviderCleanupInterval):
				continue
			}
		}
		return fmt.Errorf("openshell: delete provider %q: %w", provider, last)
	}
}

// writePolicy renders the effective policy to a temp file and returns its path
// and a cleanup function. A caller-supplied Raw document is honored verbatim;
// otherwise the hardened template is rendered. The identity is seeded from the
// image's user (a harness's requested non-root identity), then a policy or
// backend override; Build rejects root.
func (b *Backend) writePolicy(p isolation.Policy, imageUser string) (string, func(), error) {
	user, group := splitUser(imageUser)
	runAsUser := firstNonEmpty(p.RunAsUser, user, b.opts.RunAsUser)
	runAsGroup := firstNonEmpty(p.RunAsGroup, group, b.opts.RunAsGroup)

	var raw []byte
	if len(p.Raw) > 0 {
		raw = p.Raw
	} else {
		policy, err := Build(Options{
			OverridePath:  b.opts.OverridePath,
			AllowHosts:    append(append([]string(nil), b.opts.AllowHosts...), p.AllowHosts...),
			HarnessBinary: b.opts.HarnessBinary,
			RunAsUser:     runAsUser,
			RunAsGroup:    runAsGroup,
		})
		if err != nil {
			return "", nil, err
		}
		if raw, err = policy.YAML(); err != nil {
			return "", nil, err
		}
	}
	f, err := os.CreateTemp("", "ft-openshell-policy-*.yaml")
	if err != nil {
		return "", nil, fmt.Errorf("openshell: write policy: %w", err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, fmt.Errorf("openshell: write policy: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, fmt.Errorf("openshell: write policy: %w", err)
	}
	return f.Name(), func() { _ = os.Remove(f.Name()) }, nil
}

// waitReady polls the sandbox until it reports Ready or the deadline passes.
func (b *Backend) waitReady(ctx context.Context, name string) error {
	deadline, cancel := context.WithTimeout(ctx, b.opts.ReadyTimeout)
	defer cancel()
	for {
		if out, err := b.run.Run(deadline, b.cmd("sandbox", "get", name, "-o", "json"), nil); err == nil {
			var status struct {
				Phase string `json:"phase"`
			}
			if json.Unmarshal(out, &status) == nil && status.Phase == "Ready" {
				return nil
			}
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("openshell: sandbox %q did not become ready: %w", name, deadline.Err())
		case <-time.After(readyPollInterval):
		}
	}
}

// mkdir creates a directory inside the sandbox.
func (b *Backend) mkdir(ctx context.Context, name, dir string) error {
	args := b.execArgs(name, "", nil, []string{"sh", "-c", `mkdir -p "$1"`, "--", dir})
	if _, err := b.run.Run(ctx, args, nil); err != nil {
		return fmt.Errorf("openshell: mkdir %q: %w", dir, err)
	}
	return nil
}

// teardown removes a partially-created sandbox and its providers after a failed
// Prepare. Provider deletion uses the retrying path (the gateway detaches
// providers asynchronously) and any cleanup failure is returned, never
// swallowed: an orphaned provider still holds credential material.
func (b *Backend) teardown(ctx context.Context, env *environment) error {
	ctx = context.WithoutCancel(ctx)
	var errs error
	if env.created {
		if err := b.deleteSandbox(ctx, env); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	for _, provider := range env.providerList() {
		if err := b.deleteProvider(ctx, provider); err != nil {
			errs = errors.Join(errs, err)
		}
	}
	return errs
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
		return nil, fmt.Errorf("isolation/openshell: unknown handle %q", id)
	}
	return env, nil
}

// handleID validates that h is a handle this backend created.
func handleID(h isolation.Handle) (string, error) {
	if h == nil {
		return "", errors.New("isolation/openshell: nil handle")
	}
	hh, ok := h.(*handle)
	if !ok {
		return "", fmt.Errorf("isolation/openshell: foreign handle %q", h.ID())
	}
	return hh.id, nil
}

// resolveWorkdir maps a command's workdir: empty and the uploaded host path
// both mean the sandbox workdir; anything else is taken as a guest path.
func (e *environment) resolveWorkdir(workdir string) string {
	switch {
	case workdir == "":
		return e.workdir
	case e.hostWorkdir != "" && workdir == e.hostWorkdir:
		return e.workdir
	default:
		return workdir
	}
}

// resolvePath roots a relative path at the sandbox workdir; an absolute path is
// already a guest path.
func (e *environment) resolvePath(p string) string {
	if path.IsAbs(p) {
		return path.Clean(p)
	}
	return path.Join(e.workdir, p)
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

// stop ends every live command, keeping the sandbox itself alive.
func (e *environment) stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, rp := range e.procs {
		rp.cancel()
		_ = rp.proc.Kill()
	}
}

func (e *environment) addProvider(name string) {
	e.mu.Lock()
	e.providers = append(e.providers, name)
	e.mu.Unlock()
}

func (e *environment) providerList() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.providers...)
}

func (e *environment) isSandboxGone() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.sandboxGone
}

func (e *environment) markSandboxGone() {
	e.mu.Lock()
	e.sandboxGone = true
	e.mu.Unlock()
}

// splitUser splits an image user "uid[:gid]" into its non-root parts. An empty
// user yields empty strings, so a backend override or the hardened default
// applies.
func splitUser(user string) (string, string) {
	user = strings.TrimSpace(user)
	if user == "" {
		return "", ""
	}
	if i := strings.IndexByte(user, ':'); i >= 0 {
		return strings.TrimSpace(user[:i]), strings.TrimSpace(user[i+1:])
	}
	return user, ""
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

// execRunner runs the real openshell binary.
type execRunner struct{ name string }

func (e execRunner) Run(ctx context.Context, args []string, env map[string]string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.name, args...)
	cmd.Env = envSlice(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("openshell %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
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

// envSlice layers overrides over the current environment, so the CLI keeps the
// variables it needs (PATH, HOME) and a credential is passed only where asked.
// An override replaces any same-named host variable, so a real key already in
// the environment can never shadow the intended one.
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

// providerName derives a gateway-unique provider name for a credential.
func providerName(sandbox, envVar string) string {
	sum := sha256.Sum256([]byte(envVar))
	return sandbox + "-" + hex.EncodeToString(sum[:3])
}

// randomName returns a gateway-safe sandbox name (the cap is 19 characters).
func randomName() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("ft%d", time.Now().UnixNano()%1_000_000_000)
	}
	return "ft" + hex.EncodeToString(b[:])
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

// cmd prepends the selected gateway, if any, so every invocation targets it.
func (b *Backend) cmd(args ...string) []string {
	if b.opts.Gateway == "" {
		return append([]string(nil), args...)
	}
	return append([]string{"--gateway", b.opts.Gateway}, args...)
}

var _ isolation.IsolationBackend = (*Backend)(nil)
