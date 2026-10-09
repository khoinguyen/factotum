//go:build !unix

package app

import "time"

// processCPUTime falls back to wall-clock time where getrusage is unavailable.
// The hosted CI runner is Linux, where the unix implementation is used; this
// fallback only keeps the package buildable elsewhere.
func processCPUTime() time.Duration { return time.Since(procStart) }

var procStart = time.Now()
