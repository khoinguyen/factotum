package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/workspace"
)

// TestWarnLocalPlanWarnsOnlyForLocalCheckouts pins the classifier: it reads the
// resolved plan, so it warns exactly for a checkout the resolver used in place
// (OriginLocal) and stays silent for a clone — independent of how a repository
// was configured.
func TestWarnLocalPlanWarnsOnlyForLocalCheckouts(t *testing.T) {
	var buf bytes.Buffer
	d := &Deps{Err: &buf}
	d.warnLocalPlan(&workspace.Plan{Checkouts: []workspace.Checkout{
		{Name: "web", Path: "/workspace/web", Origin: workspace.OriginClone},
		{Name: "api", Path: "/checkouts/api", Origin: workspace.OriginLocal},
	}})
	out := buf.String()
	if !strings.Contains(out, "api") || !strings.Contains(out, "/checkouts/api") {
		t.Fatalf("warning did not name the local checkout:\n%s", out)
	}
	if strings.Contains(out, "web") {
		t.Fatalf("warning named a cloned checkout, want only local ones:\n%s", out)
	}
}

// TestRunWarnsLocalCheckoutWithResolvedPath drives `ft run` against a task whose
// repo is a relative local Path and asserts the warning names the resolved
// absolute checkout, not the configured relative string.
func TestRunWarnsLocalCheckoutWithResolvedPath(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	rel := "checkout-api"
	resolved := filepath.Join(filepath.Dir(r.projectPath), rel)
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		t.Fatal(err)
	}
	r.run("project", "repo", "create", projectID, "api", "--path", rel)
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--repo", "api", "--title", "Add widget"))

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(stderr, "api") || !strings.Contains(stderr, resolved) {
		t.Fatalf("run did not warn naming the resolved checkout %q on stderr:\n%s", resolved, stderr)
	}
	if !strings.Contains(stderr, "in place") {
		t.Fatalf("warning did not say the checkout is used in place:\n%s", stderr)
	}
}

// TestGroomWarnsWhenLocalCheckoutUsedInPlace proves `ft groom` emits the same
// in-place local-path warning as `ft run`: a groom resolves every project repo,
// so a local Path is used in place and a backend on the workdir may modify it.
// The configured repo Path is relative, so the warning names the resolved
// absolute checkout — not the raw config value and not merely the repo name.
func TestGroomWarnsWhenLocalCheckoutUsedInPlace(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	rel := "checkout-api"
	resolved := filepath.Join(filepath.Dir(r.projectPath), rel)
	if err := os.MkdirAll(resolved, 0o755); err != nil {
		t.Fatal(err)
	}
	r.run("project", "repo", "create", projectID, "api", "--path", rel)
	r.run("idea", "create", "-p", projectID, "-t", "Maybe cache")
	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("groom", "-p", projectID, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(stderr, resolved) || !strings.Contains(stderr, "in place") {
		t.Fatalf("groom did not warn naming the resolved checkout %q on stderr:\n%s", resolved, stderr)
	}
}
