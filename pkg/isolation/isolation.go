// Package isolation defines the pluggable isolation-backend port for `ft run`.
//
// An IsolationBackend provides an execution environment for a coding-agent
// harness: a kernel-isolated sandbox, a container, a microVM, a cluster
// workload, or the local host with no isolation at all. The port is runtime
// neutral on purpose: it must not assume the environment is sandboxed, so the
// first backend (the local host, dev-only) and a real isolating backend
// (OpenShell) are both ordinary implementations of the same interface.
//
// Backend selection is a registration, never a hardcoded switch: the concrete
// adapters live in subpackages (for example pkg/isolation/local,
// pkg/isolation/openshell) and register into a Registry. No backend-specific
// type appears in this package.
//
// A Harness (pkg/harness) describes what to run in these terms: it builds a
// Spec for Prepare and a Command for Exec. That dependency points one way,
// harness -> isolation, so the two plugin points stay independently swappable.
package isolation

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/khoinguyen/factotum/pkg/registry"
)

// ErrUnsupported means the backend cannot honor a request it is not required to
// support, such as a filesystem or network policy it cannot enforce. Callers
// treat it as "this backend cannot do that", not as a crash.
//
// It is reserved for a well-formed request with a valid handle: a backend must
// reject an unknown, foreign, or nil handle with a different error, before
// considering whether the operation is supported, so ErrUnsupported never
// disguises a failed handle check.
var ErrUnsupported = errors.New("isolation: unsupported by backend")

// ErrNoTerminal means the backend cannot attach an interactive terminal to a
// command that requested one (Command.TTY). It wraps ErrUnsupported, so a caller
// that only cares whether the operation is supported still matches, while a
// caller that offered an attached session can tell this apart from any other
// unsupported request and point the user at a headless rerun.
var ErrNoTerminal = fmt.Errorf("%w: no interactive terminal", ErrUnsupported)

// Image is the base the backend provisions the environment from. Container,
// microVM, and cluster backends use it; a host backend ignores it.
type Image struct {
	// Ref is the image reference, e.g. ghcr.io/anomalyco/opencode:latest.
	Ref string
	// Entrypoint overrides the image entrypoint where the backend supports it.
	// A backend that cannot honor it may reject a non-empty value as
	// unsupported; backends are not required to.
	Entrypoint []string
	// User is the non-root identity the workload runs as, where supported. An
	// empty value leaves the image default, which for many agent images is root.
	User string
}

// Resources caps the environment's CPU and memory where the backend supports
// it. Values are opaque strings ("2", "2Gi") so the port does not pick a unit.
type Resources struct {
	CPUs   string
	Memory string
}

// File is a file to place in, or copy out of, the environment. Path is the
// location inside the environment. A relative Path is rooted at the
// environment's workspace root (Spec.Workdir, or the backend's own workspace
// when Workdir is empty); an absolute Path is used as given where the backend
// supports it.
type File struct {
	Path    string
	Content []byte
	Mode    os.FileMode
}

// Policy is the run's access policy. A backend that enforces policy maps these
// fields to its own mechanism; a backend that cannot returns ErrUnsupported
// from ApplyPolicy. Raw carries a backend-native policy document (for example
// OpenShell YAML) as an opaque blob so a richer policy can pass through
// without this package importing that backend's types.
type Policy struct {
	// ReadOnly and ReadWrite are paths the workload may read and write. A
	// backend with no filesystem policy ignores them.
	ReadOnly  []string
	ReadWrite []string
	// AllowHosts are egress destinations the run may reach. DefaultDeny asks
	// the backend for deny-by-default egress; an empty AllowHosts then means
	// no egress at all.
	AllowHosts  []string
	DefaultDeny bool
	// RunAsUser and RunAsGroup force a workload identity where supported.
	RunAsUser  string
	RunAsGroup string
	// Raw is a backend-native policy document, opaque to this package.
	Raw []byte
}

// Credential is a reference to a secret the run may use. It never carries the
// secret value: the backend resolves the provider reference, so credentials
// stay out of ft and out of the environment's raw environment where the backend
// can inject a placeholder instead.
type Credential struct {
	// Provider names the credential provider, e.g. "openrouter".
	Provider string
	// Ref is the provider's opaque handle to the secret.
	Ref string
	// EnvVar is the variable the harness expects the credential under.
	EnvVar string
}

