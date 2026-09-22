// Command ftscale runs the end-to-end CLI scale harness: it seeds synthetic
// datasets at each scale for each backend, times the hot commands (including
// process startup), and reports p50/p95 and output bytes. Results append to a
// JSONL history so runs can be compared; see `mise run perf`.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	internalperf "github.com/khoinguyen/factotum/internal/perf"
	"github.com/khoinguyen/factotum/pkg/perf"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("ftscale", flag.ContinueOnError)
	flags.SetOutput(stderr)
	defaults := perf.DefaultConfig()
	var (
		backends   = flags.String("backends", strings.Join(defaults.Backends, ","), "comma-separated backends")
		scales     = flags.String("tasks", joinInts(defaults.Scales), "comma-separated task scales")
		iterations = flags.Int("iterations", defaults.Iterations, "recorded runs per operation")
		warmups    = flags.Int("warmups", defaults.Warmups, "discarded runs per operation before recording")
		binary     = flags.String("binary", filepath.Join("bin", "ft"), "path to the ft binary (file backends)")
		out        = flags.String("out", filepath.Join(".perf", "results.jsonl"), "JSONL history file")
		workdir    = flags.String("workdir", "", "parent directory for temp stores (default system temp)")
		strict     = flags.Bool("strict", false, "exit 3 when any operation exceeds its budget")
	)
	flags.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "usage: ftscale [flags]\n\n")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}

	cfg, err := buildConfig(*backends, *scales, *iterations, *warmups)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ftscale: %v\n", err)
		return 2
	}

	provisioner := internalperf.Provisioner{Binary: *binary, WorkDir: *workdir}
	report, err := perf.Run(context.Background(), cfg, provisioner)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ftscale: %v\n", err)
		return 1
	}

	history, err := perf.LoadHistory(*out)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "ftscale: %v\n", err)
		return 1
	}
	var previous *perf.Report
	if len(history) > 0 {
		previous = &history[len(history)-1]
	}
	if err := report.WriteText(stdout, previous); err != nil {
		_, _ = fmt.Fprintf(stderr, "ftscale: %v\n", err)
		return 1
	}
	if err := report.Append(*out); err != nil {
		_, _ = fmt.Fprintf(stderr, "ftscale: %v\n", err)
		return 1
	}

	violations := report.Violations()
	_, _ = fmt.Fprintf(stdout, "\n%d measurements at %v, %d over budget; history: %s\n",
		len(report.Results), cfg.Scales, len(violations), *out)
	for _, v := range violations {
		_, _ = fmt.Fprintf(stdout, "  OVER %s/%d %s: p95 %s > %s\n", v.Backend, v.Tasks, v.Op, v.Stats.P95, v.Budget)
	}
	if *strict && len(violations) > 0 {
		return 3
	}
	return 0
}

func buildConfig(backendsCSV, scalesCSV string, iterations, warmups int) (perf.Config, error) {
	backends := splitCSV(backendsCSV)
	if len(backends) == 0 {
		return perf.Config{}, fmt.Errorf("no backends given")
	}
	scales, err := parseScales(scalesCSV)
	if err != nil {
		return perf.Config{}, err
	}
	return perf.Config{
		Backends:   backends,
		Scales:     scales,
		Ops:        perf.DefaultOps(),
		Iterations: iterations,
		Warmups:    warmups,
	}, nil
}

func splitCSV(value string) []string {
	var out []string
	for _, part := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func joinInts(values []int) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = strconv.Itoa(value)
	}
	return strings.Join(parts, ",")
}

func parseScales(value string) ([]int, error) {
	parts := splitCSV(value)
	if len(parts) == 0 {
		return nil, fmt.Errorf("no scales given")
	}
	scales := make([]int, 0, len(parts))
	for _, part := range parts {
		n, err := strconv.Atoi(part)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("invalid scale %q, want a positive integer", part)
		}
		scales = append(scales, n)
	}
	return scales, nil
}
