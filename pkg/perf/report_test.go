package perf

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBudgetForTiers(t *testing.T) {
	tests := []struct {
		class Class
		tasks int
		want  time.Duration
	}{
		{ClassNone, 10000, 0},
		{ClassPoint, 100000, 25 * time.Millisecond},
		{ClassHot, 1000, 100 * time.Millisecond},
		{ClassHot, 10000, 100 * time.Millisecond},
		{ClassHot, 50000, 250 * time.Millisecond},
		{ClassHot, 100000, 500 * time.Millisecond},
		{ClassHeavy, 10000, 250 * time.Millisecond},
		{ClassHeavy, 50000, 500 * time.Millisecond},
		{ClassHeavy, 100000, time.Second},
	}
	for _, tt := range tests {
		if got := BudgetFor(tt.class, tt.tasks); got != tt.want {
			t.Fatalf("BudgetFor(%q, %d) = %v, want %v", tt.class, tt.tasks, got, tt.want)
		}
	}
}

func TestNewResultFlagsOverBudget(t *testing.T) {
	op := Op{Name: "task next -n 5", Class: ClassHot}
	result := NewResult("sqlite", 10000, op, Stats{N: 5, P95: 150 * time.Millisecond}, 1200)
	if result.Budget != 100*time.Millisecond {
		t.Fatalf("budget = %v, want 100ms", result.Budget)
	}
	if !result.Over {
		t.Fatalf("result should be over budget: %+v", result)
	}
	ok := NewResult("sqlite", 10000, op, Stats{N: 5, P95: 90 * time.Millisecond}, 1200)
	if ok.Over {
		t.Fatalf("result should be within budget: %+v", ok)
	}
	unbudgeted := NewResult("sqlite", 10000, Op{Name: "version", Class: ClassNone}, Stats{P95: time.Second}, 4)
	if unbudgeted.Over {
		t.Fatalf("unbudgeted op must never be marked over: %+v", unbudgeted)
	}
}

func TestReportViolations(t *testing.T) {
	report := Report{Results: []Result{
		NewResult("memory", 10000, Op{Name: "task next", Class: ClassHot}, Stats{P95: 10 * time.Millisecond}, 10),
		NewResult("sqlite", 10000, Op{Name: "task list", Class: ClassHeavy}, Stats{P95: time.Second}, 10),
	}}
	violations := report.Violations()
	if len(violations) != 1 || violations[0].Op != "task list" {
		t.Fatalf("Violations() = %+v, want only task list", violations)
	}
}

func TestReportAppendAndLoadHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "results.jsonl")
	first := Report{GeneratedAt: time.Unix(1, 0).UTC(), Version: "v1", Results: []Result{
		NewResult("sqlite", 1000, Op{Name: "task next", Class: ClassHot}, Stats{N: 1, P50: 40 * time.Millisecond, P95: 40 * time.Millisecond}, 100),
	}}
	second := Report{GeneratedAt: time.Unix(2, 0).UTC(), Version: "v2"}

	if err := first.Append(path); err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	if err := second.Append(path); err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}
	history, err := LoadHistory(path)
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}
	if len(history) != 2 {
		t.Fatalf("history len = %d, want 2", len(history))
	}
	if history[0].Version != "v1" || history[1].Version != "v2" {
		t.Fatalf("history versions = %q, %q; want v1, v2", history[0].Version, history[1].Version)
	}
	if got := history[0].Results[0].Stats.P50; got != 40*time.Millisecond {
		t.Fatalf("round-tripped P50 = %v, want 40ms", got)
	}
}

func TestLoadHistoryMissingFileIsEmpty(t *testing.T) {
	history, err := LoadHistory(filepath.Join(t.TempDir(), "absent.jsonl"))
	if err != nil {
		t.Fatalf("LoadHistory() error = %v", err)
	}
	if len(history) != 0 {
		t.Fatalf("history = %+v, want empty", history)
	}
}

func TestReportWriteTextShowsValuesAndStatus(t *testing.T) {
	report := Report{Results: []Result{
		NewResult("sqlite", 50000, Op{Name: "task list", Class: ClassHeavy}, Stats{N: 3, P50: 300 * time.Millisecond, P95: 600 * time.Millisecond}, 2048),
	}}
	var buf bytes.Buffer
	if err := report.WriteText(&buf, nil); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
	out := buf.String()
	for _, want := range []string{"BACKEND", "sqlite", "50000", "task list", "300ms", "600ms", "500ms", "OVER", "2.0KB"} {
		if !strings.Contains(out, want) {
			t.Fatalf("WriteText() missing %q:\n%s", want, out)
		}
	}
}

func TestReportWriteTextShowsDeltaAgainstPrevious(t *testing.T) {
	op := Op{Name: "task next", Class: ClassHot}
	prev := Report{Results: []Result{NewResult("memory", 1000, op, Stats{P50: 100 * time.Millisecond, P95: 100 * time.Millisecond}, 10)}}
	cur := Report{Results: []Result{NewResult("memory", 1000, op, Stats{P50: 110 * time.Millisecond, P95: 110 * time.Millisecond}, 10)}}
	var buf bytes.Buffer
	if err := cur.WriteText(&buf, &prev); err != nil {
		t.Fatalf("WriteText() error = %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "ΔP50") || !strings.Contains(out, "+10ms") {
		t.Fatalf("WriteText() missing delta:\n%s", out)
	}
}
