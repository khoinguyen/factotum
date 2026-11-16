//go:build darwin || linux

package procgroup

import (
	"errors"
	"syscall"
)

// KillTree SIGKILLs pid and every process below it. It serves the command that
// cannot be isolated: a child attached to the host terminal stays in the
// caller's process group, where a group signal would hit the caller too.
//
// The tree is read before anything is signalled, because the parent links are
// what the walk is built from. That makes the teardown best-effort by
// construction: a descendant that reparents itself away from the child (the
// classic double-fork daemon) is not in its tree, and one that forks after the
// snapshot escapes it. Only a process group we own closes that window, which is
// why the isolated path uses one; this reports a failure to read the table
// rather than retrying it. A process that has already exited is not an error:
// the stop it raced still ended with nothing left running.
func KillTree(pid int) error {
	if pid <= 0 {
		return nil
	}
	kids, enumErr := children()
	killErr := syscall.Kill(pid, syscall.SIGKILL)
	if errors.Is(killErr, syscall.ESRCH) {
		killErr = nil
	}
	// Deepest first: the leaves are the processes that would otherwise be
	// reparented and survive their parent's death.
	targets := descendants(pid, kids)
	for i := len(targets) - 1; i >= 0; i-- {
		_ = syscall.Kill(targets[i], syscall.SIGKILL)
	}
	if killErr != nil {
		return killErr
	}
	return enumErr
}

// descendants returns the processes below pid from a child -> parents snapshot.
func descendants(pid int, kids map[int][]int) []int {
	var out []int
	seen := map[int]bool{pid: true}
	queue := []int{pid}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, kid := range kids[cur] {
			if seen[kid] {
				continue
			}
			seen[kid] = true
			out = append(out, kid)
			queue = append(queue, kid)
		}
	}
	return out
}
