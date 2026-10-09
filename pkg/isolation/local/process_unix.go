//go:build unix

package local

import (
	"errors"
	"os/exec"
	"syscall"
)

// configureProcessGroup puts the command in its own process group, so Stop and
// Timeout can signal the whole tree a harness forks, not just the direct child.
func configureProcessGroup(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProcessGroup SIGKILLs the command's process group. A group that is already
// gone is not an error: Cancel may race a process that exited on its own, and the
// intended outcome (nothing left running) already holds.
func killProcessGroup(c *exec.Cmd) error {
	if c.Process == nil {
		return nil
	}
	if err := syscall.Kill(-c.Process.Pid, syscall.SIGKILL); !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
