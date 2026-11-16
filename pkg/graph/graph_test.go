package graph

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

func task(id string, kind core.TicketKind, status core.TicketStatus, deps ...string) core.Ticket {
	t := core.Ticket{
		ID:        core.TicketID(id),
		ProjectID: "prj",
		Kind:      kind,
		Title:     id,
		Status:    status,
	}
	for _, d := range deps {
		t.Deps = append(t.Deps, core.TicketID(d))
	}
	return t
}

func mustGraph(t *testing.T, tasks ...core.Ticket) *Graph {
	t.Helper()
	g, err := New(tasks, core.DefaultResolutionPolicy())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return g
}

func equalIDs(got, want []core.TicketID) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func TestReadySetExcludesNotBefore(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	ready := task("ready", core.KindTask, core.StatusTodo)
	pastTask := task("past", core.KindTask, core.StatusTodo)
	pastTask.NotBefore = &past
	deferred := task("deferred", core.KindTask, core.StatusTodo)
	deferred.NotBefore = &future
	dependent := task("dependent", core.KindTask, core.StatusTodo, "deferred")

	g, err := NewAt([]core.Ticket{ready, pastTask, deferred, dependent}, core.DefaultResolutionPolicy(), now)
	if err != nil {
		t.Fatalf("NewAt() error = %v", err)
	}
	if got := g.ReadySet(); !equalIDs(got, []core.TicketID{"past", "ready"}) {
		t.Fatalf("ReadySet() = %v, want [past ready]", got)
	}

	later, err := NewAt([]core.Ticket{ready, pastTask, deferred, dependent}, core.DefaultResolutionPolicy(), future.Add(time.Hour))
	if err != nil {
		t.Fatalf("NewAt() error = %v", err)
	}
	if got := later.ReadySet(); !equalIDs(got, []core.TicketID{"deferred", "past", "ready"}) {
		t.Fatalf("ReadySet() after deadline = %v, want [deferred past ready]", got)
	}
}

func TestReadySetExcludesSnoozed(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)

	open := task("open", core.KindTask, core.StatusTodo)
	snoozedUntil := task("snoozed-until", core.KindTask, core.StatusTodo)
	snoozedUntil.Snooze = &core.Snooze{Until: &future}
	woken := task("woken", core.KindTask, core.StatusTodo)
	woken.Snooze = &core.Snooze{Until: &past}
	parked := task("parked", core.KindTask, core.StatusTodo)
	parked.Snooze = &core.Snooze{Indefinite: true}
	blocker := task("blocker", core.KindTask, core.StatusTodo)
	untilTask := core.TicketID("blocker")
	waiting := task("waiting", core.KindTask, core.StatusTodo)
	waiting.Snooze = &core.Snooze{UntilTask: &untilTask}

	tasks := []core.Ticket{open, snoozedUntil, woken, parked, blocker, waiting}
	g, err := NewAt(tasks, core.DefaultResolutionPolicy(), now)
	if err != nil {
		t.Fatalf("NewAt() error = %v", err)
	}
	if got := g.ReadySet(); !equalIDs(got, []core.TicketID{"blocker", "open", "woken"}) {
		t.Fatalf("ReadySet() = %v, want [blocker open woken]", got)
	}

	// Once the blocker resolves, the until-task snooze wakes automatically.
	blocker.Status = core.StatusDone
	later, err := NewAt([]core.Ticket{open, snoozedUntil, woken, parked, blocker, waiting}, core.DefaultResolutionPolicy(), now)
	if err != nil {
		t.Fatalf("NewAt() error = %v", err)
	}
	if got := later.ReadySet(); !equalIDs(got, []core.TicketID{"open", "waiting", "woken"}) {
		t.Fatalf("ReadySet() after blocker = %v, want [open waiting woken]", got)
	}
}

