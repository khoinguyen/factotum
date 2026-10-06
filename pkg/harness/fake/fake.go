// Package fake is a deterministic harness for tests. It builds specs and
// commands in memory and never spawns an agent.
package fake

import (
	"path/filepath"
	"strings"

	"github.com/khoinguyen/factotum/pkg/harness"
	"github.com/khoinguyen/factotum/pkg/isolation"
)

// Delivery says how the fake harness hands the prompt to the process.
type Delivery int

const (
	// DeliveryArg appends the prompt as a positional argument.
	DeliveryArg Delivery = iota
	// DeliveryStdin writes the prompt to the process's stdin.
	DeliveryStdin
	// DeliveryEnv passes the prompt in the PROMPT variable.
	DeliveryEnv
	// DeliveryFile stages the prompt as a file and references it by argument.
	DeliveryFile
)

// Harness is an in-memory Harness.
type Harness struct {
	name     string
	image    string
	delivery Delivery
	sentinel string
	fail     error
}

// New returns a fake harness named name. It delivers the prompt as an argument
// and uses no completion sentinel until configured otherwise.
func New(name string) *Harness {
	return &Harness{name: name, delivery: DeliveryArg}
}

// WithImage sets the harness's base image reference.
func (h *Harness) WithImage(ref string) *Harness {
	h.image = ref
	return h
}

// WithDelivery sets how the prompt reaches the process.
func (h *Harness) WithDelivery(d Delivery) *Harness {
	h.delivery = d
	return h
}

// WithSentinel sets the output marker that marks the run complete.
func (h *Harness) WithSentinel(s string) *Harness {
	h.sentinel = s
	return h
}

// FailWith makes Spec, Command, and Result return err.
func (h *Harness) FailWith(err error) *Harness {
	h.fail = err
	return h
}

func (h *Harness) Name() string { return h.name }

func (h *Harness) Spec(req harness.Request) (isolation.Spec, error) {
	if h.fail != nil {
		return isolation.Spec{}, h.fail
	}
	spec := isolation.Spec{
		Image:   isolation.Image{Ref: h.image},
		Workdir: req.Workdir,
		Env:     req.Env,
		Labels:  req.Labels,
	}
	if h.delivery == DeliveryFile {
		spec.Files = append(spec.Files, isolation.File{
			Path:    h.promptPath(req),
			Content: []byte(req.Prompt),
			Mode:    0o600,
		})
	}
	return spec, nil
}

func (h *Harness) Command(req harness.Request) (isolation.Command, error) {
	if h.fail != nil {
		return isolation.Command{}, h.fail
	}
	cmd := isolation.Command{
		Argv:    []string{h.name},
		Env:     clone(req.Env),
		Workdir: req.Workdir,
	}
	if req.Model != "" {
		cmd.Argv = append(cmd.Argv, "--model", req.Model)
	}
	cmd.Argv = append(cmd.Argv, req.Args...)
	switch h.delivery {
	case DeliveryStdin:
		cmd.Stdin = []byte(req.Prompt)
	case DeliveryEnv:
		cmd.Env["PROMPT"] = req.Prompt
	case DeliveryFile:
		cmd.Argv = append(cmd.Argv, h.promptPath(req))
	default:
		cmd.Argv = append(cmd.Argv, req.Prompt)
	}
	return cmd, nil
}

func (h *Harness) Done(ev isolation.Event) bool {
	return h.sentinel != "" && strings.Contains(ev.Message, h.sentinel)
}

func (h *Harness) Result(out []byte) (harness.Result, error) {
	if h.fail != nil {
		return harness.Result{}, h.fail
	}
	text := strings.TrimSpace(string(out))
	if h.sentinel != "" {
		if i := strings.Index(text, h.sentinel); i >= 0 {
			text = strings.TrimSpace(text[:i])
		}
	}
	return harness.Result{Output: text}, nil
}

func (h *Harness) promptPath(req harness.Request) string {
	return filepath.Join(req.Workdir, "prompt.txt")
}

func clone(in map[string]string) map[string]string {
	out := make(map[string]string, len(in)+1)
	for k, v := range in {
		out[k] = v
	}
	return out
}

var _ harness.Harness = (*Harness)(nil)
