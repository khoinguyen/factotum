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
	lower := strings.ToLower(cmd.Long)
	for _, want := range []string{"capture", "token"} {
		if !strings.Contains(lower, want) {
			t.Fatalf("serve help does not mention %q:\n%s", want, cmd.Long)
		}
	}
}

func TestServeModeReportsCaptureState(t *testing.T) {
	cases := []struct {
		name        string
		allProjects bool
		token       string
		want        string
	}{
		{"all projects", true, "s3cret", "capture off"},
		{"no token", false, "", "capture off"},
		{"scoped with token", false, "s3cret", "capture on"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := serveMode(tc.allProjects, tc.token); !strings.Contains(got, tc.want) {
				t.Fatalf("serveMode(%v, %q) = %q, want %q", tc.allProjects, tc.token, got, tc.want)
			}
		})
	}
}