func TestReadySetIgnoresNotBeforeWithoutClock(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)
	deferred := task("deferred", core.KindTask, core.StatusTodo)
	deferred.NotBefore = &future

	g := mustGraph(t, deferred)
	if got := g.ReadySet(); !equalIDs(got, []core.TicketID{"deferred"}) {
		t.Fatalf("ReadySet() = %v, want [deferred]", got)
	}
}

func TestNewRejectsDuplicateIDs(t *testing.T) {
	_, err := New([]core.Ticket{task("t1", core.KindTask, core.StatusTodo), task("t1", core.KindTask, core.StatusTodo)}, core.DefaultResolutionPolicy())
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("New() error = %v, want ErrConflict", err)
	}
}

func TestReadySet(t *testing.T) {
	g := mustGraph(t,
		task("t1", core.KindTask, core.StatusTodo),
		task("t2", core.KindTask, core.StatusTodo, "t1"),
		task("t3", core.KindTask, core.StatusInProgress),
		task("t4", core.KindTask, core.StatusTodo, "t3"),
		task("t5", core.KindTask, core.StatusReadyForReview),
		task("t6", core.KindTask, core.StatusTodo, "t5"),
		task("m1", core.KindMilestone, core.StatusReadyForReview),
		task("t7", core.KindTask, core.StatusTodo, "m1"),
		task("m2", core.KindMilestone, core.StatusDone),
		task("t8", core.KindTask, core.StatusTodo, "m2"),
		task("t9", core.KindTask, core.StatusTodo, "ghost"),
	)

	// m1 is a milestone in ready_for_review: still open work (needs its explicit
	// Done gate), so it is startable, but it does not unblock t7.
	want := []core.TicketID{"m1", "t1", "t6", "t8"}
	if got := g.ReadySet(); !equalIDs(got, want) {
		t.Fatalf("ReadySet() = %v, want %v", got, want)
	}
}

func TestReadySetExcludesBlockedAndInProgress(t *testing.T) {
	g := mustGraph(t,
		task("todo", core.KindTask, core.StatusTodo),
		task("blocked", core.KindTask, core.StatusBlocked),
		task("doing", core.KindTask, core.StatusInProgress),
		task("after-blocked", core.KindTask, core.StatusTodo, "blocked"),
		task("after-doing", core.KindTask, core.StatusTodo, "doing"),
	)
	want := []core.TicketID{"todo"}
	if got := g.ReadySet(); !equalIDs(got, want) {
		t.Fatalf("ReadySet() = %v, want %v", got, want)
	}
}

func TestExternalDeps(t *testing.T) {
	g := mustGraph(t,
		task("t1", core.KindTask, core.StatusTodo, "ghost", "phantom"),
		task("t2", core.KindTask, core.StatusTodo, "t1"),
	)
	want := []core.TicketID{"ghost", "phantom"}
	if got := g.ExternalDeps(); !equalIDs(got, want) {
		t.Fatalf("ExternalDeps() = %v, want %v", got, want)
	}
}

// A dependency declared as an out-of-scope capture is provenance, never a
// blocker: it must not exclude its dependent from the ready set, add a wave, or
// count as an external dependency, even though its ticket is absent from the
// graph. A dangling dep that is not declared keeps blocking, so a real foreign
// task is unaffected.
func TestExternalCapturesDoNotBlock(t *testing.T) {
	captureOnly := task("only", core.KindTask, core.StatusTodo, "idea")
	withGhost := task("ghosted", core.KindTask, core.StatusTodo, "idea", "ghost")
	g, err := NewAtCaptures([]core.Ticket{captureOnly, withGhost}, core.DefaultResolutionPolicy(), time.Time{}, []core.TicketID{"idea"})
	if err != nil {
		t.Fatalf("NewAtCaptures() error = %v", err)
	}
	if ready, reason := g.Readiness("only"); !ready || reason != nil {
		t.Fatalf("Readiness(only) = (%v, %+v), want ready with no reason", ready, reason)
	}
	if ready, reason := g.Readiness("ghosted"); ready || reason == nil || reason.Code != ReasonDepUnresolved || reason.Detail != "ghost" {
		t.Fatalf("Readiness(ghosted) = (%v, %+v), want not ready with dep_unresolved/ghost", ready, reason)
	}
	if got := g.ExternalDeps(); !equalIDs(got, []core.TicketID{"ghost"}) {
		t.Fatalf("ExternalDeps() = %v, want [ghost] (the capture is not a blocking external dep)", got)
	}
	waves, err := g.Waves()
	if err != nil {
		t.Fatalf("Waves() error = %v", err)
	}
	if waves["only"] != 0 {
		t.Fatalf("waves[only] = %d, want 0 (the capture adds no wave)", waves["only"])
	}
	if waves["ghosted"] != 1 {
		t.Fatalf("waves[ghosted] = %d, want 1 (only ghost adds a wave)", waves["ghosted"])
	}
}

