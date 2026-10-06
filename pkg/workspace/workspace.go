// Package workspace resolves the checkout a task needs before it runs.
//
// A project spans one or more repositories (core.Repository). Before a launcher
// runs a harness for a task it must know which repositories the task touches and
// have their code available on the local filesystem. Resolve answers both: it
// selects the repos (core.Project.ReposForTask) and materializes each one under
// a deterministic workspace root, either by pointing at an existing local Path
// or by cloning the repo's URL.
//
// The package is backend agnostic. It only ever materializes a local directory
// tree; how that tree reaches the harness is the isolation backend's job. A
// local backend runs in place (Spec.Workdir is the root), while an isolating
// backend uploads the tree into its environment. No isolation type appears here.
//
// Design decisions (see the task t-r3ipl6xxal):
//
//   - Workspace root: the caller supplies an absolute Root. The layout inside it
//     is deterministic: one checkout per repo at Root/<RepoDir(name)>. A stable
//     root makes re-runs idempotent: an existing git checkout is reused, never
//     re-cloned, and an existing local Path is used as-is.
//   - Branch/worktree strategy: this task clones the remote's default branch into
//     a plain checkout and does not create a task branch or a git worktree.
//     Branch/worktree-per-run needs a run lifecycle to clean them up (the
//     launcher, t-jcap6kv5p2) and is deliberately deferred.
//   - Private-repo auth: a TokenProvider supplies a credential for a URL. The
//     clone environment is built deliberately, never inherited: ambient
//     SSH_AUTH_SOCK, GIT_*, and XDG_* are dropped and host git config is
//     isolated, so credentials cannot leak in from the host (the local backend
//     drops the same variables). Token auth covers HTTPS remotes; SSH remotes
//     need a deliberate mechanism that is not yet wired.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
)

// Sentinel errors, matched with errors.Is.
var (
	// ErrRoot means Options.Root was empty or not absolute.
	ErrRoot = errors.New("workspace: root must be an absolute path")
	// ErrNoRepos means the task resolved to no repositories to materialize.
	ErrNoRepos = errors.New("workspace: no repositories to materialize")
	// ErrNoSource means a required repo has neither a URL to clone nor a Path.
	ErrNoSource = errors.New("workspace: repository has neither a URL nor a Path")
	// ErrLocalPath means a repo's configured Path cannot be used as a checkout.
	ErrLocalPath = errors.New("workspace: local repository path is unusable")
	// ErrGitDir means a clone destination exists but is not a git checkout, so
	// cloning into it would clobber content that is not ours.
	ErrGitDir = errors.New("workspace: path exists but is not a git checkout")
	// ErrRepoDirConflict means two repos reduce to the same workspace directory,
	// so one would reuse the other's checkout.
	ErrRepoDirConflict = errors.New("workspace: repositories share a workspace directory")
)

// Origin says where a checkout came from.
type Origin int

const (
	// OriginLocal is an existing local Path used as-is.
	OriginLocal Origin = iota
	// OriginClone is a repo cloned from its URL.
	OriginClone
)

// Checkout is one repository materialized in the workspace.
type Checkout struct {
	// Name is the repository name as defined by the project.
	Name string
	// Path is the absolute path to the checkout on the local filesystem.
	Path string
	// Origin records whether the checkout is an existing local Path or a clone.
	Origin Origin
	// Reused reports that the checkout already existed and was not created by
	// this resolution.
	Reused bool
}

// Plan is the resolved workspace for a task.
type Plan struct {
	// Root is the absolute workspace root the checkouts live under.
	Root string
	// Checkouts are the task's repos, in the project's definition order.
	Checkouts []Checkout
}

// Credential is a deliberate credential for an HTTPS remote. It never comes from
// the ambient host environment.
type Credential struct {
	// Username is the git username; empty means the x-access-token convention.
	Username string
	// Token is the secret used as the git password.
	Token string
}

// TokenProvider resolves a deliberate credential for a repo URL. A provider that
// has no credential for the URL returns ok false and the clone stays anonymous.
// The resolver never reads SSH_AUTH_SOCK, GIT_*, XDG_*, or host git config.
type TokenProvider interface {
	Token(ctx context.Context, repoURL string) (Credential, bool, error)
}

// CloneRequest is one clone the resolver asks a Git to perform.
type CloneRequest struct {
	URL  string
	Dir  string
	Cred *Credential
}

