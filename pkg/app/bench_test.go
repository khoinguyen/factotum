package app

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/rank"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
	"github.com/khoinguyen/factotum/pkg/store/memory"
	"github.com/khoinguyen/factotum/pkg/store/sqlite"
)

// Hot-path latency budgets, warm (from t-g4g3ezi6iu):
//
//	hot path  (task next/get/set/start/done/claim, memory get, doc get) < 100ms @<=10k, <250ms @50k
//	heavier   (task list, graph render, search, task context, --all)   < 250ms @<=10k, <500ms @50k
//	point ops (get/set by id)                                           < 25ms at any scale
//
// The Go benchmarks report mean ns/op; per-run p50/p95 and bytes-of-output
// reporting (plus the 50k/100k tiers and process-startup-inclusive timing) land
// in t-perf-scale. TestHotPathBudgetSmoke is the CI tier: it drives the `task
// next` path (load + graph + readiness + rank) at 10k and fails at smokeHeadroom
// times the @10k budget, so an order-of-magnitude regression fails while
// ordinary CI noise does not. Seed time is not asserted.
const (
	hotPathBudget10k = 100 * time.Millisecond
	smokeHeadroom    = 5
)

func openBenchBackend(tb testing.TB, name string) store.Backend {
	tb.Helper()
	dir := tb.TempDir()
	var (
		be  store.Backend
		err error
	)
	switch name {
	case "memory":
		be = memory.New()
	case "jsonfile":
		be, err = jsonfile.Open(context.Background(), store.Config{Options: map[string]string{"path": filepath.Join(dir, "db.json")}})
	case "sqlite":
		be, err = sqlite.Open(context.Background(), store.Config{Options: map[string]string{"path": filepath.Join(dir, "db.sqlite")}})
	default:
		tb.Fatalf("unknown backend %q", name)
	}
	if err != nil {
		tb.Fatalf("open %s: %v", name, err)
	}
	tb.Cleanup(func() { _ = be.Close() })
	return be
}

// seedTasks writes n synthetic tasks into a fresh project. Every tenth task
// depends on its predecessor, so the graph has edges to resolve and the ready
// set is non-trivial.
func seedTasks(tb testing.TB, be store.Backend, n int) core.ProjectID {
	tb.Helper()
	ctx := context.Background()
	project := &core.Project{ID: "prj", Name: "Scale", Policy: core.DefaultResolutionPolicy(), CreatedAt: time.Unix(0, 0).UTC()}
	if err := be.Projects().Create(ctx, project); err != nil {
		tb.Fatalf("create project: %v", err)
	}
	prev := core.TicketID("")
	for i := 0; i < n; i++ {
		id := core.TicketID(fmt.Sprintf("t-%06d", i))
		task := &core.Ticket{
			ID: id, ProjectID: project.ID, Kind: core.KindTask,
			Title: fmt.Sprintf("task %d", i), Status: core.StatusTodo, Priority: i % 5,
			CreatedAt: time.Unix(0, 0).UTC(),
		}
		if i > 0 && i%10 == 0 {
			task.Deps = []core.TicketID{prev}
		}
		if err := be.Tickets().Create(ctx, task); err != nil {
			tb.Fatalf("create task %d: %v", i, err)
		}
		prev = id
	}
	return project.ID
}

// syntheticTasks is the in-memory twin of seedTasks, for benchmarks that time
// the graph and ranker directly.
func syntheticTasks(n int) []core.Ticket {
	tasks := make([]core.Ticket, n)
	for i := range tasks {
		tasks[i] = core.Ticket{
			ID: core.TicketID(fmt.Sprintf("t-%06d", i)), ProjectID: "prj", Kind: core.KindTask,
			Title: fmt.Sprintf("task %d", i), Status: core.StatusTodo, Priority: i % 5,
			CreatedAt: time.Unix(0, 0).UTC(),
		}
		if i > 0 && i%10 == 0 {
			tasks[i].Deps = []core.TicketID{tasks[i-1].ID}
		}
	}
	return tasks
}

