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
// Design decisions (see the tasks t-r3ipl6xxal and t-el7zfhsufc):
//
//   - Workspace root: the caller supplies an absolute Root. The layout inside it
//     is deterministic: one checkout per repo at Root/<RepoDir(name)>. A stable
//     root makes re-runs idempotent: an existing git checkout is reused, never
//     re-cloned, and an existing local Path is used as-is.
//   - Local paths: an absolute repo Path is used as-is. A relative Path (for
//     example ".", a project registered from a local checkout) resolves against
//     Options.Base, the project root. Without a base a relative Path is an error
//     that names the path and the fix (t-7wd6op3rxf). A local Path is not
//     copied: the checkout itself becomes the run's workspace, so a backend that
//     operates on the workdir runs in and may modify the source (t-rczy267zew);
//     a repo registered by URL is cloned into the workspace instead.
//   - Re-run policy: a reused clone is not updated by default, but the resolver
//     first compares the checkout's origin URL against the repo's configured URL
//     and fails with ErrRemoteChanged when they differ, so a changed URL is never
//     silently ignored. Set Options.Refresh to fetch and hard-reset a reused
//     clone to the tip of its upstream branch, discarding local changes. The
//     model carries no revision or branch, so there is nothing to pin to; refresh
//     follows the remote default branch (upstream), and the resolver never
//     re-clones a checkout it already made.
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
	// ErrRemoteChanged means a reused checkout's origin URL no longer matches the
	// repo's configured URL, so reusing it would run against the wrong code.
	ErrRemoteChanged = errors.New("workspace: checkout remote URL changed")
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

// RefreshRequest is one fetch-and-reset the resolver asks a Git to perform on an
// existing checkout: fetch the origin and hard-reset the current branch to its
// upstream, discarding local changes.
type RefreshRequest struct {
	Dir  string
	Cred *Credential
}

// Git runs git. It is a port so the resolver can be tested without a real
// network and so accounting or tracing can wrap the real runner.
type Git interface {
	Clone(ctx context.Context, req CloneRequest) error
	// RemoteURL returns the URL of the checkout's origin remote.
	RemoteURL(ctx context.Context, dir string) (string, error)
	// Refresh fetches and hard-resets an existing checkout to its upstream.
	Refresh(ctx context.Context, req RefreshRequest) error
}

// Options configure resolution.
type Options struct {
	// Root is the absolute workspace root. Required.
	Root string
	// Base is the absolute directory a relative repo Path resolves against,
	// typically the project root (the directory holding .factotum/config.toml).
	// It is only consulted for a relative Path; empty means a relative Path
	// cannot be resolved.
	Base string
	// Git is the clone runner; nil uses the host git (NewExecGit).
	Git Git
	// Token is the deliberate credential source; nil means anonymous clones.
	Token TokenProvider
	// Refresh updates a reused clone to its upstream before returning it,
	// discarding local changes. Without it a reused clone is left as-is (still
	// rejected if its origin URL changed).
	Refresh bool
}

// Resolve selects the repositories a task touches and materializes each one
// under opts.Root. It is idempotent: a second run reuses existing checkouts and
// local paths instead of re-cloning. A reused clone whose origin URL no longer
// matches the repo's configured URL fails with ErrRemoteChanged; with
// opts.Refresh it is fetched and hard-reset to its upstream first.
func Resolve(ctx context.Context, project core.Project, task core.Ticket, opts Options) (*Plan, error) {
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
		checkout, err := materialize(ctx, repo, root, opts.Base, git, opts.Token, opts.Refresh)
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

// resolveLocalPath returns the checkout directory for a repo's configured Path.
// An absolute Path is used as-is; a relative Path resolves against base (the
// project root). A relative Path with no base is an error that names the path
// and how to fix it.
func resolveLocalPath(repo core.Repository, base string) (string, error) {
	if filepath.IsAbs(repo.Path) {
		return filepath.Clean(repo.Path), nil
	}
	if base == "" {
		return "", fmt.Errorf("%w: repository %q path %q is relative and no project root is configured to resolve it; register an absolute path (ft project repo update) or run from the project root", ErrLocalPath, repo.Name, repo.Path)
	}
	return filepath.Clean(filepath.Join(base, repo.Path)), nil
}

func materialize(ctx context.Context, repo core.Repository, root, base string, git Git, token TokenProvider, refresh bool) (Checkout, error) {
	if repo.Path != "" {
		path, err := resolveLocalPath(repo, base)
		if err != nil {
			return Checkout{}, err
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return Checkout{}, fmt.Errorf("%w: %q does not exist or is not a directory", ErrLocalPath, path)
		}
		return Checkout{Name: repo.Name, Path: path, Origin: OriginLocal, Reused: true}, nil
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
		if err := verifyRemoteURL(ctx, git, dir, repo); err != nil {
			return Checkout{}, err
		}
		if refresh {
			cred, err := resolveCredential(ctx, token, repo.URL)
			if err != nil {
				return Checkout{}, err
			}
			if err := git.Refresh(ctx, RefreshRequest{Dir: dir, Cred: cred}); err != nil {
				return Checkout{}, err
			}
		}
		return Checkout{Name: repo.Name, Path: dir, Origin: OriginClone, Reused: true}, nil
	}

	cred, err := resolveCredential(ctx, token, repo.URL)
	if err != nil {
		return Checkout{}, err
	}
	if err := git.Clone(ctx, CloneRequest{URL: repo.URL, Dir: dir, Cred: cred}); err != nil {
		return Checkout{}, err
	}
	return Checkout{Name: repo.Name, Path: dir, Origin: OriginClone}, nil
}

// verifyRemoteURL compares a reused checkout's origin URL against the repo's
// configured URL. A mismatch means the checkout belongs to a different remote
// and reusing it would run against the wrong code, so it fails rather than
// silently re-cloning (which would discard the checkout).
func verifyRemoteURL(ctx context.Context, git Git, dir string, repo core.Repository) error {
	remote, err := git.RemoteURL(ctx, dir)
	if err != nil {
		return fmt.Errorf("workspace: read origin URL for %q: %w", repo.Name, err)
	}
	if remote != repo.URL {
		return fmt.Errorf("%w: %q is at %q, configures %q", ErrRemoteChanged, repo.Name, remote, repo.URL)
	}
	return nil
}

// resolveCredential asks the token provider for a deliberate credential for
// repoURL. A nil provider, or one with no credential for the URL, yields nil so
// the clone or refresh stays anonymous.
func resolveCredential(ctx context.Context, token TokenProvider, repoURL string) (*Credential, error) {
	if token == nil {
		return nil, nil
	}
	c, ok, err := token.Token(ctx, repoURL)
	if err != nil {
		return nil, fmt.Errorf("workspace: resolve credential for %q: %w", repoURL, err)
	}
	if !ok {
		return nil, nil
	}
	return &c, nil
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
