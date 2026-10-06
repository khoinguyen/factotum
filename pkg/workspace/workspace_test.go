package workspace_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/workspace"
)

func testProject(repos ...core.Repository) core.Project {
	return core.Project{ID: "acme", Name: "Acme", Repos: repos, Policy: core.DefaultResolutionPolicy()}
}

func testTask(repo string) core.Task {
	return core.Task{
		ID:        "t-1",
		ProjectID: "acme",
		Repo:      repo,
		Kind:      core.KindTask,
		Title:     "do the thing",
		Status:    core.StatusTodo,
	}
}

// fakeGit records clone requests and simulates the side effect a real clone has:
// creating dir with a .git marker so re-runs detect an existing checkout.
type fakeGit struct {
	calls []workspace.CloneRequest
	err   error
}

func (f *fakeGit) Clone(_ context.Context, req workspace.CloneRequest) error {
	f.calls = append(f.calls, req)
	if f.err != nil {
		return f.err
	}
	if err := os.MkdirAll(filepath.Join(req.Dir, ".git"), 0o755); err != nil {
		return err
	}
	return nil
}

func TestResolveClonesMissingRepoIntoDeterministicDir(t *testing.T) {
	root := t.TempDir()
	git := &fakeGit{}
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git"})

	plan, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: git})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := len(plan.Checkouts), 1; got != want {
		t.Fatalf("checkouts = %d, want %d", got, want)
	}
	wantPath := filepath.Join(root, "backend")
	got := plan.Checkouts[0]
	if got.Path != wantPath {
		t.Errorf("checkout path = %q, want %q", got.Path, wantPath)
	}
	if got.Origin != workspace.OriginClone {
		t.Errorf("origin = %v, want OriginClone", got.Origin)
	}
	if got.Reused {
		t.Errorf("Reused = true, want a fresh clone")
	}
	if len(git.calls) != 1 || git.calls[0].URL != "https://example.com/acme/backend.git" || git.calls[0].Dir != wantPath {
		t.Fatalf("clone calls = %+v", git.calls)
	}
}

func TestResolveIsIdempotent(t *testing.T) {
	root := t.TempDir()
	git := &fakeGit{}
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git"})

	first, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: git})
	if err != nil {
		t.Fatalf("first Resolve() error = %v", err)
	}
	second, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: git})
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if len(git.calls) != 1 {
		t.Fatalf("clone calls = %d, want 1 (second run must reuse)", len(git.calls))
	}
	if !second.Checkouts[0].Reused {
		t.Errorf("second run Reused = false, want true")
	}
	if second.Checkouts[0].Path != first.Checkouts[0].Path {
		t.Errorf("second path = %q, want %q", second.Checkouts[0].Path, first.Checkouts[0].Path)
	}
}

func TestResolveUsesExistingLocalPath(t *testing.T) {
	root := t.TempDir()
	local := t.TempDir()
	git := &fakeGit{}
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git", Path: local})

	plan, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: git})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	got := plan.Checkouts[0]
	if got.Path != local {
		t.Errorf("path = %q, want the local path %q", got.Path, local)
	}
	if got.Origin != workspace.OriginLocal {
		t.Errorf("origin = %v, want OriginLocal", got.Origin)
	}
	if len(git.calls) != 0 {
		t.Errorf("clone calls = %d, want 0 for a local path", len(git.calls))
	}
}

func TestResolveErrorsWhenLocalPathMissing(t *testing.T) {
	root := t.TempDir()
	project := testProject(core.Repository{Name: "backend", Path: filepath.Join(root, "nope")})

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrLocalPath) {
		t.Fatalf("Resolve() error = %v, want ErrLocalPath", err)
	}
}

func TestResolveErrorsWhenLocalPathRelative(t *testing.T) {
	root := t.TempDir()
	project := testProject(core.Repository{Name: "backend", Path: "repos/backend"})

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrLocalPath) {
		t.Fatalf("Resolve() error = %v, want ErrLocalPath", err)
	}
}

func TestResolveErrorsWhenRepoHasNoSource(t *testing.T) {
	root := t.TempDir()
	project := testProject(core.Repository{Name: "backend"})

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrNoSource) {
		t.Fatalf("Resolve() error = %v, want ErrNoSource", err)
	}
	if !strings.Contains(err.Error(), "backend") {
		t.Errorf("error %q does not name the repository", err)
	}
}

func TestResolveErrorsWhenCloneTargetIsNotACheckout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git"})

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrGitDir) {
		t.Fatalf("Resolve() error = %v, want ErrGitDir", err)
	}
}

