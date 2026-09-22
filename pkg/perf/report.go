package perf

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"
)

// Class groups operations by the latency budget they must meet.
type Class string

const (
	// ClassNone marks an operation with no budget (for example a startup probe).
	ClassNone Class = ""
	// ClassPoint is a get/set by id; it must stay O(1)-ish at any scale.
	ClassPoint Class = "point"
	// ClassHot is the interactive hot path.
	ClassHot Class = "hot"
	// ClassHeavy is a full read or render.
	ClassHeavy Class = "heavy"
)

// Op is one measured command: a label, its arguments, and its budget class.
type Op struct {
	Name  string
	Args  []string
	Class Class
}

// BudgetFor returns the p95 budget for an operation at a task scale, or 0 when
// the operation has no budget. The tiers mirror t-g4g3ezi6iu; scales above 50k
// extrapolate the 50k budget so the 100k tier is still reported.
func BudgetFor(class Class, tasks int) time.Duration {
	switch class {
	case ClassPoint:
		return 25 * time.Millisecond
	case ClassHot:
		switch {
		case tasks <= 10000:
			return 100 * time.Millisecond
		case tasks <= 50000:
			return 250 * time.Millisecond
		default:
			return 500 * time.Millisecond
		}
	case ClassHeavy:
		switch {
		case tasks <= 10000:
			return 250 * time.Millisecond
		case tasks <= 50000:
			return 500 * time.Millisecond
		default:
			return time.Second
		}
	default:
		return 0
	}
}

// Result is one operation's summary at one backend and scale.
type Result struct {
	Backend     string        `json:"backend"`
	Tasks       int           `json:"tasks"`
	Op          string        `json:"op"`
	Stats       Stats         `json:"stats"`
	OutputBytes int           `json:"output_bytes"`
	Budget      time.Duration `json:"budget_ns"`
	Over        bool          `json:"over_budget"`
}

// NewResult builds a result and evaluates it against its budget.
func NewResult(backend string, tasks int, op Op, stats Stats, outputBytes int) Result {
	budget := BudgetFor(op.Class, tasks)
	return Result{
		Backend:     backend,
		Tasks:       tasks,
		Op:          op.Name,
		Stats:       stats,
		OutputBytes: outputBytes,
		Budget:      budget,
		Over:        budget > 0 && stats.P95 > budget,
	}
}

// Report is one harness run: its results plus the run metadata that makes
// history comparable.
type Report struct {
	GeneratedAt time.Time `json:"generated_at"`
	Version     string    `json:"version"`
	Results     []Result  `json:"results"`
}

// Violations returns the results whose p95 exceeded their budget.
func (r Report) Violations() []Result {
	var out []Result
	for _, result := range r.Results {
		if result.Over {
			out = append(out, result)
		}
	}
	return out
}

// Append adds the report as one line of a JSONL history file, creating parent
// directories as needed. The history is append-only so a run can be compared
// with the runs before it.
func (r Report) Append(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create history dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open history: %w", err)
	}
	defer func() { _ = file.Close() }()
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write history: %w", err)
	}
	return nil
}

// LoadHistory reads a JSONL history file. A missing file is not an error: it
// means no run has been recorded yet.
func LoadHistory(path string) ([]Report, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open history: %w", err)
	}
	defer func() { _ = file.Close() }()

	var reports []Report
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" {
			continue
		}
		var report Report
		if err := json.Unmarshal([]byte(text), &report); err != nil {
			return nil, fmt.Errorf("parse history line %d: %w", line, err)
		}
		reports = append(reports, report)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read history: %w", err)
	}
	return reports, nil
}

// WriteText renders the results as an aligned table. When prev is non-nil the
// table adds per-operation p50/p95 deltas against that report, so a trend is
// visible at a glance.
func (r Report) WriteText(w io.Writer, prev *Report) error {
	header := []string{"BACKEND", "TASKS", "OP", "P50", "P95"}
	if prev != nil {
		header = append(header, "ΔP50", "ΔP95")
	}
	header = append(header, "BYTES", "BUDGET", "STATUS")

	writer := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, strings.Join(header, "\t")); err != nil {
		return err
	}
	for _, result := range r.Results {
		row := []string{
			result.Backend,
			fmt.Sprintf("%d", result.Tasks),
			result.Op,
			formatDuration(result.Stats.P50),
			formatDuration(result.Stats.P95),
		}
		if prev != nil {
			row = append(row, delta(result, prev, true), delta(result, prev, false))
		}
		row = append(row, formatBytes(result.OutputBytes), formatBudget(result.Budget), status(result))
		if _, err := fmt.Fprintln(writer, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return writer.Flush()
}

// delta renders the signed p50 (p50==true) or p95 change from prev to result.
func delta(result Result, prev *Report, p50 bool) string {
	before, ok := prev.lookup(result.Backend, result.Tasks, result.Op)
	if !ok {
		return "-"
	}
	sub := func(s Stats) time.Duration {
		if p50 {
			return s.P50
		}
		return s.P95
	}
	change := sub(result.Stats) - sub(before.Stats)
	if change > 0 {
		return "+" + change.String()
	}
	return change.String()
}

func (r Report) lookup(backend string, tasks int, op string) (Result, bool) {
	for _, result := range r.Results {
		if result.Backend == backend && result.Tasks == tasks && result.Op == op {
			return result, true
		}
	}
	return Result{}, false
}

func status(result Result) string {
	if result.Over {
		return "OVER"
	}
	return "ok"
}

func formatBudget(budget time.Duration) string {
	if budget <= 0 {
		return "-"
	}
	return budget.String()
}

func formatDuration(d time.Duration) string {
	if d == 0 {
		return "0s"
	}
	return d.Round(time.Microsecond).String()
}

func formatBytes(n int) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%dB", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
