// Package cli maps domain errors to stable machine codes and exit codes so an
// agent can branch on cause without parsing prose.
package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

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
}

// PrintError writes err to w: a structured envelope for -o json|yaml, else a
// plain "ft: ..." line.
func PrintError(w io.Writer, format string, err error) {
	switch format {
	case "json":
		data, _ := json.Marshal(errorEnvelope{Code: ErrorCode(err), Message: err.Error()})
		_, _ = fmt.Fprintln(w, string(data))
	case "yaml":
		data, _ := yaml.Marshal(errorEnvelope{Code: ErrorCode(err), Message: err.Error()})
		_, _ = fmt.Fprint(w, string(data))
	default:
		_, _ = fmt.Fprintln(w, "ft:", err)
	}
}