// A real task in another project is not a capture, so its edge still blocks
// readiness: the fix is scoped to captures.
func TestUndeclaredExternalDepStillBlocks(t *testing.T) {
	dep := task("mine", core.KindTask, core.StatusTodo, "foreign")
	g, err := NewAtCaptures([]core.Ticket{dep}, core.DefaultResolutionPolicy(), time.Time{}, nil)
	if err != nil {
		t.Fatalf("NewAtCaptures() error = %v", err)
	}
	if ready, reason := g.Readiness("mine"); ready || reason == nil || reason.Code != ReasonDepUnresolved {
		t.Fatalf("Readiness(mine) = (%v, %+v), want not ready with dep_unresolved", ready, reason)
	}
}

func TestReadyByActor(t *testing.T) {
	agentID := core.ActorID("ag")
	humanID := core.ActorID("hu")
	actors := map[core.ActorID]core.Actor{
		agentID: {ID: agentID, Kind: core.ActorAgent, Name: "claude"},
		humanID: {ID: humanID, Kind: core.ActorHuman, Name: "Khoi"},
	}

	t1 := task("t1", core.KindTask, core.StatusTodo)
	t1.AssigneeID = &agentID
	t1.Groomed = true
	t1.AcceptanceCriteria = []string{"done"}
	t2 := task("t2", core.KindTask, core.StatusTodo)
	t2.AssigneeID = &humanID
	t3 := task("t3", core.KindTask, core.StatusTodo)
	// An agent-assigned but ungroomed task is not agent-ready: it still needs a
	// human to define its scope and acceptance criteria.
	t4 := task("t4", core.KindTask, core.StatusTodo)
	t4.AssigneeID = &agentID

	g := mustGraph(t, t1, t2, t3, t4)
	got := g.ReadyByActor(actors)

	if !equalIDs(got.Agent, []core.TicketID{"t1"}) {
		t.Fatalf("Agent = %v, want [t1]", got.Agent)
	}
	if !equalIDs(got.Human, []core.TicketID{"t2", "t3", "t4"}) {
		t.Fatalf("Human = %v, want [t2 t3 t4]", got.Human)
	}
}

