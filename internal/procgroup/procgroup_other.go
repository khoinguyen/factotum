//go:build !unix

package procgroup

import "os/exec"

// Isolate is a no-op where process groups are unavailable.
func Isolate(*exec.Cmd) {}

// KillGroup kills the direct child only: there is no process group to signal, so
// a descendant that outlives its parent is left to the OS to reap.
func KillGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
