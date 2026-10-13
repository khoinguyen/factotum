package cli

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
)

func TestServeCommandSurface(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	cmd := newServeCommand(deps)

	if _, ok := builtinCommands().Lookup("serve"); !ok {
		t.Fatal("serve is not registered")
	}
	for _, name := range []string{"bind", "project", "all", "token"} {
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

// TestWarnIncompleteHub covers the warning `ft serve` (and `ft run`) emit when
// the [serve] table names a hub URL but no token: the transport is token-gated,
// so the receiver could never authenticate.
func TestWarnIncompleteHub(t *testing.T) {
	cases := []struct {
		name  string
		url   string
		token string
		want  bool
	}{
		{"url without token", "http://hub:8484", "", true},
		{"url with blank token", "http://hub:8484", "   ", true},
		{"url and token", "http://hub:8484", "tok", false},
		{"token without url", "", "tok", false},
		{"neither", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			deps := &Deps{Err: &stderr, Config: config.Config{Serve: config.Serve{URL: tc.url, Token: tc.token}}}
			deps.warnIncompleteHub()
			if got := strings.Contains(stderr.String(), "serve.token"); got != tc.want {
				t.Fatalf("warnIncompleteHub() warning = %v (%q), want %v", got, stderr.String(), tc.want)
			}
		})
	}
}

// TestResolveServeToken covers the effective-token rule: an explicitly
// configured token (flag, machine config, or FACTOTUM_SERVE_TOKEN, all already
// folded into the value passed here) is used as-is and never printed; with none
// and a scoped project a random one is generated for the run; an all-projects
// server has no capture target, so it stays off.
func TestResolveServeToken(t *testing.T) {
	gen := func() string { return "generated" }
	cases := []struct {
		name          string
		explicit      string
		all           bool
		want          string
		wantGenerated bool
	}{
		{"explicit wins", "s3cret", false, "s3cret", false},
		{"explicit wins while serving all projects", "s3cret", true, "s3cret", false},
		{"no token generates when scoped", "", false, "generated", true},
		{"blank explicit generates when scoped", "   ", false, "generated", true},
		{"no token stays off while serving all projects", "", true, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, generated := resolveServeToken(tc.explicit, tc.all, gen)
			if got != tc.want || generated != tc.wantGenerated {
				t.Fatalf("resolveServeToken(%q, %v) = (%q, %v), want (%q, %v)",
					tc.explicit, tc.all, got, generated, tc.want, tc.wantGenerated)
			}
		})
	}
}

// TestNewServeTokenIsRandom proves each start mints a fresh, non-empty token.
func TestNewServeTokenIsRandom(t *testing.T) {
	first, second := newServeToken(), newServeToken()
	if first == "" || second == "" {
		t.Fatalf("newServeToken() = (%q, %q), want both non-empty", first, second)
	}
	if first == second {
		t.Fatalf("newServeToken() returned the same token twice: %q", first)
	}
}

// TestServeAnnouncementPrintsOnlyGeneratedToken proves a generated token is
// echoed for the user to copy, while an explicit token is never printed.
func TestServeAnnouncementPrintsOnlyGeneratedToken(t *testing.T) {
	generated := serveAnnouncement("127.0.0.1:8484", false, "minted-token", true)
	if len(generated) != 2 {
		t.Fatalf("generated announcement has %d lines, want 2: %q", len(generated), generated)
	}
	if !strings.Contains(generated[0], "capture on") {
		t.Fatalf("announcement does not report capture on: %q", generated[0])
	}
	if !strings.Contains(generated[1], "minted-token") {
		t.Fatalf("announcement does not print the generated token: %q", generated[1])
	}

	explicit := serveAnnouncement("127.0.0.1:8484", false, "s3cret", false)
	if len(explicit) != 1 {
		t.Fatalf("explicit announcement has %d lines, want 1: %q", len(explicit), explicit)
	}
	if strings.Contains(strings.Join(explicit, "\n"), "s3cret") {
		t.Fatalf("announcement printed an explicit token:\n%q", explicit)
	}

	allProjects := serveAnnouncement("127.0.0.1:8484", true, "", false)
	if !strings.Contains(allProjects[0], "capture off") {
		t.Fatalf("all-projects announcement does not report capture off: %q", allProjects[0])
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
