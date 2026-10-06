package cli

import (
	"io"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
)

// TestRunBackendsIncludeOpenShell pins that the OpenShell isolation backend is
// a registered, selectable `ft run` backend alongside the dev-only local host
// backend, and that its factory constructs without error.
func TestRunBackendsIncludeOpenShell(t *testing.T) {
	reg := runBackends()
	names := map[string]bool{}
	for _, name := range reg.Names() {
		names[name] = true
	}
	for _, want := range []string{"local", "openshell"} {
		if !names[want] {
			t.Fatalf("run backends %v missing %q", reg.Names(), want)
		}
	}
	factory, err := reg.MustLookup("openshell")
	if err != nil {
		t.Fatalf("lookup openshell: %v", err)
	}
	backend, err := factory(config.Run{}, io.Discard)
	if err != nil {
		t.Fatalf("build openshell backend: %v", err)
	}
	if backend.Name() != "openshell" {
		t.Fatalf("backend.Name() = %q, want openshell", backend.Name())
	}
}