// Spec is what Prepare creates: the environment, its initial contents, and the
// policy and credentials that apply to the whole run.
type Spec struct {
	Image       Image
	Workdir     string
	Env         map[string]string
	Files       []File
	Policy      Policy
	Credentials []Credential
	Resources   Resources
	// Labels are backend-agnostic identifiers (project, task) for the
	// environment, for logs and cleanup.
	Labels map[string]string
}

// Command is one process invocation inside the environment. The harness has
// already resolved prompt delivery into Argv, Stdin, or Env, so a backend only
// has to execute it.
type Command struct {
	Argv    []string
	Env     map[string]string
	Workdir string
	Stdin   []byte
	// TTY requests an interactive terminal where the backend supports one. A
	// backend that cannot attach one fails Exec with ErrNoTerminal rather than
	// running the command without a terminal.
	TTY     bool
	Timeout time.Duration
}

// EventKind classifies a streamed event.
type EventKind int

const (
	// EventOutput is process output.
	EventOutput EventKind = iota
	// EventStatus is a lifecycle change (started, exited, frozen).
	EventStatus
	// EventPolicy is a policy decision (allowed or denied).
	EventPolicy
	// EventError is a backend or process error.
	EventError
)

// Stream identifies where an EventOutput event came from.
type Stream int

const (
	StreamSystem Stream = iota
	StreamStdout
	StreamStderr
)

// Event is one streamed backend or process event.
type Event struct {
	Time    time.Time
	Kind    EventKind
	Stream  Stream
	Message string
}

// ExecResult is the outcome of a completed command.
type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte
}

// Handle is an opaque reference to a prepared environment. Each backend
// implements it with its own concrete type; callers only ever use ID.
type Handle interface {
	ID() string
}

// Execution is a running (or finished) command. Draining Events streams its
// output; Wait blocks for the result. A caller that wants a detached run keeps
// the Execution, drives it later, or drops it without waiting.
type Execution interface {
	// Events streams output and status events until the command exits. The
	// channel is closed when the command finishes.
	Events() <-chan Event
	// Wait blocks until the command finishes and returns its result. It must be
	// safe to call without draining Events, so a backend either buffers the
	// event channel and then closes it, or drains it itself.
	Wait(ctx context.Context) (ExecResult, error)
}

// LogOptions narrows a Logs request.
type LogOptions struct {
	Since  time.Time
	Follow bool
}

// IsolationBackend is the port. Implementations must be safe for concurrent use.
type IsolationBackend interface {
	// Name is the registration key.
	Name() string
	// Prepare creates the environment described by spec and returns a handle.
	Prepare(ctx context.Context, spec Spec) (Handle, error)
	// Exec starts cmd in the environment and returns a live Execution.
	Exec(ctx context.Context, h Handle, cmd Command) (Execution, error)
	// Upload copies files into the environment, creating parent directories.
	Upload(ctx context.Context, h Handle, files []File) error
	// Download copies the named paths out of the environment. Missing paths are
	// omitted rather than an error.
	Download(ctx context.Context, h Handle, paths []string) ([]File, error)
	// Logs streams the environment's events (lifecycle, policy decisions).
	Logs(ctx context.Context, h Handle, opts LogOptions) (<-chan Event, error)
	// Stop stops the environment's processes but keeps its state for a restart.
	// It is idempotent: stopping an environment with nothing running, or one
	// already stopped, returns nil.
	Stop(ctx context.Context, h Handle) error
	// Delete stops and removes the environment and all of its state. It is
	// idempotent: deleting an already-deleted handle returns nil, so a caller's
	// deferred teardown never races a retry. An unknown, foreign, or nil handle
	// is still an error.
	Delete(ctx context.Context, h Handle) error
	// ApplyPolicy installs or replaces the run's policy, where supported. An
	// empty Policy is a no-op every backend must accept; a backend that cannot
	// enforce a non-empty policy returns ErrUnsupported.
	ApplyPolicy(ctx context.Context, h Handle, p Policy) error
	// AttachCredential makes a credential reference available to the run.
	AttachCredential(ctx context.Context, h Handle, c Credential) error
}

// Registry is the plugin registry for isolation backends.
type Registry = registry.Registry[IsolationBackend]

// NewRegistry returns an empty isolation-backend registry.
func NewRegistry() *Registry { return registry.New[IsolationBackend]() }
