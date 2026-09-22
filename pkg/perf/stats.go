// Package perf measures the end-to-end cost of the hot CLI commands at scale:
// it generates synthetic datasets, times each command over several runs, and
// reports p50/p95 latency and output size so a regression or a budget breach is
// visible and comparable across runs. See t-perf-scale.
package perf

import (
	"math"
	"sort"
	"time"
)

// Stats summarizes a set of latency samples.
type Stats struct {
	N    int           `json:"n"`
	Min  time.Duration `json:"min_ns"`
	P50  time.Duration `json:"p50_ns"`
	P95  time.Duration `json:"p95_ns"`
	Max  time.Duration `json:"max_ns"`
	Mean time.Duration `json:"mean_ns"`
}

// Summarize computes the summary of samples. The input is not modified.
func Summarize(samples []time.Duration) Stats {
	if len(samples) == 0 {
		return Stats{}
	}
	sorted := append([]time.Duration(nil), samples...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	var total time.Duration
	for _, d := range sorted {
		total += d
	}
	return Stats{
		N:    len(sorted),
		Min:  sorted[0],
		Max:  sorted[len(sorted)-1],
		P50:  Percentile(sorted, 0.50),
		P95:  Percentile(sorted, 0.95),
		Mean: total / time.Duration(len(sorted)),
	}
}

// Percentile returns the nearest-rank percentile of an ascending-sorted slice.
// The rank is ceil(q*n), clamped to the valid index range; an empty slice yields
// zero. Percentile does not sort its input.
func Percentile(sorted []time.Duration, q float64) time.Duration {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if q <= 0 {
		return sorted[0]
	}
	if q >= 1 {
		return sorted[n-1]
	}
	idx := int(math.Ceil(q*float64(n))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	return sorted[idx]
}