// Git runs git. It is a port so the resolver can be tested without a real
// network and so accounting or tracing can wrap the real runner.
type Git interface {
	Clone(ctx context.Context, req CloneRequest) error
}

// Options configure resolution.
type Options struct {
	// Root is the absolute workspace root. Required.
	Root string
	// Git is the clone runner; nil uses the host git (NewExecGit).
	Git Git
	// Token is the deliberate credential source; nil means anonymous clones.
	Token TokenProvider
}

// Resolve selects the repositories a task touches and materializes each one
// under opts.Root. It is idempotent: a second run reuses existing checkouts and
// local paths instead of re-cloning.
func Resolve(ctx context.Context, project core.Project, task core.Task, opts Options) (*Plan, error) {
	root := opts.Root
	if root == "" || !filepath.IsAbs(root) {
		return nil, fmt.Errorf("%w: %q", ErrRoot, root)
	}
	repos, err := project.ReposForTask(task)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("%w: project %s defines no repositories", ErrNoRepos, project.ID)
	}

	git := opts.Git
	if git == nil {
		git = NewExecGit()
	}
	root = filepath.Clean(root)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("workspace: create root %q: %w", root, err)
	}

	plan := &Plan{Root: root, Checkouts: make([]Checkout, 0, len(repos))}
	dirs := make(map[string]string, len(repos))
	for _, repo := range repos {
		if repo.Path == "" && repo.URL != "" {
			dir := RepoDir(repo.Name)
			if other, ok := dirs[dir]; ok {
				return nil, fmt.Errorf("%w: repositories %q and %q both map to %q", ErrRepoDirConflict, other, repo.Name, dir)
			}
			dirs[dir] = repo.Name
		}
		checkout, err := materialize(ctx, repo, root, git, opts.Token)
		if err != nil {
			return nil, err
		}
		plan.Checkouts = append(plan.Checkouts, checkout)
	}
	return plan, nil
}

// RepoDir is the deterministic directory name for a repo under the workspace
// root: the repo name reduced to a single safe path element. Characters outside
// [A-Za-z0-9._] collapse to a dash; an empty or dot-only result becomes "repo".
func RepoDir(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), ".-")
	if out == "" {
		return "repo"
	}
	return out
}

func materialize(ctx context.Context, repo core.Repository, root string, git Git, token TokenProvider) (Checkout, error) {
	if repo.Path != "" {
		if !filepath.IsAbs(repo.Path) {
			return Checkout{}, fmt.Errorf("%w: %q is not an absolute path", ErrLocalPath, repo.Path)
		}
		info, err := os.Stat(repo.Path)
		if err != nil || !info.IsDir() {
			return Checkout{}, fmt.Errorf("%w: %q does not exist or is not a directory", ErrLocalPath, repo.Path)
		}
		return Checkout{Name: repo.Name, Path: filepath.Clean(repo.Path), Origin: OriginLocal, Reused: true}, nil
	}
	if repo.URL == "" {
		return Checkout{}, fmt.Errorf("%w: %q", ErrNoSource, repo.Name)
	}

	dir := filepath.Join(root, RepoDir(repo.Name))
	reused, err := existingCheckout(dir)
	if err != nil {
		return Checkout{}, err
	}
	if reused {
		return Checkout{Name: repo.Name, Path: dir, Origin: OriginClone, Reused: true}, nil
	}

	var cred *Credential
	if token != nil {
		c, ok, err := token.Token(ctx, repo.URL)
		if err != nil {
			return Checkout{}, fmt.Errorf("workspace: resolve credential for %q: %w", repo.URL, err)
		}
		if ok {
			cred = &c
		}
	}
	if err := git.Clone(ctx, CloneRequest{URL: repo.URL, Dir: dir, Cred: cred}); err != nil {
		return Checkout{}, err
	}
	return Checkout{Name: repo.Name, Path: dir, Origin: OriginClone}, nil
}

// existingCheckout reports whether dir already holds a git checkout. A missing
// dir is not an error (it is about to be cloned); an existing non-git dir is,
// because cloning into it would clobber content the workspace does not own.
func existingCheckout(dir string) (bool, error) {
	info, err := os.Stat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("workspace: stat %q: %w", dir, err)
	}
	if !info.IsDir() {
		return false, fmt.Errorf("%w: %q is not a directory", ErrGitDir, dir)
	}
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("%w: %q exists but is not a git checkout", ErrGitDir, dir)
		}
		return false, fmt.Errorf("workspace: stat %q: %w", dir, err)
	}
	return true, nil
}
