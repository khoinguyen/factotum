// Package procgroup ends the whole tree of processes a command forks.
//
// A command that is cancelled, stopped, or simply exits on its own must not leave
// descendants behind: a launcher that forks a worker, or a harness that forks an
// MCP server or a bash tool, outlives a signal sent to the direct child alone -
// and outlives the direct child's own exit, because a process group outlives its
// leader while any member is left. The process group is the primitive for that,
// so a command nobody attached to a terminal belongs in one (Isolate +
// KillGroup). A command attached to the host terminal cannot be: it has to stay
// in the caller's process group, which owns the terminal and carries the
// caller's own Ctrl-C, so its tree is walked instead (KillTree). The walk reads
// the kernel's process table, which is wired up for darwin and linux; elsewhere
// KillTree kills the direct child only.
package procgroup
