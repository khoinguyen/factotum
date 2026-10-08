package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestWarnLocalReposWarnsOnlyForPathRepos pins the classifier: a repository
// configured with a Path is used in place, while one with a URL is cloned into
// the workspace, so only the Path repo is warned about.
func TestWarnLocalReposWarnsOnlyForPathRepos(t *testing.T) {
	var buf bytes.Buffer
	d := &Deps{Err: &buf}
	d.warnLocalRepos([]core.Repository{
		{Name: "web", URL: "https://example.com/acme/web.git"},
		{Name: "api", Path: "/checkouts/api"},
	})
	out := buf.String()
	if !strings.Contains(out, "api") || !strings.Contains(out, "/checkouts/api") {
		t.Fatalf("warning did not name the local-path repo:\n%s", out)
	}
	if strings.Contains(out, "web") {
		t.Fatalf("warning named a URL repo, want only local paths:\n%s", out)
	}
}

// TestRunWarnsWhenLocalCheckoutUsedInPlace drives `ft run` against a task whose
// repo is a local path and asserts the user is warned on stderr that the source
// checkout is used in place (workspace.Resolve does not copy it), so a run may
// modify the source.
func TestRunWarnsWhenLocalCheckoutUsedInPlace(t *testing.T) {
	r := newRunner(t)
	projectID := firstField(t, r.run("project", "create", "Acme"))
	repoPath := t.TempDir()
	r.run("project", "repo", "create", projectID, "web", "--path", repoPath)
	taskID := firstField(t, r.run("task", "create", "--project", projectID, "--repo", "web", "--title", "Add widget"))

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	_, stderr := r.runSplit("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	if !strings.Contains(stderr, "web") || !strings.Contains(stderr, repoPath) {
		t.Fatalf("run did not warn about the local checkout on stderr:\n%s", stderr)
	}
	if !strings.Contains(stderr, "in place") {
		t.Fatalf("warning did not say the checkout is used in place:\n%s", stderr)
	}
}
