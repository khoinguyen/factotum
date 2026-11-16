package opencode

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ordinaryLoadProbeTail is the worst `--version` probe latency measured under
// ordinary parallel load (t-izluv7llq6): eight concurrent `go test -race`
// suites plus 32 concurrent probes pushed the tail past 3.7s, against roughly
// 13ms on an idle host.
const ordinaryLoadProbeTail = 4 * time.Second

// TestVersionProbeTimeoutKeepsHeadroomOverTheLoadTail pins what the probe's
// bound is for. It exists to stop a hung binary from stalling run selection, not
// to race a loaded host: fork/exec latency on a busy machine reaches seconds,
// and a bound near that tail does not merely slow the probe, it reports V1 for
// an in-place V2, which is the flake that burned every loop agent's `mise run
// ci`. A bound with only a small margin over the load tail will flake again.
func TestVersionProbeTimeoutKeepsHeadroomOverTheLoadTail(t *testing.T) {
	want := 4 * ordinaryLoadProbeTail
	if versionProbeTimeout < want {
		t.Fatalf("versionProbeTimeout = %v, want at least %v: four times the %v probe tail measured under parallel load; a tighter bound answers V1 for an in-place V2 on a loaded host", versionProbeTimeout, want, ordinaryLoadProbeTail)
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