func TestResolveErrorsWhenCloneTargetIsAFile(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "backend"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git"})

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrGitDir) {
		t.Fatalf("Resolve() error = %v, want ErrGitDir", err)
	}
}

func TestResolveErrorsWhenTaskNamesUnknownRepo(t *testing.T) {
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git"})

	_, err := workspace.Resolve(context.Background(), project, testTask("data"), workspace.Options{Root: t.TempDir(), Git: &fakeGit{}})
	if !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Resolve() error = %v, want core.ErrInvalid", err)
	}
}

func TestResolveErrorsWhenRepoDirsCollide(t *testing.T) {
	root := t.TempDir()
	project := testProject(
		core.Repository{Name: "a/b", URL: "https://example.com/acme/one.git"},
		core.Repository{Name: "a:b", URL: "https://example.com/acme/two.git"},
	)

	_, err := workspace.Resolve(context.Background(), project, testTask(""), workspace.Options{Root: root, Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrRepoDirConflict) {
		t.Fatalf("Resolve() error = %v, want ErrRepoDirConflict", err)
	}
}

func TestResolveErrorsWhenRootMissing(t *testing.T) {
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git"})

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: "relative/root", Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrRoot) {
		t.Fatalf("Resolve() error = %v, want ErrRoot", err)
	}
}

func TestResolveErrorsWhenNoRepos(t *testing.T) {
	_, err := workspace.Resolve(context.Background(), testProject(), testTask(""), workspace.Options{Root: t.TempDir(), Git: &fakeGit{}})
	if !errors.Is(err, workspace.ErrNoRepos) {
		t.Fatalf("Resolve() error = %v, want ErrNoRepos", err)
	}
}

func TestResolveTaskWithoutRepoMaterializesAllProjectRepos(t *testing.T) {
	root := t.TempDir()
	git := &fakeGit{}
	project := testProject(
		core.Repository{Name: "backend", URL: "https://example.com/acme/backend.git"},
		core.Repository{Name: "web", URL: "https://example.com/acme/web.git"},
	)

	plan, err := workspace.Resolve(context.Background(), project, testTask(""), workspace.Options{Root: root, Git: git})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(plan.Checkouts) != 2 {
		t.Fatalf("checkouts = %d, want 2", len(plan.Checkouts))
	}
	if plan.Checkouts[0].Name != "backend" || plan.Checkouts[1].Name != "web" {
		t.Errorf("checkout order = %s,%s, want backend,web", plan.Checkouts[0].Name, plan.Checkouts[1].Name)
	}
}

func TestResolvePassesDeliberateTokenToGit(t *testing.T) {
	root := t.TempDir()
	git := &fakeGit{}
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/private.git"})
	token := stubToken{cred: workspace.Credential{Username: "x-access-token", Token: "s3cret"}}

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: git, Token: token})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if len(git.calls) != 1 || git.calls[0].Cred == nil {
		t.Fatalf("clone calls = %+v, want one with a credential", git.calls)
	}
	if git.calls[0].Cred.Token != "s3cret" {
		t.Errorf("token = %q, want s3cret", git.calls[0].Cred.Token)
	}
}

func TestResolvePropagatesTokenProviderError(t *testing.T) {
	root := t.TempDir()
	project := testProject(core.Repository{Name: "backend", URL: "https://example.com/acme/private.git"})
	token := stubToken{err: errors.New("no credential for host")}

	_, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root, Git: &fakeGit{}, Token: token})
	if err == nil || !strings.Contains(err.Error(), "no credential for host") {
		t.Fatalf("Resolve() error = %v, want the provider error", err)
	}
}

