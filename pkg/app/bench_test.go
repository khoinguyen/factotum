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

// Hot-path latency budgets, warm p95 (from t-g4g3ezi6iu):
//
//	hot path  (task next/get/set/start/done/claim, memory get, doc get) < 100ms @<=10k, <250ms @50k
//	heavier   (task list, graph render, search, task context, --all)   < 250ms @<=10k, <500ms @50k
//	point ops (get/set by id)                                           < 25ms at any scale
//
// TestHotPathBudgetSmoke enforces a generous multiple of the @10k hot-path
// budget so an order-of-magnitude regression fails CI without flaking on a
// noisy machine. Benchmark results are the finer-grained signal; run them with
// `mise run bench`.
const (
	hotPathBudget10k = 100 * time.Millisecond
	smokeCeiling     = 30 * hotPathBudget10k
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
	prev := core.TaskID("")
	for i := 0; i < n; i++ {
		id := core.TaskID(fmt.Sprintf("t-%06d", i))
		task := &core.Task{
			ID: id, ProjectID: project.ID, Kind: core.KindTask,
			Title: fmt.Sprintf("task %d", i), Status: core.StatusTodo, Priority: i % 5,
			CreatedAt: time.Unix(0, 0).UTC(),
		}
		if i > 0 && i%10 == 0 {
			task.Deps = []core.TaskID{prev}
		}
		if err := be.Tasks().Create(ctx, task); err != nil {
			tb.Fatalf("create task %d: %v", i, err)
		}
		prev = id
	}
	return project.ID
}

// syntheticTasks is the in-memory twin of seedTasks, for benchmarks that time
// the graph and ranker directly.
func syntheticTasks(n int) []core.Task {
	tasks := make([]core.Task, n)
	for i := range tasks {
		tasks[i] = core.Task{
			ID: core.TaskID(fmt.Sprintf("t-%06d", i)), ProjectID: "prj", Kind: core.KindTask,
			Title: fmt.Sprintf("task %d", i), Status: core.StatusTodo, Priority: i % 5,
			CreatedAt: time.Unix(0, 0).UTC(),
		}
		if i > 0 && i%10 == 0 {
			tasks[i].Deps = []core.TaskID{tasks[i-1].ID}
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
	ptrs := make([]*core.Task, len(tasks))
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

// TestHotPathBudgetSmoke is the CI tier: it seeds 10k tasks and fails only on an
// order-of-magnitude regression, not on ordinary machine noise.
func TestHotPathBudgetSmoke(t *testing.T) {
	ctx := context.Background()
	be := memory.New()
	t.Cleanup(func() { _ = be.Close() })
	projectID := seedTasks(t, be, 10000)

	start := time.Now()
	if _, err := LoadSnapshot(ctx, be, projectID, time.Unix(0, 0).UTC()); err != nil {
		t.Fatalf("LoadSnapshot() error = %v", err)
	}
	if elapsed := time.Since(start); elapsed > smokeCeiling {
		t.Fatalf("LoadSnapshot at 10k tasks took %v, over the %v smoke ceiling (hot-path budget %v)", elapsed, smokeCeiling, hotPathBudget10k)
	}
}
