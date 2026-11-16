//go:build !darwin && !linux

package procgroup

import "os"

// KillTree kills pid alone. Walking a process tree needs a kernel interface this
// platform does not expose here, so the command that cannot be isolated - the
// interactive case - falls back to killing the direct child, and a descendant it
// forked survives it.
func KillTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return proc.Kill()
}