func TestCycles(t *testing.T) {
	tests := []struct {
		name  string
		tasks []core.Ticket
		want  [][]core.TicketID
	}{
		{
			"none",
			[]core.Ticket{task("t1", core.KindTask, core.StatusTodo), task("t2", core.KindTask, core.StatusTodo, "t1")},
			nil,
		},
		{
			"two cycle",
			[]core.Ticket{task("t1", core.KindTask, core.StatusTodo, "t2"), task("t2", core.KindTask, core.StatusTodo, "t1")},
			[][]core.TicketID{{"t1", "t2"}},
		},
		{
			"three cycle",
			[]core.Ticket{
				task("t1", core.KindTask, core.StatusTodo, "t3"),
				task("t2", core.KindTask, core.StatusTodo, "t1"),
				task("t3", core.KindTask, core.StatusTodo, "t2"),
			},
			[][]core.TicketID{{"t1", "t2", "t3"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := mustGraph(t, tt.tasks...)
			got := g.Cycles()
			if len(got) != len(tt.want) {
				t.Fatalf("Cycles() = %v, want %v", got, tt.want)
			}
			for i := range got {
				if !equalIDs(got[i], tt.want[i]) {
					t.Fatalf("Cycles()[%d] = %v, want %v", i, got[i], tt.want[i])
				}
			}
			if got := g.HasCycle(); got != (len(tt.want) > 0) {
				t.Fatalf("HasCycle() = %v, want %v", got, len(tt.want) > 0)
			}
		})
	}
}

func TestWaves(t *testing.T) {
	g := mustGraph(t,
		task("a", core.KindTask, core.StatusTodo),
		task("b", core.KindTask, core.StatusTodo, "a"),
		task("c", core.KindTask, core.StatusTodo, "b"),
		task("d", core.KindTask, core.StatusTodo, "a"),
	)
	waves, err := g.Waves()
	if err != nil {
		t.Fatalf("Waves() error = %v", err)
	}
	want := map[core.TicketID]int{"a": 0, "b": 1, "c": 2, "d": 1}
	for id, w := range want {
		if waves[id] != w {
			t.Errorf("wave(%s) = %d, want %d", id, waves[id], w)
		}
	}
	deep, err := g.WavesDeep()
	if err != nil {
		t.Fatalf("WavesDeep() error = %v", err)
	}
	if deep != 3 {
		t.Fatalf("WavesDeep() = %d, want 3", deep)
	}
}

func TestWavesContractResolvedDeps(t *testing.T) {
	g := mustGraph(t,
		task("done1", core.KindTask, core.StatusDone),
		task("mid", core.KindTask, core.StatusTodo, "done1"),
		task("blocked", core.KindTask, core.StatusTodo, "mid"),
	)
	waves, err := g.Waves()
	if err != nil {
		t.Fatalf("Waves() error = %v", err)
	}
	if waves["mid"] != 0 {
		t.Fatalf("wave(mid) = %d, want 0 (its only blocker is resolved)", waves["mid"])
	}
	if waves["blocked"] != 1 {
		t.Fatalf("wave(blocked) = %d, want 1", waves["blocked"])
	}
	deep, err := g.WavesDeep()
	if err != nil {
		t.Fatalf("WavesDeep() error = %v", err)
	}
	if deep != 2 {
		t.Fatalf("WavesDeep() = %d, want 2", deep)
	}
}

func TestForestKeepsStructuralParentAcrossResolution(t *testing.T) {
	g := mustGraph(t,
		task("root", core.KindTask, core.StatusDone),
		task("child", core.KindTask, core.StatusTodo, "root"),
	)
	forest, err := g.Forest()
	if err != nil {
		t.Fatalf("Forest() error = %v", err)
	}
	if forest["child"] != "root" {
		t.Fatalf("forest[child] = %q, want root", forest["child"])
	}
}

func TestCyclesBlockOrdering(t *testing.T) {
	g := mustGraph(t,
		task("t1", core.KindTask, core.StatusTodo, "t2"),
		task("t2", core.KindTask, core.StatusTodo, "t1"),
	)
	if _, err := g.TopoSort(); !errors.Is(err, core.ErrCycle) {
		t.Fatalf("TopoSort() error = %v, want ErrCycle", err)
	}
	if _, err := g.Waves(); !errors.Is(err, core.ErrCycle) {
		t.Fatalf("Waves() error = %v, want ErrCycle", err)
	}
	if _, err := g.CriticalPath(); !errors.Is(err, core.ErrCycle) {
		t.Fatalf("CriticalPath() error = %v, want ErrCycle", err)
	}
	if _, err := g.Forest(); !errors.Is(err, core.ErrCycle) {
		t.Fatalf("Forest() error = %v, want ErrCycle", err)
	}
}

func TestTopoSort(t *testing.T) {
	g := mustGraph(t,
		task("c", core.KindTask, core.StatusTodo, "a", "b"),
		task("a", core.KindTask, core.StatusTodo),
		task("b", core.KindTask, core.StatusTodo, "a"),
	)
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort() error = %v", err)
	}
	if len(order) != 3 {
		t.Fatalf("TopoSort() = %v, want 3 nodes", order)
	}
	pos := map[core.TicketID]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["a"] >= pos["b"] || pos["b"] >= pos["c"] {
		t.Fatalf("TopoSort() = %v, violates dependencies", order)
	}
}

