//go:build darwin

package procgroup

import "golang.org/x/sys/unix"

// children returns every live process keyed by parent id, from the kernel's
// process table. Darwin exposes the parent through kern.proc.all, which an
// unprivileged process may read.
func children() (map[int][]int, error) {
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	kids := make(map[int][]int, len(kps))
	for _, kp := range kps {
		pid, ppid := int(kp.Proc.P_pid), int(kp.Eproc.Ppid)
		kids[ppid] = append(kids[ppid], pid)
	}
	return kids, nil
}
