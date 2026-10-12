package render

import "time"

// measureCPU returns the process CPU time fn consumed. CPU time, not wall time,
// is the budget's invariant: it does not inflate when other processes (a
// concurrent `cover` run) compete for the CPU, which is what keeps the
// agent-render budget smoke robust to `mise run ci` parallel fan-out
// (t-vofx7ijled). It is platform-independent; only processCPUTime differs by
// platform.
func measureCPU(fn func()) time.Duration {
	start := processCPUTime()
	fn()
	return processCPUTime() - start
}
