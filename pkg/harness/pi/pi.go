// Package pi is the pi harness for `ft run`: it drives the pi coding agent CLI
// headlessly, either as the host binary through the dev-only local backend or
// from a caller-supplied image through an isolating backend.
//
// It implements the harness port (pkg/harness) and speaks only the isolation
// vocabulary: Spec names the base image, entrypoint, and working identity;
// Command builds the headless invocation (`pi --print` with the model flag and
// the prompt as the final message) or, for an interactive request, the
// terminal-attached TUI invocation; Done and Result read the agent's output.
//
// pi ships as an npm package, not a published container image, so no image is
// assumed by default: a local run uses the host `pi` binary, and an isolating
// backend must set Options.Image. The model and its credentials are never
// hardcoded; the model comes from the configured provider (Options.Model,
// overridden by Request.Model) and a credential, when configured, is declared as
// a provider reference the isolation backend resolves.
package pi

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

// Name is the harness's registration key.
const Name = "pi"

const (
	// DefaultBinary is the argv[0] on the host PATH and inside a caller-supplied
	// image.
	DefaultBinary = "pi"
	// approveFlag trusts project-local resources for the run. pi loads
	// project-local extensions (the staged receiver lives at
	// .pi/extensions/factotum-msg.js) only after the project is trusted, and a
	// non-interactive run disables the trust prompt, so without this flag the
	// receiver would never load.
	approveFlag = "--approve"
	// printFlag runs pi's non-interactive mode: process the prompt and exit.
	printFlag = "--print"
)

// ErrNoPrompt means Command was called without a prompt: a headless run has
// nothing to execute.
var ErrNoPrompt = errors.New("harness/pi: no prompt")

// Options configures the harness. The zero value is usable: it targets the
// `pi` binary on PATH.
type Options struct {
	// Image overrides the base image reference for an isolating backend. It is
	// empty by default because pi ships as an npm package, not an image; a local
	// run ignores it.
	Image string
	// Binary is the argv[0] for the invocation: a host path resolved locally,
	// or the entrypoint name that exists inside Image. Defaults to
	// DefaultBinary.
	Binary string
	// User is the non-root identity the backend should run as, for a
	// caller-supplied Image. Empty leaves the image's own default.
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
	// Sentinel marks the run complete when it appears in the agent's output. It
	// is empty by default, so completion is then the process exit.
	Sentinel string
	// Args are extra arguments passed through before the prompt, e.g.
	// {"--no-session"} or {"--thinking", "high"}.
	Args []string
}

// Harness is the pi implementation of harness.Harness.
type Harness struct {
	image    string
	binary   string
	user     string
	model    string
	provider string
	credEnv  string
	sentinel string
	args     []string
}

// New returns a pi harness with defaults applied.
func New(opts Options) *Harness {
	return &Harness{
		image:    opts.Image,
		binary:   firstNonEmpty(opts.Binary, DefaultBinary),
		user:     opts.User,
		model:    opts.Model,
		provider: opts.Provider,
		credEnv:  opts.CredentialEnvVar,
		sentinel: opts.Sentinel,
		args:     append([]string(nil), opts.Args...),
	}
}

func (h *Harness) Name() string { return Name }

// Spec returns the environment the run needs: the base image and identity, the
// workdir, any extra environment, the run labels, and the credential reference
// the backend should resolve.
func (h *Harness) Spec(req harness.Request) (isolation.Spec, error) {
	spec := isolation.Spec{
		Image: isolation.Image{
			Ref:  h.image,
			User: h.user,
		},
		Workdir: req.Workdir,
		Env:     clone(req.Env),
		Labels:  req.Labels,
	}
	// When the run carries receiver configuration, stage the ft msg extension so
	// the launched session registers and claims messages. Without a configured
	// project/actor the extension is not installed and the session is unmanaged.
	if MessagingEnabled(req.Env) {
		spec.Files = append(spec.Files, MsgExtensionFile())
	}
	if h.provider != "" && h.credEnv != "" {
		spec.Credentials = []isolation.Credential{{
			Provider: h.provider,
			EnvVar:   h.credEnv,
		}}
	}
	return spec, nil
}

// Command builds the invocation for req. A headless run is `pi --print` with the
// model flag and any passthrough arguments, then `--` and the prompt as the final
// message; the separator keeps a prompt that begins with a dash from being parsed
// as an option. An interactive run drops `--print`, delivers the kickoff as the
// initial message, and marks the command as needing a terminal so the backend
// attaches the pi TUI to it. Both carry `--approve` so project-local resources,
// including the staged receiver extension, load.
func (h *Harness) Command(req harness.Request) (isolation.Command, error) {
	if req.Prompt == "" {
		return isolation.Command{}, ErrNoPrompt
	}
	if req.Interactive {
		argv := make([]string, 0, len(h.args)+len(req.Args)+6)
		argv = append(argv, h.binary, approveFlag)
		if model := firstNonEmpty(req.Model, h.model); model != "" {
			argv = append(argv, "--model", model)
		}
		argv = append(argv, h.args...)
		argv = append(argv, req.Args...)
		argv = append(argv, "--", req.Prompt)

		return isolation.Command{
			Argv:    argv,
			Env:     clone(req.Env),
			Workdir: req.Workdir,
			TTY:     true,
		}, nil
	}

	argv := make([]string, 0, len(h.args)+len(req.Args)+7)
	argv = append(argv, h.binary, approveFlag, printFlag)
	if model := firstNonEmpty(req.Model, h.model); model != "" {
		argv = append(argv, "--model", model)
	}
	argv = append(argv, h.args...)
	argv = append(argv, req.Args...)
	argv = append(argv, "--", req.Prompt)

	return isolation.Command{
		Argv:    argv,
		Env:     clone(req.Env),
		Workdir: req.Workdir,
	}, nil
}

// Done reports whether ev is the configured completion sentinel. It matches
// within a single streamed event, so Result on the full stdout is the
// authoritative completion check. With no sentinel, Done always returns false
// and the run completes on process exit.
func (h *Harness) Done(ev isolation.Event) bool {
	if h.sentinel == "" || ev.Kind != isolation.EventOutput {
		return false
	}
	return strings.Contains(ev.Message, h.sentinel)
}

// Result parses the agent's stdout: it trims whitespace, removes the completion
// sentinel if present, and reports whether that sentinel fired.
func (h *Harness) Result(out []byte) (harness.Result, error) {
	text := strings.TrimSpace(string(out))
	complete := false
	if h.sentinel != "" && strings.Contains(text, h.sentinel) {
		complete = true
		text = strings.TrimSpace(strings.ReplaceAll(text, h.sentinel, ""))
	}
	return harness.Result{Output: text, Complete: complete}, nil
}

// LocateBinary resolves name on the host PATH. The local launcher uses it to
// find the host binary; an image backend does not need it, because the binary
// is already on PATH inside the image.
func LocateBinary(name string) (string, error) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("harness/pi: locate %q: %w", name, err)
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
