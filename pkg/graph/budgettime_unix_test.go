//go:build unix

package graph

import (
	"syscall"
	"testing"
	"time"
)

// processCPUTime returns the CPU time (user + system) the process has consumed
// so far. Unlike wall-clock time it does not inflate when other processes
// compete for the CPU, which is what keeps the budget guards robust to `mise
// run ci` running `cover` concurrently with `test` (t-xfrpkkt4mx).
func processCPUTime() time.Duration {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return 0
	}
	return time.Duration(ru.Utime.Nano() + ru.Stime.Nano())
}

// TestProcessCPUTimeMeasuredByWork pins processCPUTime's contract: it tracks
// work, not wall time. A wall-clock implementation fails the sleep check,
// because a sleep burns wall time but (near) no CPU. It lives beside the unix
// implementation because the !unix fallback is wall time by design.
func TestProcessCPUTimeMeasuredByWork(t *testing.T) {
	if idle := measureCPU(func() { time.Sleep(80 * time.Millisecond) }); idle > 20*time.Millisecond {
		t.Fatalf("an 80ms sleep counted as %v of CPU time; the budget is measuring wall time", idle)
	}
	if busy := measureCPU(func() { burnCPU(40 * time.Millisecond) }); busy <= 0 {
		t.Fatal("a CPU-burning loop consumed no measurable CPU time")
	}
}

var burnSink uint64

// burnCPU spins on arithmetic for d of wall time, consuming CPU the whole
// while. burnSink keeps the loop from being optimized away.
func burnCPU(d time.Duration) {
	deadline := time.Now().Add(d)
	var x uint64
	for time.Now().Before(deadline) {
		for i := 0; i < 10000; i++ {
			x = x*1664525 + 1013904223
		}
	}
	burnSink = x
}