func TestRepoDir(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"backend", "backend"},
		{"github:org/repo", "github-org-repo"},
		{"a b", "a-b"},
		{"..", "repo"},
		{"", "repo"},
	}
	for _, tt := range tests {
		if got := workspace.RepoDir(tt.name); got != tt.want {
			t.Errorf("RepoDir(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

type stubToken struct {
	cred workspace.Credential
	ok   bool
	err  error
}

func (s stubToken) Token(_ context.Context, _ string) (workspace.Credential, bool, error) {
	if s.err != nil {
		return workspace.Credential{}, false, s.err
	}
	ok := s.ok || s.cred.Token != ""
	return s.cred, ok, nil
}

func TestResolveClonesRealRepoWithRealGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	src := t.TempDir()
	runGit(t, src, "init", "-q")
	if err := os.WriteFile(filepath.Join(src, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, src, "add", "README.md")
	runGit(t, src, "-c", "user.email=test@example.com", "-c", "user.name=Test", "commit", "-q", "-m", "init")

	root := t.TempDir()
	project := testProject(core.Repository{Name: "backend", URL: "file://" + src})

	plan, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	checkout := plan.Checkouts[0].Path
	if _, err := os.Stat(filepath.Join(checkout, ".git")); err != nil {
		t.Fatalf("cloned checkout has no .git: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(checkout, "README.md")); err != nil || string(data) != "hello\n" {
		t.Fatalf("README.md = %q, %v", data, err)
	}

	again, err := workspace.Resolve(context.Background(), project, testTask("backend"), workspace.Options{Root: root})
	if err != nil {
		t.Fatalf("second Resolve() error = %v", err)
	}
	if !again.Checkouts[0].Reused {
		t.Errorf("second run Reused = false, want true")
	}
}

func TestExecGitEnvIsDeliberate(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "env.txt")
	userOut := filepath.Join(dir, "user.txt")
	passOut := filepath.Join(dir, "pass.txt")
	fakeGit := filepath.Join(dir, "git")
	script := "#!/bin/sh\n" +
		"env > " + shellQuote(capture) + "\n" +
		"\"$GIT_ASKPASS\" 'Username for https://example.com' > " + shellQuote(userOut) + "\n" +
		"\"$GIT_ASKPASS\" 'Password for https://example.com' > " + shellQuote(passOut) + "\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	t.Setenv("GIT_SSH_COMMAND", "ssh -i /host/key")
	t.Setenv("GIT_DIR", "/host/repo")
	hostHome := os.Getenv("HOME")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	cred := &workspace.Credential{Username: "x-access-token", Token: "s3cret"}
	if err := workspace.NewExecGit().Clone(context.Background(), workspace.CloneRequest{
		URL:  "https://example.com/acme/private.git",
		Dir:  filepath.Join(dir, "checkout"),
		Cred: cred,
	}); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}

	env := readFile(t, capture)
	for _, want := range []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=", "FT_GIT_TOKEN=s3cret", "FT_GIT_USERNAME=x-access-token", "GIT_CONFIG_NOSYSTEM=1"} {
		if !strings.Contains(env, want) {
			t.Errorf("clone env missing %q:\n%s", want, env)
		}
	}
	for _, unwanted := range []string{"SSH_AUTH_SOCK=", "GIT_SSH_COMMAND=", "GIT_DIR="} {
		if strings.Contains(env, unwanted) {
			t.Errorf("clone env leaked ambient %q:\n%s", unwanted, env)
		}
	}
	if strings.Contains(env, "HOME="+hostHome+"\n") {
		t.Errorf("clone env inherited the host HOME:\n%s", env)
	}
	if got := strings.TrimSpace(readFile(t, userOut)); got != "x-access-token" {
		t.Errorf("askpass username = %q, want x-access-token", got)
	}
	if got := strings.TrimSpace(readFile(t, passOut)); got != "s3cret" {
		t.Errorf("askpass password = %q, want s3cret", got)
	}
}

func TestExecGitDefaultsTokenUsername(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "env.txt")
	fakeGit := filepath.Join(dir, "git")
	script := "#!/bin/sh\nenv > " + shellQuote(capture) + "\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := workspace.NewExecGit().Clone(context.Background(), workspace.CloneRequest{
		URL:  "https://example.com/acme/private.git",
		Dir:  filepath.Join(dir, "checkout"),
		Cred: &workspace.Credential{Token: "s3cret"},
	}); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	if env := readFile(t, capture); !strings.Contains(env, "FT_GIT_USERNAME=x-access-token") {
		t.Errorf("clone env missing the default username:\n%s", env)
	}
}

func TestExecGitAnonymousHasNoCredential(t *testing.T) {
	dir := t.TempDir()
	capture := filepath.Join(dir, "env.txt")
	fakeGit := filepath.Join(dir, "git")
	script := "#!/bin/sh\nenv > " + shellQuote(capture) + "\n"
	if err := os.WriteFile(fakeGit, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if err := workspace.NewExecGit().Clone(context.Background(), workspace.CloneRequest{
		URL: "https://example.com/acme/public.git",
		Dir: filepath.Join(dir, "checkout"),
	}); err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	env := readFile(t, capture)
	if strings.Contains(env, "GIT_ASKPASS=") {
		t.Errorf("anonymous clone should have no askpass:\n%s", env)
	}
	if strings.Contains(env, "FT_GIT_TOKEN=") {
		t.Errorf("anonymous clone should carry no token:\n%s", env)
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
