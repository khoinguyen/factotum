package opencode

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ordinaryLoadProbeTail is the worst `--version` probe latency measured under
// parallel loop-agent load on the host where this flaked (t-izluv7llq6): past
// 3.7s, against roughly 13ms idle. It is a host-specific measurement, not a
// portable constant — other hosts run the same test with a tail under a second —
// so it serves as the yardstick for the bound, not as a claim about any machine.
const ordinaryLoadProbeTail = 4 * time.Second

// TestVersionProbeTimeoutKeepsHeadroomOverTheLoadTail pins the rule the bound is
// chosen by: at least four times the probe tail parallel loop agents produce. The
// bound is a hang bound, not a load budget, so a value near that tail does not
// merely slow the probe, it reports V1 for an in-place V2 — the flake that burned
// every loop agent's `mise run ci`. A bound under this floor flakes again.
func TestVersionProbeTimeoutKeepsHeadroomOverTheLoadTail(t *testing.T) {
	want := 4 * ordinaryLoadProbeTail
	if versionProbeTimeout < want {
		t.Fatalf("versionProbeTimeout = %v, want at least %v: four times the %v probe tail measured on the host where the probe flaked; a tighter bound answers V1 for an in-place V2 under loop-agent load", versionProbeTimeout, want, ordinaryLoadProbeTail)
	}
}

// TestProbeVariantCutsOffAHungBinary pins the other half of the bound: a binary
// that never answers must still be cut off at the bound rather than stalling run
// selection, and the cut-off is reported as V1 like any other failed probe. The
// fake binary leaves `sleep` holding its stdout, the shape a launcher takes, so
// killing the direct child is not enough to end the probe.
func TestProbeVariantCutsOffAHungBinary(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "opencode")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 10\necho 'opencode v2.0.25'\n"), 0o755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	start := time.Now()
	got := probeVariant(bin, 150*time.Millisecond)
	elapsed := time.Since(start)

	if got != V1 {
		t.Fatalf("probeVariant(hung binary) = %v, want V1", got)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("probeVariant(hung binary) took %v, want it cut off at the 150ms bound", elapsed)
	}
}
