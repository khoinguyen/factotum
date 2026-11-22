// Package opencode is the OpenCode harness for `ft run`: it drives the OpenCode
// agent CLI headlessly, either as the host binary through the dev-only local
// backend or from the shipped container image through an isolating backend such
// as OpenShell.
//
// It implements the harness port (pkg/harness) and speaks only the isolation
// vocabulary: Spec names the base image and working identity;
// Command builds the headless invocation (opencode run with the model flag and
// the prompt as the final positional) or, for an interactive request, the
// terminal-attached TUI invocation; Result parses the agent's output. V2
// launches on its own private server in both modes, so the receiver plugin it
// loads inherits the run's environment. That server also carries a per-launch
// config layer disabling OpenCode V2's idle location unload, which would evict
// the plugin after an hour of session inactivity.
//
// The model and its credentials are never hardcoded. The model comes from the
// configured provider (Options.Model, overridden by Request.Model) and is passed
// through as --model; a credential, when configured, is declared as a provider
// reference with its environment variable name, and the isolation backend
// resolves the secret. No secret value is read or written here.
package opencode

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/internal/procgroup"
	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

// Name is the V1 harness's registration key.
const Name = "opencode"

// NameV2 is the OpenCode V2 harness's registration key. V2 ships as its own
// build with a distinct executable, so it is a separate registration rather than
// a flag on the V1 harness: `--harness opencode2` selects it and leaves the V1
// `opencode` harness untouched.
const NameV2 = "opencode2"

// Variant selects which OpenCode major a harness drives. The majors ship as
// separate executables and images, and their interactive flags differ, so a
// harness is bound to one variant. The zero value is V1, so existing callers
// keep driving V1 unchanged.
type Variant int

const (
	// V1 is the original OpenCode: the `opencode` binary, the rolling
	// ghcr.io/anomalyco/opencode:latest image, and a TUI that accepts a
	// top-level --model.
	V1 Variant = iota
	// V2 is OpenCode V2. It is selected by a distinct executable:
	// `opencode2`, the alias the npm @opencode/cli package installs next to
	// `opencode` (so V1 and V2 coexist). The brew `opencode-v2` formula ships
	// the same V2 build but links it as `opencode`, conflicting with V1, so a
	// host-installed V2 resolves as `opencode2` only via npm; a pinned V2
	// image covers the container path. V2's TUI has no top-level --model
	// (the flag lives only on `run`/`mini`), so the interactive invocation
	// omits it; and V2 is launched --standalone in both modes, because its
	// shared background service cannot see a run's receiver environment.
	V2
)

func (v Variant) name() string {
	if v == V2 {
		return NameV2
	}
	return Name
}

func (v Variant) binary() string {
	if v == V2 {
		return DefaultBinaryV2
	}
	return DefaultBinary
}

func (v Variant) image() string {
	if v == V2 {
		return DefaultImageV2
	}
	return DefaultImage
}

// interactiveModel reports whether the variant's TUI accepts a top-level
// --model flag. V1 does; V2 rejects it (it is a `run`/`mini` flag only), so the
// interactive invocation omits it and the TUI uses its configured default.
func (v Variant) interactiveModel() bool { return v != V2 }

// privateServer reports whether the variant must be launched on its own server
// instead of attaching to the shared background service. V2 loads plugin code in
// that shared service, whose process environment carries none of the run's
// FACTOTUM_* variables, so a receiver it loads is inert. `--standalone` gives
// the launch a private server that inherits the run's environment, which is what
// makes `ft run --harness opencode2 --actor` register a live run. V1 runs plugin
// code in the launched process itself, so it needs no such flag (and would
// reject one).
func (v Variant) privateServer() bool { return v == V2 }

// versionPattern is the first dotted version in an `opencode --version` banner,
// which is either bare ("1.19.0") or prefixed ("opencode v2.0.25").
var versionPattern = regexp.MustCompile(`[0-9]+\.[0-9]+`)

