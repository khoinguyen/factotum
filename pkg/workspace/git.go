package workspace

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// askpassScript answers git's credential prompt from the explicit clone
// environment. It carries no secret itself: the token arrives as FT_GIT_TOKEN,
// which is set only on the git process this package starts.
const askpassScript = `#!/bin/sh
case "$1" in
  *[Uu]sername*) printf '%s\n' "${FT_GIT_USERNAME:-x-access-token}" ;;
  *) printf '%s\n' "$FT_GIT_TOKEN" ;;
esac
`

// ExecGit clones with the host git binary under a deliberate, minimal
// environment. It is the default Git.
//
// The environment is built from scratch rather than inherited, which is the
// point: ambient SSH_AUTH_SOCK, GIT_*, and XDG_* variables never reach the
// clone, and HOME points at a scratch directory so host git config and stored
// credentials are not read. This matches the local isolation backend, which
// deliberately drops the same host variables. A Credential is the only way a
// secret enters a clone, and it enters through GIT_ASKPASS (not argv, so it does
// not leak through ps).
type ExecGit struct{}

// NewExecGit returns the host-git clone runner.
func NewExecGit() *ExecGit { return &ExecGit{} }

// Clone runs `git clone` for req. The destination's parent is created by the
// caller; git creates the destination itself.
func (g *ExecGit) Clone(ctx context.Context, req CloneRequest) error {
	if req.URL == "" {
		return errors.New("workspace: clone requires a URL")
	}
	if req.Dir == "" {
		return errors.New("workspace: clone requires a destination")
	}

	env, cleanup, err := gitEnv(req.Cred)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := runGit(ctx, env, "clone", "--quiet", "--no-tags", "--", req.URL, req.Dir); err != nil {
		return fmt.Errorf("workspace: clone %s: %w", req.URL, err)
	}
	return nil
}

// RemoteURL returns the checkout's origin URL. It reads repo-local config only,
// and still runs under the deliberate env so an ambient GIT_DIR cannot redirect
// it to a different repository.
func (g *ExecGit) RemoteURL(ctx context.Context, dir string) (string, error) {
	if dir == "" {
		return "", errors.New("workspace: remote URL requires a checkout directory")
	}
	env, cleanup, err := gitEnv(nil)
	if err != nil {
		return "", err
	}
	defer cleanup()

	cmd := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin")
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return "", fmt.Errorf("workspace: remote url %s: %w: %s", dir, err, msg)
		}
		return "", fmt.Errorf("workspace: remote url %s: %w", dir, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Refresh fetches req.Dir's origin and hard-resets its current branch to the
// upstream tip, discarding tracked local changes (untracked files are left in
// place). It runs under the same deliberate env as Clone, so a credential
// reaches the fetch through GIT_ASKPASS and nothing ambient leaks in.
func (g *ExecGit) Refresh(ctx context.Context, req RefreshRequest) error {
	if req.Dir == "" {
		return errors.New("workspace: refresh requires a checkout directory")
	}
	env, cleanup, err := gitEnv(req.Cred)
	if err != nil {
		return err
	}
	defer cleanup()

	if err := runGit(ctx, env, "-C", req.Dir, "fetch", "--quiet", "--prune", "origin"); err != nil {
		return fmt.Errorf("workspace: fetch %s: %w", req.Dir, err)
	}
	if err := runGit(ctx, env, "-C", req.Dir, "reset", "--quiet", "--hard", "@{u}"); err != nil {
		return fmt.Errorf("workspace: reset %s: %w", req.Dir, err)
	}
	return nil
}

// gitEnv builds the deliberate, minimal environment git runs under and a cleanup
// for its scratch dir. The environment is built from scratch rather than
// inherited, which is the point: ambient SSH_AUTH_SOCK, GIT_*, and XDG_*
// variables never reach git, and HOME points at a scratch directory so host git
// config and stored credentials are not read. A Credential is the only way a
// secret enters, through GIT_ASKPASS (not argv, so it does not leak via ps).
func gitEnv(cred *Credential) (env []string, cleanup func(), err error) {
	scratch, err := os.MkdirTemp("", "ft-git-")
	if err != nil {
		return nil, nil, fmt.Errorf("workspace: create git scratch dir: %w", err)
	}
	cleanup = func() { _ = os.RemoveAll(scratch) }

	env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + scratch,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(scratch, "gitconfig"),
		"GIT_TERMINAL_PROMPT=0",
	}
	if cred != nil && cred.Token != "" {
		helper := filepath.Join(scratch, "askpass")
		if err := os.WriteFile(helper, []byte(askpassScript), 0o700); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("workspace: write askpass helper: %w", err)
		}
		env = append(env,
			"GIT_ASKPASS="+helper,
			"FT_GIT_USERNAME="+gitUsername(cred),
			"FT_GIT_TOKEN="+cred.Token,
		)
	}
	return env, cleanup, nil
}

// runGit runs git with env and folds stderr into the returned error so callers
// can add operation and target context.
func runGit(ctx context.Context, env []string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func gitUsername(cred *Credential) string {
	if cred.Username != "" {
		return cred.Username
	}
	return "x-access-token"
}

var _ Git = (*ExecGit)(nil)
