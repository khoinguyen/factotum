package graph

import (
	"errors"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

func task(id string, kind core.TaskKind, status core.TaskStatus, deps ...string) core.Task {
	t := core.Task{
		ID:        core.TaskID(id),
		ProjectID: "prj",
		Kind:      kind,
		Title:     id,
		Status:    status,
	}
	for _, d := range deps {
		t.Deps = append(t.Deps, core.TaskID(d))
	}
	return t
}

func mustGraph(t *testing.T, tasks ...core.Task) *Graph {
	t.Helper()
	g, err := New(tasks, core.DefaultResolutionPolicy())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return g
}

func equalIDs(got, want []core.TaskID) bool {
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

func TestNewRejectsDuplicateIDs(t *testing.T) {
	_, err := New([]core.Task{task("t1", core.KindTask, core.StatusTodo), task("t1", core.KindTask, core.StatusTodo)}, core.DefaultResolutionPolicy())
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
	want := []core.TaskID{"m1", "t1", "t6", "t8"}
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
	want := []core.TaskID{"todo"}
	if got := g.ReadySet(); !equalIDs(got, want) {
		t.Fatalf("ReadySet() = %v, want %v", got, want)
	}
}

func TestExternalDeps(t *testing.T) {
	g := mustGraph(t,
		task("t1", core.KindTask, core.StatusTodo, "ghost", "phantom"),
		task("t2", core.KindTask, core.StatusTodo, "t1"),
	)
	want := []core.TaskID{"ghost", "phantom"}
	if got := g.ExternalDeps(); !equalIDs(got, want) {
		t.Fatalf("ExternalDeps() = %v, want %v", got, want)
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
	t2 := task("t2", core.KindTask, core.StatusTodo)
	t2.AssigneeID = &humanID
	t3 := task("t3", core.KindTask, core.StatusTodo)

	g := mustGraph(t, t1, t2, t3)
	got := g.ReadyByActor(actors)

	if !equalIDs(got.Agent, []core.TaskID{"t1"}) {
		t.Fatalf("Agent = %v, want [t1]", got.Agent)
	}
	if !equalIDs(got.Human, []core.TaskID{"t2", "t3"}) {
		t.Fatalf("Human = %v, want [t2 t3]", got.Human)
	}
}

func TestCycles(t *testing.T) {
	tests := []struct {
		name  string
		tasks []core.Task
		want  [][]core.TaskID
	}{
		{
			"none",
			[]core.Task{task("t1", core.KindTask, core.StatusTodo), task("t2", core.KindTask, core.StatusTodo, "t1")},
			nil,
		},
		{
			"two cycle",
			[]core.Task{task("t1", core.KindTask, core.StatusTodo, "t2"), task("t2", core.KindTask, core.StatusTodo, "t1")},
			[][]core.TaskID{{"t1", "t2"}},
		},
		{
			"three cycle",
			[]core.Task{
				task("t1", core.KindTask, core.StatusTodo, "t3"),
				task("t2", core.KindTask, core.StatusTodo, "t1"),
				task("t3", core.KindTask, core.StatusTodo, "t2"),
			},
			[][]core.TaskID{{"t1", "t2", "t3"}},
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
	want := map[core.TaskID]int{"a": 0, "b": 1, "c": 2, "d": 1}
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
	pos := map[core.TaskID]int{}
	for i, id := range order {
		pos[id] = i
	}
	if pos["a"] >= pos["b"] || pos["b"] >= pos["c"] {
		t.Fatalf("TopoSort() = %v, violates dependencies", order)
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
	want := []core.TaskID{"b", "c", "d"}
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
	if !equalIDs(path, []core.TaskID{"a", "b", "c"}) && !equalIDs(path, []core.TaskID{"a", "b2", "c"}) {
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
	if !equalIDs(path, []core.TaskID{"a", "b", "c"}) {
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
