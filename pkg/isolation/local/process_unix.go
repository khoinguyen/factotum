//go:build unix

package local

import (
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the command in its own process group, so Stop and
// Timeout can signal the whole tree a harness forks, not just the direct child.
func configureProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	return syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
}