// TestTopoSortIsLexicographicallyMinimal pins the ordering contract: among the
// tasks whose dependencies are all already emitted, TopoSort always emits the
// smallest id next, including when a dependency unlocks an id smaller than ids
// still waiting. The heap-backed implementation must reproduce this exactly.
func TestTopoSortIsLexicographicallyMinimal(t *testing.T) {
	g := mustGraph(t,
		task("a", core.KindTask, core.StatusTodo, "x"),
		task("w", core.KindTask, core.StatusTodo),
		task("x", core.KindTask, core.StatusTodo, "w"),
		task("z", core.KindTask, core.StatusTodo),
	)
	order, err := g.TopoSort()
	if err != nil {
		t.Fatalf("TopoSort() error = %v", err)
	}
	want := []core.TicketID{"w", "x", "a", "z"}
	if !equalIDs(order, want) {
		t.Fatalf("TopoSort() = %v, want %v", order, want)
	}
}

// wideGraph builds n tasks with no dependencies, so every task is ready at
// once: the worst case for a frontier rescan.
func wideGraph(t *testing.T, n int) *Graph {
	t.Helper()
	tasks := make([]core.Ticket, n)
	for i := range tasks {
		tasks[i] = task(fmt.Sprintf("t-%06d", i), core.KindTask, core.StatusTodo)
	}
	return mustGraph(t, tasks...)
}

// topoSortCeiling is the CPU-time budget for TopoSort on the wide graph, sized
// for the expensive stage `mise run ci` runs: `cover` (`-race -covermode=atomic`)
// costs about six times the `test` stage's race build, because atomic coverage
// counters and the race shadow sit inside this pointer-chasing loop. The two
// consts below record the measurements the ceiling's bounds come from.
const topoSortCeiling = 1200 * time.Millisecond

// topoSortCoverTail is the worst CPU time measured for one TopoSort on the wide
// graph in the stage that failed: `mise run ci`'s cover stage runs
// `go test -race -covermode=atomic`, where the same call costs ~123ms p50
// against ~21ms in the plain `test` stage. On a machine loaded by parallel
// suites it reached 259.169ms, over the old 250ms ceiling, while a register-only
// control loop in the same process stayed flat: the growth is cache and
// bandwidth contention on this memory-bound loop, not a contention bug in
// TopoSort (t-nbcpypbds2).
const topoSortCoverTail = 260 * time.Millisecond

// topoSortQuadraticRegression is the CPU time the pre-73e93ab quadratic rescan
// consumed on the same wide graph: measured by putting that rescan back into
// TopoSort, 5.5s in the `test` stage and 15.2s in `cover` at 20000 tasks. The
// cover figure is higher because the sort closure's comparisons each bump an
// atomic coverage counter. The smaller of the two is the upper bound the ceiling
// must stay well under to keep the guard.
const topoSortQuadraticRegression = 5500 * time.Millisecond

