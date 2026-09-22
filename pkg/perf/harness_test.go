package perf

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

type fakeRunner struct {
	duration time.Duration
	bytes    int
	err      error
	calls    int
}

func (r *fakeRunner) Run(_ context.Context, _ Op) (time.Duration, int, error) {
	r.calls++
	return r.duration, r.bytes, r.err
}

type provisionCall struct {
	backend string
	spec    Spec
}

type fakeProvisioner struct {
	runners map[string]*fakeRunner
	calls   []provisionCall
	cleaned int
	provErr error
}

func (p *fakeProvisioner) Provision(_ context.Context, backend string, spec Spec) (Runner, func(), error) {
	p.calls = append(p.calls, provisionCall{backend: backend, spec: spec})
	if p.provErr != nil {
		return nil, nil, p.provErr
	}
	runner := p.runners[backend]
	if runner == nil {
		runner = &fakeRunner{duration: 10 * time.Millisecond}
	}
	return runner, func() { p.cleaned++ }, nil
}

func TestRunSkipsScalesAboveTheBackendCap(t *testing.T) {
	provisioner := &fakeProvisioner{runners: map[string]*fakeRunner{
		"jsonfile": {duration: 10 * time.Millisecond},
	}}
	cfg := Config{
		Backends:   []string{"jsonfile"},
		Scales:     []int{1000, 10000},
		Ops:        []Op{{Name: "task next", Class: ClassHot}},
		Iterations: 1,
	}
	report, err := Run(context.Background(), cfg, provisioner)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(provisioner.calls) != 1 {
		t.Fatalf("provision calls = %d, want 1 (10000 is above the jsonfile cap)", len(provisioner.calls))
	}
	if provisioner.calls[0].spec.Tasks != 1000 {
		t.Fatalf("provisioned tasks = %d, want 1000", provisioner.calls[0].spec.Tasks)
	}
	if len(report.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(report.Results))
	}
}

func TestRunWarmsUpThenRecordsEveryOp(t *testing.T) {
	runner := &fakeRunner{duration: 7 * time.Millisecond, bytes: 42}
	provisioner := &fakeProvisioner{runners: map[string]*fakeRunner{"memory": runner}}
	cfg := Config{
		Backends:   []string{"memory"},
		Scales:     []int{1000},
		Ops:        []Op{{Name: "task next", Class: ClassHot}, {Name: "task get", Class: ClassPoint}},
		Iterations: 3,
		Warmups:    1,
	}
	report, err := Run(context.Background(), cfg, provisioner)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	// 2 ops x (1 warmup + 3 iterations).
	if runner.calls != 8 {
		t.Fatalf("runner calls = %d, want 8", runner.calls)
	}
	if len(report.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(report.Results))
	}
	first := report.Results[0]
	if first.Stats.N != 3 || first.Stats.P50 != 7*time.Millisecond {
		t.Fatalf("first result = %+v, want N=3 P50=7ms", first)
	}
	if first.OutputBytes != 42 {
		t.Fatalf("output bytes = %d, want 42", first.OutputBytes)
	}
	if first.Budget != 100*time.Millisecond || first.Over {
		t.Fatalf("first result budget/over = %v/%v, want 100ms/false", first.Budget, first.Over)
	}
}

func TestRunMarksOverBudgetResults(t *testing.T) {
	runner := &fakeRunner{duration: 300 * time.Millisecond}
	provisioner := &fakeProvisioner{runners: map[string]*fakeRunner{"sqlite": runner}}
	cfg := Config{
		Backends:   []string{"sqlite"},
		Scales:     []int{10000},
		Ops:        []Op{{Name: "task list", Class: ClassHeavy}},
		Iterations: 2,
	}
	report, err := Run(context.Background(), cfg, provisioner)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(report.Violations()) != 1 {
		t.Fatalf("violations = %+v, want 1", report.Violations())
	}
}

func TestRunCleansUpAfterEachScale(t *testing.T) {
	provisioner := &fakeProvisioner{runners: map[string]*fakeRunner{}}
	cfg := Config{
		Backends:   []string{"memory"},
		Scales:     []int{1000, 10000},
		Ops:        []Op{{Name: "task next", Class: ClassHot}},
		Iterations: 1,
	}
	if _, err := Run(context.Background(), cfg, provisioner); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if provisioner.cleaned != 2 {
		t.Fatalf("cleanups = %d, want 2", provisioner.cleaned)
	}
}

func TestRunPropagatesProvisionError(t *testing.T) {
	provisioner := &fakeProvisioner{provErr: errors.New("boom")}
	cfg := Config{Backends: []string{"sqlite"}, Scales: []int{1000}, Ops: []Op{{Name: "task next"}}, Iterations: 1}
	if _, err := Run(context.Background(), cfg, provisioner); err == nil {
		t.Fatal("Run() error = nil, want provision error")
	}
}

func TestRunPropagatesRunnerError(t *testing.T) {
	runner := &fakeRunner{err: errors.New("op failed")}
	provisioner := &fakeProvisioner{runners: map[string]*fakeRunner{"memory": runner}}
	cfg := Config{Backends: []string{"memory"}, Scales: []int{1000}, Ops: []Op{{Name: "task next"}}, Iterations: 1}
	_, err := Run(context.Background(), cfg, provisioner)
	if err == nil {
		t.Fatal("Run() error = nil, want runner error")
	}
	if !errors.Is(err, runner.err) {
		t.Fatalf("Run() error = %v, want wrapped %v", err, runner.err)
	}
}

func TestDefaultOpsCoverHotHeavyAndPoint(t *testing.T) {
	ops := DefaultOps()
	classes := map[Class]bool{}
	for _, op := range ops {
		if op.Name == "" || len(op.Args) == 0 {
			t.Fatalf("op %+v is missing a name or args", op)
		}
		classes[op.Class] = true
	}
	for _, want := range []Class{ClassNone, ClassPoint, ClassHot, ClassHeavy} {
		if !classes[want] {
			t.Fatalf("DefaultOps() has no %q op: %+v", want, ops)
		}
	}
}

func TestDefaultConfigCoversBackendsAndScales(t *testing.T) {
	cfg := DefaultConfig()
	if fmt.Sprint(cfg.Backends) != "[memory sqlite jsonfile]" {
		t.Fatalf("backends = %v", cfg.Backends)
	}
	if len(cfg.Scales) < 4 || cfg.Scales[len(cfg.Scales)-1] != 100000 {
		t.Fatalf("scales = %v, want up to 100000", cfg.Scales)
	}
	if cfg.Iterations < 1 {
		t.Fatalf("iterations = %d, want >= 1", cfg.Iterations)
	}
}