// MajorVersion extracts the major version number from an `opencode --version`
// banner. ok is false when the banner carries no dotted version, so a caller
// treats an unparseable banner the same as an unknown binary rather than
// guessing a major.
func MajorVersion(banner string) (major int, ok bool) {
	match := versionPattern.FindString(banner)
	if match == "" {
		return 0, false
	}
	head, _, _ := strings.Cut(match, ".")
	n, err := strconv.Atoi(head)
	if err != nil {
		return 0, false
	}
	return n, true
}

// versionProbeTimeout bounds one `--version` probe. It exists to stop a hung
// binary from stalling run selection, not to race a loaded host: fork/exec
// latency on a busy machine reaches seconds — past 3.7s on the host where this
// flaked, against ~13ms idle, and past 17s on a deliberately saturated one — and
// a bound near that tail does not merely slow the probe, it reports V1 for an
// in-place V2 (t-izluv7llq6). So the bound is chosen as at least four times the
// tail parallel loop agents produce (see the probe test), and 30s is also as
// long as run selection should ever wait for one probe; Docker's readiness bound
// (DefaultReadyTimeout) picks the same number.
const versionProbeTimeout = 30 * time.Second

// VariantForBinary returns the OpenCode variant the named binary drives,
// detected from its `--version` output: a major version of 2 or later is V2.
// Everything else — a V1 banner, a missing binary, a nonzero exit, or an
// unparseable banner — is V1, so detection never selects V2 by accident. This
// lets `--harness opencode` follow an in-place upgrade (brew installs V2 under
// the same `opencode` name) without a config change.
func VariantForBinary(binary string) Variant {
	return probeVariant(binary, versionProbeTimeout)
}

// probeVariant is VariantForBinary with its bound exposed, so a test can pin the
// hang guard without waiting the production bound out.
func probeVariant(binary string, timeout time.Duration) Variant {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--version")
	// The probe owns no terminal, so it can put the binary in its own process
	// group and signal the group when the bound expires. That is what ends the
	// probe: a launcher can leave a grandchild holding the stdout pipe, and
	// killing the direct child alone neither ends the probe nor reaps the
	// grandchild - it abandons a live process on every hung `--version`.
	procgroup.Isolate(cmd)
	cmd.Cancel = func() error { return procgroup.KillGroup(cmd) }
	// WaitDelay stays as the backstop for a descendant that left the group: it
	// bounds the drain, where the group kill bounds the tree.
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	// The probe is over, but the group is not: a binary that answered can leave a
	// worker it forked in the group, and no bound expires on a normal exit to
	// signal it. End what is left; a group with no survivors makes this a no-op.
	_ = procgroup.KillGroup(cmd)
	if err != nil {
		return V1
	}
	major, ok := MajorVersion(string(out))
	if !ok || major < 2 {
		return V1
	}
	return V2
}

const (
	// DefaultImage is the OpenCode V1 agent image (Alpine, entrypoint
	// `opencode`). It declares no USER, so the image runs as root; DefaultUser
	// carries the non-root identity a backend should request instead.
	DefaultImage = "ghcr.io/anomalyco/opencode:latest"
	// DefaultImageV2 is the OpenCode V2 agent image. V2 publishes under version
	// tags while :latest still points at V1, so the V2 harness pins the V2
	// release it was verified against. Like the V1 image it declares no USER.
	DefaultImageV2 = "ghcr.io/anomalyco/opencode:2.0.27"
	// DefaultBinary is the V1 argv[0] on the host PATH and inside the image.
	DefaultBinary = "opencode"
	// DefaultBinaryV2 is the V2 argv[0]: the `opencode2` alias the npm
	// @opencode/cli package installs, keeping it distinct from V1's `opencode`.
	DefaultBinaryV2 = "opencode2"
	// DefaultUser is the non-root identity the workload should run as. The
	// shipped images have no interactive home for this uid, so a backend must
	// provide a writable HOME (for example /tmp).
	DefaultUser = "1000:1000"
)

// ErrNoPrompt means Command was called without a prompt: a headless run has
// nothing to execute.
var ErrNoPrompt = errors.New("harness/opencode: no prompt")

// The two ways a caller hands OpenCode a config layer for one launch. Either one
// set in the environment replaces the launch's own layer, so both are treated as
// the caller's say.
const (
	envConfig        = "OPENCODE_CONFIG"
	envConfigContent = "OPENCODE_CONFIG_CONTENT"
)

