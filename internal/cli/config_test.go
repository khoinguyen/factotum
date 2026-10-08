package cli

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// seedProjectConfig seeds the runner's project config so `ft config get` has a
// resolved project (and tech stack) to report.
func seedProjectConfig(t *testing.T, r *runner, body string) {
	t.Helper()
	if err := os.WriteFile(r.projectPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", r.projectPath, err)
	}
}

// TestConfigGetSurfacesTechStack pins the acceptance behavior: the recorded
// tech_stack is readable through the CLI, not write-only.
func TestConfigGetSurfacesTechStack(t *testing.T) {
	r := newRunner(t)
	seedProjectConfig(t, r, "project = \"widget\"\ntech_stack = \"rust\"\n")

	out := r.run("config", "get")
	if !strings.Contains(out, "tech_stack: rust") {
		t.Fatalf("config get missing tech_stack:\n%s", out)
	}
	if !strings.Contains(out, "project: widget") {
		t.Fatalf("config get missing project:\n%s", out)
	}
}

func TestConfigGetStructuredEmitsTechStack(t *testing.T) {
	r := newRunner(t)
	seedProjectConfig(t, r, "project = \"widget\"\ntech_stack = \"rust\"\n")

	out := r.run("config", "get", "-o", "json")
	if !strings.Contains(out, `"tech_stack": "rust"`) {
		t.Fatalf("config get -o json missing tech_stack:\n%s", out)
	}
}

func TestConfigGetKeySelectsOneField(t *testing.T) {
	r := newRunner(t)
	seedProjectConfig(t, r, "project = \"widget\"\ntech_stack = \"rust\"\n")

	if got := r.run("config", "get", "tech_stack"); got != "tech_stack: rust\n" {
		t.Fatalf("config get tech_stack = %q, want one field line", got)
	}
}

func TestConfigGetRejectsUnknownKey(t *testing.T) {
	r := newRunner(t)
	seedProjectConfig(t, r, "project = \"widget\"\n")

	if err := r.runErr("config", "get", "nope"); !errors.Is(err, ErrUsage) {
		t.Fatalf("config get nope error = %v, want ErrUsage", err)
	}
}