// BenchmarkLoadSnapshot measures the dominant read path: load every task,
// decode it, build the graph, and compute readiness. It runs per backend and
// per scale; jsonfile is capped at 1k because it rewrites its whole document on
// every insert (see t-ewr3xidxtk).
func BenchmarkLoadSnapshot(b *testing.B) {
	scales := map[string][]int{"memory": {1000, 10000}, "sqlite": {1000, 10000}, "jsonfile": {1000}}
	ctx := context.Background()
	now := time.Unix(0, 0).UTC()
	for _, name := range []string{"memory", "sqlite", "jsonfile"} {
		for _, n := range scales[name] {
			b.Run(fmt.Sprintf("%s/%d", name, n), func(b *testing.B) {
				be := openBenchBackend(b, name)
				projectID := seedTasks(b, be, n)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := LoadSnapshot(ctx, be, projectID, now); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkTaskGraphFacts measures the bounded neighborhood read that backs
// `task get`. The neighborhood load is constant, and sqlite resolves dependents
// from its task_deps index, so sqlite is scale-flat. memory and jsonfile scan
// their in-memory set for DependsOn, so they grow O(tasks) (still well within
// the point budget); the benchmark tracks both.
func BenchmarkTaskGraphFacts(b *testing.B) {
	scales := map[string][]int{"memory": {1000, 10000, 50000}, "sqlite": {1000, 10000, 50000}, "jsonfile": {1000}}
	ctx := context.Background()
	now := time.Unix(0, 0).UTC()
	for _, name := range []string{"memory", "sqlite", "jsonfile"} {
		for _, n := range scales[name] {
			b.Run(fmt.Sprintf("%s/%d", name, n), func(b *testing.B) {
				be := openBenchBackend(b, name)
				seedTasks(b, be, n)
				target, err := be.Tickets().Get(ctx, core.TicketID(fmt.Sprintf("t-%06d", n/2)))
				if err != nil {
					b.Fatal(err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, _, err := TaskGraphFacts(ctx, be, target, now); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkGraphBuild(b *testing.B) {
	tasks := syntheticTasks(10000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := graph.New(tasks, core.DefaultResolutionPolicy()); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompositeRank(b *testing.B) {
	tasks := syntheticTasks(10000)
	g, err := graph.New(tasks, core.DefaultResolutionPolicy())
	if err != nil {
		b.Fatal(err)
	}
	ranker, err := rank.Default()
	if err != nil {
		b.Fatal(err)
	}
	ptrs := make([]*core.Ticket, len(tasks))
	for i := range tasks {
		ptrs[i] = &tasks[i]
	}
	req := rank.Request{Graph: g, Tasks: ptrs, Candidates: g.ReadySet()}
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := ranker.Rank(ctx, req); err != nil {
			b.Fatal(err)
		}
	}
}

// TestHotPathBudgetSmoke is the CI tier: it drives the full `task next` path
// (load + graph + readiness + rank) and fails at smokeHeadroom times the @10k
// budget. CI runs under -race, which inflates the sqlite driver far more than
// the in-memory backend, so sqlite is exercised at a smaller scale to keep its
// absolute time under a ceiling tight enough to catch a constant-factor
// regression. Seed time is not asserted.
//
// The timed section measures process CPU time, not wall time. `mise run ci` fans
// out `test` and `cover` (both `go test -race ./...`) in parallel, and on a
// loaded machine that contention inflated wall-clock `task next` to ~2s against
// the 500ms ceiling while the work itself was unchanged (t-k4xvzrnrdi). CPU time
// is the invariant under load and still catches a constant-factor regression.
func TestHotPathBudgetSmoke(t *testing.T) {
	ctx := context.Background()
	now := time.Unix(0, 0).UTC()
	ranker, err := rank.Default()
	if err != nil {
		t.Fatalf("rank.Default() error = %v", err)
	}
	ceiling := time.Duration(smokeHeadroom) * hotPathBudget10k
	cases := []struct {
		backend string
		tasks   int
	}{
		{"memory", 10000},
		{"sqlite", 2000},
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("%s/%d", tc.backend, tc.tasks), func(t *testing.T) {
			be := openBenchBackend(t, tc.backend)
			projectID := seedTasks(t, be, tc.tasks)

			var (
				snapshot *Snapshot
				loadErr  error
				rankErr  error
			)
			elapsed := measureCPU(func() {
				snapshot, loadErr = LoadSnapshot(ctx, be, projectID, now)
				if loadErr != nil {
					return
				}
				_, rankErr = ranker.Rank(ctx, rank.Request{Graph: snapshot.Graph, Tasks: snapshot.Tasks, Candidates: snapshot.Graph.ReadySet()})
			})
			if loadErr != nil {
				t.Fatalf("LoadSnapshot() error = %v", loadErr)
			}
			if rankErr != nil {
				t.Fatalf("Rank() error = %v", rankErr)
			}
			t.Logf("task next path at %d (%s) used %v of CPU (ceiling %v)", tc.tasks, tc.backend, elapsed, ceiling)
			if elapsed > ceiling {
				t.Fatalf("task next path at %d (%s) used %v of CPU, over the %v ceiling (budget %v)", tc.tasks, tc.backend, elapsed, ceiling, hotPathBudget10k)
			}
		})
	}
}
