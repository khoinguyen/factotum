package cli

import (
	"io"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
)

func TestServeCommandSurface(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	cmd := newServeCommand(deps)

	if _, ok := builtinCommands().Lookup("serve"); !ok {
		t.Fatal("serve is not registered")
	}
	for _, name := range []string{"bind", "project", "all"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("serve flag --%s is missing", name)
		}
	}
	if bind := cmd.Flags().Lookup("bind").DefValue; !strings.HasPrefix(bind, "127.0.0.1") {
		t.Fatalf("default bind = %q, want a loopback address", bind)
	}
	if !strings.Contains(strings.ToLower(cmd.Long), "read-only") {
		t.Fatalf("serve help does not state it is read-only:\n%s", cmd.Long)
	}
}
