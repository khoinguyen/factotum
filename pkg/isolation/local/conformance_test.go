package local_test

import (
	"io"
	"testing"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/conformance"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
)

// TestConformance runs the shared IsolationBackend contract against the host
// backend. It is opted in (AllowHost) and discards the unsandboxed warning, so
// the suite exercises the real lifecycle on the host. Every optional operation
// (Logs, ApplyPolicy, TTY, credentials) is unsupported here and must say so.
func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) isolation.IsolationBackend {
		return local.New(local.Options{AllowHost: true, Warn: io.Discard})
	})
}
