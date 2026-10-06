// Package harness defines the pluggable coding-agent harness port for `ft run`.
//
// A Harness knows one agent CLI end to end: its image and entrypoint, the
// invocation and model flag, how the prompt reaches the process, how a run is
// detected as complete, and how its output is parsed. It describes all of that
// in the vocabulary of the isolation port (pkg/isolation), so any backend can
// run any harness without either side knowing the other by name.
//
// Harnesses are registrations, not a hardcoded switch: concrete adapters live in
// subpackages (for example pkg/harness/opencode, pkg/harness/claude) and
// register into a Registry. No harness-specific type appears in this package.
//
// The dependency is one way, harness -> isolation: a harness builds an
// isolation.Spec for Prepare and an isolation.Command for Exec. isolation never
// imports harness, so the two ports stay independently swappable.
package harness

import (
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/registry"
)

// Request is one agent run: the instruction and the knobs around it.
type Request struct {
	// Prompt is the instruction the agent executes.
	Prompt string
	// Model optionally overrides the harness's default model.
	Model string
	// Workdir is the directory the agent runs in; empty means the environment's
	// default.
	Workdir string
	// Env is extra non-secret environment for the run.
	Env map[string]string
	// Args are extra CLI arguments passed through to the harness.
	Args []string
	// Labels are backend-agnostic identifiers (project, task) for the run.
	Labels map[string]string
}

// Result is what a harness extracts from a completed run.
type Result struct {
	// Output is the agent's final output, however the harness defines it.
	Output string
	// ExitCode is the command's exit status.
	ExitCode int
	// Complete reports whether the harness's completion detection fired (as
	// opposed to the process merely exiting).
	Complete bool
}

// Harness is the port. Implementations must be safe for concurrent use.
type Harness interface {
	// Name is the registration key.
	Name() string
	// Spec returns the environment the harness needs: base image, entrypoint,
	// working directory, and any files staged before the run.
	Spec(req Request) (isolation.Spec, error)
	// Command returns the invocation for req, with the model flag and prompt
	// delivery already resolved into the isolation.Command.
	Command(req Request) (isolation.Command, error)
	// Done reports whether an event marks the run complete. A harness with no
	// completion sentinel returns false; the run then completes on process exit.
	Done(ev isolation.Event) bool
	// Result parses the agent's output into a Result.
	Result(out []byte) (Result, error)
}

// Registry is the plugin registry for harnesses.
type Registry = registry.Registry[Harness]

// NewRegistry returns an empty harness registry.
func NewRegistry() *Registry { return registry.New[Harness]() }
