// Package opencode is the OpenCode harness for `ft run`: it drives the OpenCode
// agent CLI headlessly, either as the host binary through the dev-only local
// backend or from the shipped container image through an isolating backend such
// as OpenShell.
//
// It implements the harness port (pkg/harness) and speaks only the isolation
// vocabulary: Spec names the base image and working identity;
// Command builds the headless invocation (opencode run with the model flag and
// the prompt as the final positional) or, for an interactive request, the
// terminal-attached TUI invocation; Done and Result read the agent's output.
//
// The model and its credentials are never hardcoded. The model comes from the
// configured provider (Options.Model, overridden by Request.Model) and is passed
// through as --model; a credential, when configured, is declared as a provider
// reference with its environment variable name, and the isolation backend
// resolves the secret. No secret value is read or written here.
package opencode

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

// Name is the harness's registration key.
const Name = "opencode"

const (
	// DefaultImage is the OpenCode agent image (Alpine, entrypoint `opencode`).
	// It declares no USER, so the image runs as root; DefaultUser carries the
	// non-root identity a backend should request instead.
	DefaultImage = "ghcr.io/anomalyco/opencode:latest"
	// DefaultBinary is the argv[0] on the host PATH and inside the image.
	DefaultBinary = "opencode"
	// DefaultUser is the non-root identity the workload should run as. The
	// shipped image has no interactive home for this uid, so a backend must
	// provide a writable HOME (for example /tmp).
	DefaultUser = "1000:1000"
)

// ErrNoPrompt means Command was called without a prompt: a headless run has
// nothing to execute.
var ErrNoPrompt = errors.New("harness/opencode: no prompt")

// Options configures the harness. The zero value is usable: it targets the
// default image and the `opencode` binary on PATH.
type Options struct {
	// Image overrides the base image reference. Defaults to DefaultImage.
	Image string
	// Binary is the argv[0] for the invocation: a host path resolved locally,
	// or the binary name that exists inside Image. Defaults to
	// DefaultBinary.
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
	// Sentinel marks the run complete when it appears in the agent's output. It
	// is empty by default, so completion is then the process exit.
	Sentinel string
	// Args are extra arguments passed through before the prompt, e.g.
	// {"--agent", "build"} or {"--auto"} to let the agent act without
	// permission prompts.
	Args []string
}

// Harness is the OpenCode implementation of harness.Harness.
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

// New returns an OpenCode harness with defaults applied.
func New(opts Options) *Harness {
	h := &Harness{
		image:    firstNonEmpty(opts.Image, DefaultImage),
		binary:   firstNonEmpty(opts.Binary, DefaultBinary),
		user:     opts.User,
		model:    opts.Model,
		provider: opts.Provider,
		credEnv:  opts.CredentialEnvVar,
		sentinel: opts.Sentinel,
		args:     append([]string(nil), opts.Args...),
	}
	if opts.User == "" && opts.Image == "" {
		h.user = DefaultUser
	}
	return h
}

func (h *Harness) Name() string { return Name }

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
		Env:     clone(req.Env),
		Labels:  req.Labels,
	}
	// When the run carries receiver configuration, stage the ft msg plugin so
	// the launched session registers and claims messages. Without a configured
	// project/actor the plugin is not installed and the session is unmanaged.
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
func (h *Harness) Command(req harness.Request) (isolation.Command, error) {
	if req.Prompt == "" {
		return isolation.Command{}, ErrNoPrompt
	}
	if req.Interactive {
		argv := make([]string, 0, len(h.args)+len(req.Args)+5)
		argv = append(argv, h.binary)
		if model := firstNonEmpty(req.Model, h.model); model != "" {
			argv = append(argv, "--model", model)
		}
		argv = append(argv, h.args...)
		argv = append(argv, req.Args...)
		argv = append(argv, "--prompt", req.Prompt)

		return isolation.Command{
			Argv:    argv,
			Env:     clone(req.Env),
			Workdir: req.Workdir,
			TTY:     true,
		}, nil
	}

	argv := make([]string, 0, len(h.args)+len(req.Args)+5)
	argv = append(argv, h.binary, "run")
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
// find the dev-only host binary; an image backend does not need it, because the
// binary is already on PATH inside the image.
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