// TestTopoSortWideGraphBudget guards against the quadratic queue rescan that
// made graph render --format agent superlinear (t-lvlqecjqgm): the old
// implementation sorted the whole remaining queue on every pop, which is
// O(V^2 log V) when most tasks are ready at once. A wide graph is the worst
// case.
//
// The budget measures process CPU time, not wall time. `mise run ci` runs
// `cover` concurrently with `test`, and on a contended hosted runner that load
// inflated wall-clock TopoSort past 500ms while the work itself was unchanged
// (t-xfrpkkt4mx). CPU time does not count the time the process spends waiting
// for a core, so scheduling contention no longer moves the number. It is not
// free of the loaded machine's effects: this work is memory-bound, so cache and
// bandwidth contention can still inflate it by about 2x, which is why the
// ceiling is sized with headroom over the measured cover-stage tail rather than
// just above the typical run (t-nbcpypbds2).
func TestTopoSortWideGraphBudget(t *testing.T) {
	const n = 20000
	g := wideGraph(t, n)

	var (
		order []core.TicketID
		err   error
	)
	elapsed := measureCPU(func() { order, err = g.TopoSort() })
	if err != nil {
		t.Fatalf("TopoSort() error = %v", err)
	}
	if len(order) != n {
		t.Fatalf("TopoSort() = %d nodes, want %d", len(order), n)
	}
	if elapsed > topoSortCeiling {
		t.Fatalf("TopoSort() on %d tasks used %v of CPU, over the %v ceiling", n, elapsed, topoSortCeiling)
	}
	t.Logf("TopoSort() on %d tasks used %v of CPU (ceiling %v)", n, elapsed, topoSortCeiling)
}

// TestTopoSortCeilingIsSizedByMeasurement pins both halves of the ceiling: it
// must keep headroom over the cover-stage tail that actually failed, and it must
// stay well under the quadratic rescan it exists to catch. A ceiling tightened
// back towards the typical run flakes on a loaded machine; one raised towards
// the regression stops being a guard.
func TestTopoSortCeilingIsSizedByMeasurement(t *testing.T) {
	if want := 4 * topoSortCoverTail; topoSortCeiling < want {
		t.Fatalf("topoSortCeiling = %v, want at least %v: four times the %v cover-stage tail measured under parallel load (t-nbcpypbds2); a tighter ceiling races the machine instead of guarding the algorithm", topoSortCeiling, want, topoSortCoverTail)
	}
	if want := 3; topoSortCeiling*time.Duration(want) > topoSortQuadraticRegression {
		t.Fatalf("topoSortCeiling = %v is within %dx of the %v quadratic rescan it must catch; the budget no longer guards against the regression", topoSortCeiling, want, topoSortQuadraticRegression)
	}
}

// measureCPU returns the process CPU time fn consumed. CPU time, not wall
// time, keeps the budget out of a loaded machine's scheduling tail: it does not
// inflate when other processes (a concurrent `cover` run) compete for a core.
// It still tracks work done on this process's behalf, including GC that runs
// inside fn.
func measureCPU(fn func()) time.Duration {
	start := processCPUTime()
	fn()
	return processCPUTime() - start
}

