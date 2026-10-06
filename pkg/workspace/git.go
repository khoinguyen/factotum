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

	scratch, err := os.MkdirTemp("", "ft-git-")
	if err != nil {
		return fmt.Errorf("workspace: create git scratch dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + scratch,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + filepath.Join(scratch, "gitconfig"),
		"GIT_TERMINAL_PROMPT=0",
	}
	if req.Cred != nil && req.Cred.Token != "" {
		helper := filepath.Join(scratch, "askpass")
		if err := os.WriteFile(helper, []byte(askpassScript), 0o700); err != nil {
			return fmt.Errorf("workspace: write askpass helper: %w", err)
		}
		env = append(env,
			"GIT_ASKPASS="+helper,
			"FT_GIT_USERNAME="+gitUsername(req.Cred),
			"FT_GIT_TOKEN="+req.Cred.Token,
		)
	}

	cmd := exec.CommandContext(ctx, "git", "clone", "--quiet", "--no-tags", "--", req.URL, req.Dir)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return fmt.Errorf("workspace: clone %s: %w: %s", req.URL, err, msg)
		}
		return fmt.Errorf("workspace: clone %s: %w", req.URL, err)
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
