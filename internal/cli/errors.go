// Package cli maps domain errors to stable machine codes and exit codes so an
// agent can branch on cause without parsing prose.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/core"
)

// ErrorCode returns a stable machine code for err.
func ErrorCode(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, ErrUsage):
		return "usage"
	case errors.Is(err, core.ErrNotFound):
		return "not_found"
	case errors.Is(err, core.ErrAlreadyExists):
		return "already_exists"
	case errors.Is(err, core.ErrConflict):
		return "conflict"
	case errors.Is(err, core.ErrCycle):
		return "cycle"
	case errors.Is(err, core.ErrInvalid):
		return "invalid"
	default:
		return "internal"
	}
}

// ExitCode returns the process exit code for err: 2 usage, 3 not-found,
// 4 conflict, 5 invalid/cycle, 1 anything else.
func ExitCode(err error) int {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, ErrUsage):
		return 2
	case errors.Is(err, core.ErrNotFound):
		return 3
	case errors.Is(err, core.ErrConflict), errors.Is(err, core.ErrAlreadyExists):
		return 4
	case errors.Is(err, core.ErrInvalid), errors.Is(err, core.ErrCycle):
		return 5
	default:
		return 1
	}
}

type errorEnvelope struct {
	Code    string `json:"code" yaml:"code"`
	Message string `json:"message" yaml:"message"`
	// Stderr is the harness's bounded stderr tail when the failure came from a
	// run, matching the field `ft run` emits, so a machine consumer names why
	// instead of only the exit code.
	Stderr string `json:"stderr,omitempty" yaml:"stderr,omitempty"`
}

// stderrCarrier is a failure that carries the bounded stderr tail of the
// harness whose run failed.
type stderrCarrier interface {
	Stderr() string
}

// harnessFailure attaches a failed run's harness stderr tail to the run error,
// which the error text cannot hold. It unwraps to that error, so errors.Is and
// the exit-code mapping are unchanged.
type harnessFailure struct {
	err    error
	stderr string
}

func (f harnessFailure) Error() string  { return f.err.Error() }
func (f harnessFailure) Unwrap() error  { return f.err }
func (f harnessFailure) Stderr() string { return f.stderr }

// withHarnessStderr carries stderr alongside err into the structured error
// envelope. An empty tail leaves err untouched, so a failure without one keeps
// a bare envelope.
func withHarnessStderr(err error, stderr string) error {
	stderr = strings.TrimSpace(stderr)
	if err == nil || stderr == "" {
		return err
	}
	return harnessFailure{err: err, stderr: stderr}
}

// newErrorEnvelope maps err to its machine-readable payload.
func newErrorEnvelope(err error) errorEnvelope {
	envelope := errorEnvelope{Code: ErrorCode(err), Message: err.Error()}
	var carrier stderrCarrier
	if errors.As(err, &carrier) {
		envelope.Stderr = carrier.Stderr()
	}
	return envelope
}

// PrintError writes err to w: a structured envelope for -o json|yaml, else a
// plain "ft: ..." line.
func PrintError(w io.Writer, format string, err error) {
	switch format {
	case "json":
		data, _ := json.Marshal(newErrorEnvelope(err))
		_, _ = fmt.Fprintln(w, string(data))
	case "yaml":
		data, _ := yaml.Marshal(newErrorEnvelope(err))
		_, _ = fmt.Fprint(w, string(data))
	default:
		_, _ = fmt.Fprintln(w, "ft:", err)
	}
}
