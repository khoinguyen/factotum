package perf

import (
	"context"
	"fmt"
	"time"

	"github.com/khoinguyen/factotum/pkg/version"
)

// Runner executes one command invocation and reports its wall-clock duration and
// the number of bytes written to stdout.
type Runner interface {
	Run(ctx context.Context, op Op) (time.Duration, int, error)
}

// Provisioner opens and seeds a fresh backend at a scale, returning a Runner for
// it and a cleanup function the harness calls when the scale is done.
type Provisioner interface {
	Provision(ctx context.Context, backend string, spec Spec) (Runner, func(), error)
}

// Config is the matrix the harness runs: every backend, at every scale, through
// every operation, Iterations recorded times after Warmups discarded runs.
type Config struct {
	Backends   []string
	Scales     []int
	Ops        []Op
	Iterations int
	Warmups    int
}

// DefaultOps are the end-to-end commands the harness measures. They cover the
// startup floor, the hot path, point reads, and the heavier list/search/render
// paths.
func DefaultOps() []Op {
	return []Op{
		{Name: "version", Args: []string{"version"}, Class: ClassNone},
		{Name: "task next -n 5", Args: []string{"task", "next", "-n", "5"}, Class: ClassHot},
		{Name: "task get", Args: []string{"task", "get", string(TaskID(0))}, Class: ClassPoint},
		{Name: "task list", Args: []string{"task", "list"}, Class: ClassHeavy},
		{Name: "task list -o json", Args: []string{"task", "list", "-o", "json"}, Class: ClassHeavy},
		{Name: "task search", Args: []string{"task", "search", "synthetic"}, Class: ClassHeavy},
		{Name: "graph render agent", Args: []string{"graph", "render", "--format", "agent"}, Class: ClassHeavy},
		{Name: "task context", Args: []string{"task", "context", string(TaskID(0))}, Class: ClassHeavy},
		{Name: "event list -n 20", Args: []string{"event", "list", "-n", "20"}, Class: ClassHeavy},
	}
}

// DefaultConfig is the full manual/nightly matrix: the 1k/10k/50k/100k task
// tiers across all three backends. The jsonfile backend is capped by SeedCap, so
// it only appears at 1k. The heavy graph render is superlinear, so a full run
// takes tens of minutes at the 100k tier; use -tasks/-backends to subset it.
func DefaultConfig() Config {
	return Config{
		Backends:   []string{"memory", "sqlite", "jsonfile"},
		Scales:     []int{1000, 10000, 50000, 100000},
		Ops:        DefaultOps(),
		Iterations: 5,
		Warmups:    1,
	}
}

// Run executes the matrix and returns its report. Every over-budget result is
// marked, so the caller can fail or file a fix task. Seeding time is excluded
// from the measurements but included in the run's wall clock.
func Run(ctx context.Context, cfg Config, provisioner Provisioner) (*Report, error) {
	if cfg.Iterations < 1 {
		return nil, fmt.Errorf("perf: iterations must be >= 1, got %d", cfg.Iterations)
	}
	report := &Report{GeneratedAt: time.Now().UTC(), Version: version.Version}
	for _, backend := range cfg.Backends {
		cap := SeedCap(backend)
		for _, tasks := range cfg.Scales {
			if cap > 0 && tasks > cap {
				continue
			}
			spec := SpecFor(tasks)
			runner, cleanup, err := provisioner.Provision(ctx, backend, spec)
			if err != nil {
				return nil, fmt.Errorf("perf: provision %s/%d: %w", backend, tasks, err)
			}
			results, err := measureBackend(ctx, cfg, backend, tasks, runner)
			cleanup()
			if err != nil {
				return nil, err
			}
			report.Results = append(report.Results, results...)
		}
	}
	return report, nil
}

// measureBackend times every op at one backend and scale.
func measureBackend(ctx context.Context, cfg Config, backend string, tasks int, runner Runner) ([]Result, error) {
	results := make([]Result, 0, len(cfg.Ops))
	for _, op := range cfg.Ops {
		stats, outputBytes, err := measureOp(ctx, runner, op, cfg.Iterations, cfg.Warmups)
		if err != nil {
			return nil, fmt.Errorf("perf: %s/%d %s: %w", backend, tasks, op.Name, err)
		}
		results = append(results, NewResult(backend, tasks, op, stats, outputBytes))
	}
	return results, nil
}

// measureOp runs warmups discarded samples, then iterations recorded samples.
func measureOp(ctx context.Context, runner Runner, op Op, iterations, warmups int) (Stats, int, error) {
	for i := 0; i < warmups; i++ {
		if _, _, err := runner.Run(ctx, op); err != nil {
			return Stats{}, 0, err
		}
	}
	samples := make([]time.Duration, 0, iterations)
	outputBytes := 0
	for i := 0; i < iterations; i++ {
		elapsed, written, err := runner.Run(ctx, op)
		if err != nil {
			return Stats{}, 0, err
		}
		samples = append(samples, elapsed)
		if written > outputBytes {
			outputBytes = written
		}
	}
	return Summarize(samples), outputBytes, nil
}
