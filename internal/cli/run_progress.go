package cli

import (
	"fmt"
	"io"
	"time"
)

// defaultRunProgressInterval is the gap between headless-run heartbeats. It is
// long enough to stay quiet on a short run and short enough that a multi-minute
// harness never looks hung.
const defaultRunProgressInterval = 15 * time.Second

// runProgressLabel names a run in live progress: its session (a task id, a
// project, or a groom session id) plus the adapters it runs through, so a
// waiting user can tell which run is alive.
func runProgressLabel(session, backend, harness string) string {
	return fmt.Sprintf("%s (sandbox=%s harness=%s)", session, backend, harness)
}

// runProgress narrates a headless run on stderr: a "started" line naming the
// run's session, then a periodic "still running" heartbeat with the elapsed
// time. It is deliberately not silent, because a headless harness buffers its
// output until it exits, so a multi-minute run would otherwise show nothing.
type runProgress struct {
	w        io.Writer
	label    string
	start    time.Time
	interval time.Duration
	stopc    chan struct{}
	done     chan struct{}
}

// startRunProgress begins narrating a headless run to stderr and returns the
// reporter, or nil when progress is suppressed: an interactive run (the agent
// owns the terminal), machine-readable output (-o json|yaml), or a non-terminal
// stderr (a pipe or redirect wants no narration). Call stop when the run ends.
func (d *Deps) startRunProgress(label string, interactive bool) *runProgress {
	if interactive || d.structured() || d.IsTerminal == nil || !d.IsTerminal(d.Err) {
		return nil
	}
	p := &runProgress{
		w:        d.Err,
		label:    label,
		start:    d.Clock.Now(),
		interval: defaultRunProgressInterval,
		stopc:    make(chan struct{}),
		done:     make(chan struct{}),
	}
	_, _ = fmt.Fprintf(p.w, "ft: %s started\n", p.label)
	go p.loop()
	return p
}

func (p *runProgress) loop() {
	defer close(p.done)
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stopc:
			return
		case <-ticker.C:
			p.heartbeat()
		}
	}
}

// heartbeat writes one progress line with the run's label and elapsed time.
func (p *runProgress) heartbeat() {
	_, _ = fmt.Fprintf(p.w, "ft: %s still running (%s)\n", p.label, time.Since(p.start).Round(time.Second))
}

// stop ends the narration and waits for the heartbeat goroutine, so no progress
// write outlives the run. It is safe on a nil reporter.
func (p *runProgress) stop() {
	if p == nil {
		return
	}
	close(p.stopc)
	<-p.done
}
