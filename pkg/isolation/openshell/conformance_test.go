package openshell_test

import (
	"os"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/conformance"
	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
)

// TestConformance runs the shared IsolationBackend contract against the
// OpenShell backend on a live gateway. It is opt-in because it needs the
// openshell CLI and a reachable gateway; CI has neither. Run it with:
//
//	FACTOTUM_OPENSHELL_TEST=1 mise run test-openshell
//
// Every subtest gets a fresh backend with a random sandbox name, and the suite
// deletes each sandbox it prepares.
func TestConformance(t *testing.T) {
	if os.Getenv("FACTOTUM_OPENSHELL_TEST") == "" {
		t.Skip("set FACTOTUM_OPENSHELL_TEST=1 with a running OpenShell gateway to run the live conformance suite")
	}
	conformance.Run(t, func(t *testing.T) isolation.IsolationBackend {
		return openshell.New(openshell.Options{
			ReadyTimeout: 3 * time.Minute,
		})
	}, conformance.WithLogs(), conformance.WithPolicy())
}