// idleLocationLayer is the config layer a V2 private server receives so OpenCode
// does not evict the location's services - the ft msg receiver plugin among
// them - after experimental.location_idle_timeout without session activity (60
// minutes by default). The evicted TUI survives, but the receiver stops claiming
// and the run drops out of `ft msg runs --live` while a handoff sits queued.
const idleLocationLayer = `{"experimental":{"location_idle_timeout":false}}`

// Options configures the harness. The zero value is usable: it targets the
// default image and the `opencode` binary on PATH.
type Options struct {
	// Variant selects the OpenCode major to drive (V1 or V2). The zero value is
	// V1, so an Options literal that predates V2 keeps driving V1 unchanged.
	Variant Variant
	// Image overrides the base image reference. Defaults to the variant's
	// default image (DefaultImage, or DefaultImageV2 for V2).
	Image string
	// Binary is the argv[0] for the invocation: a host path resolved locally,
	// or the binary name that exists inside Image. Defaults to the variant's
	// default binary (DefaultBinary, or DefaultBinaryV2 for V2).
	Binary string
	// User is the non-root identity the backend should run as. For the shipped
	// image it defaults to DefaultUser; with a custom Image it is empty, so the
	// image's own default applies unless User is set.
	User string
	// Model is the configured default model in provider/model form. It is
	// never a built-in literal: the launcher sets it from the provider config.
	Model string
	// Provider names the credential provider, e.g. "openrouter".
	Provider string
	// CredentialEnvVar is the variable the credential must arrive under, e.g.
	// "OPENROUTER_API_KEY". CredentialEnvVar and Provider must both be set for
	// the harness to declare a credential.
	CredentialEnvVar string
	// Args are extra arguments passed through before the prompt, e.g.
	// {"--agent", "build"} or {"--auto"} to let the agent act without
	// permission prompts.
	Args []string
}

// Harness is the OpenCode implementation of harness.Harness.
type Harness struct {
	name             string
	image            string
	binary           string
	user             string
	model            string
	provider         string
	credEnv          string
	args             []string
	interactiveModel bool
	standalone       bool
}

// New returns an OpenCode harness for the configured variant with defaults
// applied.
func New(opts Options) *Harness {
	v := opts.Variant
	h := &Harness{
		name:             v.name(),
		image:            firstNonEmpty(opts.Image, v.image()),
		binary:           firstNonEmpty(opts.Binary, v.binary()),
		user:             opts.User,
		model:            opts.Model,
		provider:         opts.Provider,
		credEnv:          opts.CredentialEnvVar,
		args:             append([]string(nil), opts.Args...),
		interactiveModel: v.interactiveModel(),
		standalone:       v.privateServer(),
	}
	if opts.User == "" && opts.Image == "" {
		h.user = DefaultUser
	}
	return h
}

func (h *Harness) Name() string { return h.name }

// Spec returns the environment the run needs: the base image with its non-root
// identity, the workdir, any extra environment, the run labels, and the
// credential reference the backend should resolve.
func (h *Harness) Spec(req harness.Request) (isolation.Spec, error) {
	spec := isolation.Spec{
		Image: isolation.Image{
			Ref:  h.image,
			User: h.user,
		},
		Workdir: req.Workdir,
		Env:     h.launchEnv(req),
		Labels:  req.Labels,
	}
	// When the run carries receiver configuration, stage the ft msg plugin so
	// the launched session registers and claims messages. Without a configured
	// project/actor the plugin is not installed and the session is unmanaged.
	//
	// The plugin shells out to `ft` for the local transport, so the environment
	// must provide it: a sandboxed/containerized image needs `ft` on PATH, or
	// the run must set FACTOTUM_BIN to one it staged. A remote hub
	// (FACTOTUM_MSG_URL) needs no local ft.
	if MessagingEnabled(req.Env) {
		spec.Files = append(spec.Files, MsgPluginFile())
	}
	if h.provider != "" && h.credEnv != "" {
		spec.Credentials = []isolation.Credential{{
			Provider: h.provider,
			EnvVar:   h.credEnv,
		}}
	}
	return spec, nil
}

