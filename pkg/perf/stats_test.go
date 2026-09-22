package perf

import (
	"testing"
	"time"
)

func TestPercentileNearestRank(t *testing.T) {
	sorted := []time.Duration{
		10 * time.Millisecond, 20 * time.Millisecond, 30 * time.Millisecond,
		40 * time.Millisecond, 50 * time.Millisecond,
	}
	tests := []struct {
		name string
		q    float64
		want time.Duration
	}{
		{"min", 0, 10 * time.Millisecond},
		{"p50", 0.50, 30 * time.Millisecond},
		{"p95", 0.95, 50 * time.Millisecond},
		{"max", 1, 50 * time.Millisecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Percentile(sorted, tt.q); got != tt.want {
				t.Fatalf("Percentile(q=%v) = %v, want %v", tt.q, got, tt.want)
			}
		})
	}
}

func TestPercentileEmpty(t *testing.T) {
	if got := Percentile(nil, 0.5); got != 0 {
		t.Fatalf("Percentile(nil) = %v, want 0", got)
	}
}

func TestSummarizeSortsAndReportsTail(t *testing.T) {
	// Unsorted input: min 5ms, max 500ms, mean 605/3 ms.
	samples := []time.Duration{
		500 * time.Millisecond, 5 * time.Millisecond, 100 * time.Millisecond,
	}
	got := Summarize(samples)
	if got.N != 3 {
		t.Fatalf("N = %d, want 3", got.N)
	}
	if got.Min != 5*time.Millisecond || got.Max != 500*time.Millisecond {
		t.Fatalf("Min/Max = %v/%v, want 5ms/500ms", got.Min, got.Max)
	}
	if got.P50 != 100*time.Millisecond {
		t.Fatalf("P50 = %v, want 100ms", got.P50)
	}
	if got.P95 != 500*time.Millisecond {
		t.Fatalf("P95 = %v, want 500ms", got.P95)
	}
	if want := 605 * time.Millisecond / 3; got.Mean != want {
		t.Fatalf("Mean = %v, want %v", got.Mean, want)
	}
}

func TestSummarizeEmpty(t *testing.T) {
	if got := Summarize(nil); got != (Stats{}) {
		t.Fatalf("Summarize(nil) = %+v, want zero", got)
	}
}