func BenchmarkTopoSort(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			tasks := make([]core.Ticket, n)
			for i := range tasks {
				tasks[i] = task(fmt.Sprintf("t-%06d", i), core.KindTask, core.StatusTodo)
			}
			g, err := New(tasks, core.DefaultResolutionPolicy())
			if err != nil {
				b.Fatal(err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := g.TopoSort(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestUnblockCount(t *testing.T) {
	g := mustGraph(t,
		task("r", core.KindTask, core.StatusTodo),
		task("x", core.KindTask, core.StatusTodo, "r"),
		task("y", core.KindTask, core.StatusTodo, "r"),
		task("z", core.KindTask, core.StatusTodo, "x", "y"),
	)
	if got := g.UnblockCount("r"); got != 2 {
		t.Fatalf("UnblockCount(r) = %d, want 2", got)
	}
	if got := g.UnblockCount("x"); got != 0 {
		t.Fatalf("UnblockCount(x) = %d, want 0", got)
	}
}

func TestTransitiveDependents(t *testing.T) {
	g := mustGraph(t,
		task("a", core.KindTask, core.StatusTodo),
		task("b", core.KindTask, core.StatusTodo, "a"),
		task("c", core.KindTask, core.StatusTodo, "b"),
		task("d", core.KindTask, core.StatusTodo, "c"),
	)
	want := []core.TicketID{"b", "c", "d"}
	if got := g.TransitiveDependents("a"); !equalIDs(got, want) {
		t.Fatalf("TransitiveDependents(a) = %v, want %v", got, want)
	}
}

func TestDistanceToMilestone(t *testing.T) {
	g := mustGraph(t,
		task("a", core.KindTask, core.StatusTodo),
		task("m", core.KindMilestone, core.StatusTodo, "a"),
		task("c", core.KindTask, core.StatusTodo, "m"),
	)
	if d, ok := g.DistanceToMilestone("a"); !ok || d != 1 {
		t.Fatalf("DistanceToMilestone(a) = (%d, %v), want (1, true)", d, ok)
	}
	if d, ok := g.DistanceToMilestone("m"); !ok || d != 0 {
		t.Fatalf("DistanceToMilestone(m) = (%d, %v), want (0, true)", d, ok)
	}
	if _, ok := g.DistanceToMilestone("c"); ok {
		t.Fatalf("DistanceToMilestone(c) ok = true, want false")
	}
}

func TestShortestPathTo(t *testing.T) {
	g := mustGraph(t,
		task("a", core.KindTask, core.StatusTodo),
		task("b", core.KindTask, core.StatusTodo, "a"),
		task("b2", core.KindTask, core.StatusTodo, "a"),
		task("c", core.KindTask, core.StatusTodo, "b", "b2"),
	)
	path, ok := g.ShortestPathTo("a", "c")
	if !ok {
		t.Fatalf("ShortestPathTo(a,c) ok = false")
	}
	if !equalIDs(path, []core.TicketID{"a", "b", "c"}) && !equalIDs(path, []core.TicketID{"a", "b2", "c"}) {
		t.Fatalf("ShortestPathTo(a,c) = %v, want a shortest path", path)
	}
	if _, ok := g.ShortestPathTo("c", "a"); ok {
		t.Fatalf("ShortestPathTo(c,a) ok = true, want false")
	}
}

func TestCriticalPath(t *testing.T) {
	g := mustGraph(t,
		task("a", core.KindTask, core.StatusTodo),
		task("b", core.KindTask, core.StatusTodo, "a"),
		task("c", core.KindTask, core.StatusTodo, "b"),
		task("d", core.KindTask, core.StatusTodo, "a"),
	)
	path, err := g.CriticalPath()
	if err != nil {
		t.Fatalf("CriticalPath() error = %v", err)
	}
	if !equalIDs(path, []core.TicketID{"a", "b", "c"}) {
		t.Fatalf("CriticalPath() = %v, want [a b c]", path)
	}
}

func TestForestCanonicalParent(t *testing.T) {
	g := mustGraph(t,
		task("r", core.KindTask, core.StatusTodo),
		task("x", core.KindTask, core.StatusTodo, "r"),
		task("y", core.KindTask, core.StatusTodo, "r"),
		task("z", core.KindTask, core.StatusTodo, "x", "y"),
	)
	forest, err := g.Forest()
	if err != nil {
		t.Fatalf("Forest() error = %v", err)
	}
	if _, ok := forest["r"]; ok {
		t.Fatalf("root r should have no canonical parent")
	}
	if forest["x"] != "r" || forest["y"] != "r" {
		t.Fatalf("forest = %v, want x,y -> r", forest)
	}
	if forest["z"] != "x" {
		t.Fatalf("forest[z] = %q, want x (lowest wave, then id)", forest["z"])
	}
}

func TestForestPrefersShallowerParent(t *testing.T) {
	g := mustGraph(t,
		task("r", core.KindTask, core.StatusTodo),
		task("deep", core.KindTask, core.StatusTodo, "r"),
		task("shallow", core.KindTask, core.StatusTodo),
		task("z", core.KindTask, core.StatusTodo, "deep", "shallow"),
	)
	forest, err := g.Forest()
	if err != nil {
		t.Fatalf("Forest() error = %v", err)
	}
	if forest["z"] != "shallow" {
		t.Fatalf("forest[z] = %q, want shallow (lower wave)", forest["z"])
	}
}
