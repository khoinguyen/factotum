package perf

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
	"github.com/khoinguyen/factotum/pkg/store/memory"
	"github.com/khoinguyen/factotum/pkg/store/sqlite"
)

func itoa(n int) string { return strconv.Itoa(n) }

// benchmarkScales is the per-backend scale set the store micro-benchmarks use.
// jsonfile is capped so its O(n^2) seed does not dominate the benchmark run.
func benchmarkScales(backend string) []int {
	if backend == "jsonfile" {
		return []int{1000}
	}
	return []int{1000, 10000}
}

func openBenchBackend(tb testing.TB, backend string) store.Backend {
	tb.Helper()
	dir := tb.TempDir()
	ctx := context.Background()
	var (
		be  store.Backend
		err error
	)
	switch backend {
	case "memory":
		be = memory.New()
	case "jsonfile":
		be, err = jsonfile.Open(ctx, store.Config{Backend: backend, Options: map[string]string{"path": filepath.Join(dir, "db.json")}})
	case "sqlite":
		be, err = sqlite.Open(ctx, store.Config{Backend: backend, Options: map[string]string{"path": filepath.Join(dir, "db.db")}})
	default:
		tb.Fatalf("unknown backend %q", backend)
	}
	if err != nil {
		tb.Fatalf("open %s: %v", backend, err)
	}
	tb.Cleanup(func() { _ = be.Close() })
	return be
}

func seedBenchBackend(tb testing.TB, backend string, spec Spec) store.Backend {
	tb.Helper()
	be := openBenchBackend(tb, backend)
	if _, err := Seed(context.Background(), be, spec); err != nil {
		tb.Fatalf("seed %s: %v", backend, err)
	}
	return be
}

// BenchmarkTaskList measures the direct store List path (load every task in a
// project), which LoadSnapshot calls implicitly.
func BenchmarkTaskList(b *testing.B) {
	ctx := context.Background()
	for _, backend := range []string{"memory", "sqlite", "jsonfile"} {
		for _, n := range benchmarkScales(backend) {
			b.Run(backend+"/"+itoa(n), func(b *testing.B) {
				be := seedBenchBackend(b, backend, Spec{Tasks: n})
				filter := store.TaskFilter{ProjectID: DefaultProjectID}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := be.Tasks().List(ctx, filter); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkTaskSearch measures the direct lexical search path: a term that
// matches every synthetic task.
func BenchmarkTaskSearch(b *testing.B) {
	ctx := context.Background()
	for _, backend := range []string{"memory", "sqlite", "jsonfile"} {
		for _, n := range benchmarkScales(backend) {
			b.Run(backend+"/"+itoa(n), func(b *testing.B) {
				be := seedBenchBackend(b, backend, Spec{Tasks: n})
				filter := store.TaskFilter{ProjectID: DefaultProjectID}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := be.Tasks().Search(ctx, filter, "synthetic"); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkEventList measures event list bounded to the newest 20 (the shape
// `event list` and `task context` use) as the events table grows, to show that
// the audit log stays bounded under load.
func BenchmarkEventList(b *testing.B) {
	ctx := context.Background()
	for _, backend := range []string{"memory", "sqlite"} {
		for _, n := range []int{1000, 10000} {
			b.Run(backend+"/"+itoa(n), func(b *testing.B) {
				be := seedBenchBackend(b, backend, Spec{Tasks: n, Events: n})
				filter := store.EventFilter{ProjectID: DefaultProjectID, Limit: 20}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := be.Events().List(ctx, filter); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// BenchmarkJSONFileWriteAmplification measures a single mutation (Update of an
// existing task) at several state sizes. jsonfile rewrites its whole document on
// every write, so the cost should climb with state size; this is the measurement
// behind JSONFileSeedCap and t-ewr3xidxtk.
func BenchmarkJSONFileWriteAmplification(b *testing.B) {
	ctx := context.Background()
	for _, n := range []int{100, 1000, 2000} {
		b.Run(itoa(n), func(b *testing.B) {
			be := seedBenchBackend(b, "jsonfile", Spec{Tasks: n})
			task, err := be.Tasks().Get(ctx, core.TaskID("t-000000"))
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				task.UpdatedAt = task.UpdatedAt.Add(time.Millisecond)
				if err := be.Tasks().Update(ctx, task); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