// Command builds the invocation for req. A headless run is `opencode run`, the
// model flag, any passthrough arguments, then `--` and the prompt as the final
// positional; the separator keeps a prompt that begins with a dash from being
// parsed as an option. An interactive run drops `run`, delivers the kickoff
// through `--prompt`, and marks the command as needing a terminal so the backend
// attaches the OpenCode TUI to it.
//
// V2 is launched with --standalone in both modes, so the run gets its own server
// rather than attaching to the shared background service: only a private server
// inherits the run's FACTOTUM_* environment, which the staged receiver plugin
// needs to register.
func (h *Harness) Command(req harness.Request) (isolation.Command, error) {
	if req.Prompt == "" {
		return isolation.Command{}, ErrNoPrompt
	}
	if req.Interactive {
		argv := make([]string, 0, len(h.args)+len(req.Args)+5)
		argv = append(argv, h.binary)
		argv = h.appendStandalone(argv)
		// V2's TUI has no top-level --model, so only emit it for a variant that
		// accepts it; V2 falls back to its configured default model.
		if h.interactiveModel {
			if model := firstNonEmpty(req.Model, h.model); model != "" {
				argv = append(argv, "--model", model)
			}
		}
		argv = append(argv, h.args...)
		argv = append(argv, req.Args...)
		argv = append(argv, "--prompt", req.Prompt)

		return isolation.Command{
			Argv:    argv,
			Env:     h.launchEnv(req),
			Workdir: req.Workdir,
			TTY:     true,
		}, nil
	}

	argv := make([]string, 0, len(h.args)+len(req.Args)+5)
	argv = append(argv, h.binary, "run")
	argv = h.appendStandalone(argv)
	if model := firstNonEmpty(req.Model, h.model); model != "" {
		argv = append(argv, "--model", model)
	}
	argv = append(argv, h.args...)
	argv = append(argv, req.Args...)
	argv = append(argv, "--", req.Prompt)

	return isolation.Command{
		Argv:    argv,
		Env:     h.launchEnv(req),
		Workdir: req.Workdir,
	}, nil
}

// appendStandalone adds the private-server flag when the variant needs one. A
// headless launch carries it after the `run` subcommand: V2 rejects a global flag
// placed before a subcommand (`opencode --standalone run` is "Unrecognized flag"
// on 2.0.27), and the post-subcommand form is the one every V2 build takes. The
// TUI has no subcommand, so there the flag goes directly after the binary, where
// both 2.0.x and 2.3.x accept it.
func (h *Harness) appendStandalone(argv []string) []string {
	if h.standalone {
		argv = append(argv, "--standalone")
	}
	return argv
}

// launchEnv returns the environment the launch needs: the run's own environment
// plus, for a V2 private server, the layer that keeps the location loaded. The
// layer is a whole config document set on the launch's environment only, so it
// never touches an OpenCode config file and the user's own sessions keep the
// default idle unload.
//
// A caller that already set OPENCODE_CONFIG or OPENCODE_CONFIG_CONTENT wins:
// merging arbitrary caller JSON here would be guesswork, and replacing it could
// drop the caller's own settings. Such a launch keeps the default idle unload,
// exactly as a caller-set layer does for a loop agent.
func (h *Harness) launchEnv(req harness.Request) map[string]string {
	env := clone(req.Env)
	if !h.standalone {
		return env
	}
	if env[envConfig] != "" || env[envConfigContent] != "" {
		return env
	}
	env[envConfigContent] = idleLocationLayer
	return env
}

// Result parses the agent's stdout: it trims surrounding whitespace and returns
// the text verbatim, including any line that looks like a completion marker.
func (h *Harness) Result(out []byte) (harness.Result, error) {
	return harness.Result{Output: strings.TrimSpace(string(out))}, nil
}

// LocateBinary resolves name on the host PATH. The local launcher uses it to
// find the dev-only host binary; an image backend does not need it, because the
// binary is already on PATH inside the image. Callers pass DefaultBinary (V1) or
// DefaultBinaryV2 (V2) for the variant they drive.
func LocateBinary(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("harness/opencode: locate %q: %w", name, err)
	}
	return path, nil
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

var _ harness.Harness = (*Harness)(nil)
