package rank

import (
	"context"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
)

func task(id string, status core.TaskStatus, deps ...string) core.Task {
	t := core.Task{ID: core.TaskID(id), ProjectID: "prj", Kind: core.KindTask, Title: id, Status: status}
	for _, dep := range deps {
		t.Deps = append(t.Deps, core.TaskID(dep))
	}
	return t
}

func milestone(id string, status core.TaskStatus, deps ...string) core.Task {
	t := task(id, status, deps...)
	t.Kind = core.KindMilestone
	return t
}

func request(t *testing.T, tasks []core.Task, toward core.TaskID) Request {
	t.Helper()
	g, err := graph.New(tasks, core.DefaultResolutionPolicy())
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}
	pointers := make([]*core.Task, 0, len(tasks))
	for i := range tasks {
		pointers = append(pointers, &tasks[i])
	}
	return Request{Graph: g, Tasks: pointers, Toward: toward}
}

func order(scored []Scored) []core.TaskID {
	out := make([]core.TaskID, 0, len(scored))
	for _, s := range scored {
		out = append(out, s.TaskID)
	}
	return out
}

func scoresByID(scored []Scored) map[core.TaskID]float64 {
	out := make(map[core.TaskID]float64, len(scored))
	for _, s := range scored {
		out[s.TaskID] = s.Score
	}
	return out
}

func equalOrder(got, want []core.TaskID) bool {
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

func TestUnblockRankerOrdersByImpact(t *testing.T) {
	tasks := []core.Task{
		task("a", core.StatusTodo),
		task("a1", core.StatusTodo, "a"),
		task("a2", core.StatusTodo, "a"),
		task("b", core.StatusTodo),
		task("b1", core.StatusTodo, "b"),
		task("c", core.StatusTodo),
	}
	scored, err := Unblock{}.Rank(context.Background(), request(t, tasks, ""))
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"a", "b", "c"}) {
		t.Fatalf("Rank() = %v, want [a b c]", order(scored))
	}
}

func TestMilestoneRankerPrefersCloserTasks(t *testing.T) {
	tasks := []core.Task{
		task("near", core.StatusTodo),
		milestone("m1", core.StatusTodo, "near"),
		task("far", core.StatusTodo),
		task("mid", core.StatusTodo, "far"),
		milestone("m2", core.StatusTodo, "mid"),
	}
	scored, err := Milestone{}.Rank(context.Background(), request(t, tasks, ""))
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"near", "far"}) {
		t.Fatalf("Rank() = %v, want [near far]", order(scored))
	}
}

func TestTowardRankerPrefersPathToTarget(t *testing.T) {
	tasks := []core.Task{
		task("onpath", core.StatusTodo),
		task("middle", core.StatusTodo, "onpath"),
		task("target", core.StatusTodo, "middle"),
		task("offpath", core.StatusTodo),
	}
	scored, err := Toward{}.Rank(context.Background(), request(t, tasks, "target"))
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"onpath", "offpath"}) {
		t.Fatalf("Rank() = %v, want [onpath offpath]", order(scored))
	}
}

func TestCompositeRanksByPriorityWeight(t *testing.T) {
	high := task("high", core.StatusTodo)
	high.Priority = 5
	low := task("low", core.StatusTodo)
	low.Priority = 1
	tasks := []core.Task{low, high}

	composite, err := NewComposite(DefaultWeights())
	if err != nil {
		t.Fatalf("NewComposite() error = %v", err)
	}
	scored, err := composite.Rank(context.Background(), request(t, tasks, ""))
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"high", "low"}) {
		t.Fatalf("Rank() = %v, want [high low]", order(scored))
	}
	byID := scoresByID(scored)
	if byID["high"] <= byID["low"] {
		t.Fatalf("priority term did not lift high above low: %v", byID)
	}
}

func TestCompositeZeroPriorityIsNeutral(t *testing.T) {
	tasks := []core.Task{task("a", core.StatusTodo), task("b", core.StatusTodo)}
	composite, err := NewComposite(DefaultWeights())
	if err != nil {
		t.Fatalf("NewComposite() error = %v", err)
	}
	scored, err := composite.Rank(context.Background(), request(t, tasks, ""))
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"a", "b"}) {
		t.Fatalf("Rank() = %v, want [a b]", order(scored))
	}
	byID := scoresByID(scored)
	if byID["a"] != 0 || byID["b"] != 0 {
		t.Fatalf("equal priorities should not change scores, got %v", byID)
	}
}

func TestCompositePriorityWeightLowersLowPriority(t *testing.T) {
	critical := task("critical", core.StatusTodo)
	chore := task("chore", core.StatusTodo)
	chore.Priority = -10
	blocked1 := task("blocked-1", core.StatusTodo, "chore")
	blocked2 := task("blocked-2", core.StatusTodo, "chore")
	tasks := []core.Task{critical, chore, blocked1, blocked2}

	composite, err := NewComposite(Weights{Unblock: 1, Milestone: 1, Toward: 1, Priority: 2})
	if err != nil {
		t.Fatalf("NewComposite() error = %v", err)
	}
	scored, err := composite.Rank(context.Background(), request(t, tasks, ""))
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"critical", "chore"}) {
		t.Fatalf("Rank() = %v, want [critical chore]", order(scored))
	}
	byID := scoresByID(scored)
	if byID["chore"] >= byID["critical"] {
		t.Fatalf("low priority was not measurably lowered: %v", byID)
	}
}

func TestUnblockBreaksTiesByPriorityThenID(t *testing.T) {
	high := task("b-high", core.StatusTodo)
	high.Priority = 5
	low := task("a-low", core.StatusTodo)
	low.Priority = 1
	tasks := []core.Task{low, high}

	scored, err := Unblock{}.Rank(context.Background(), request(t, tasks, ""))
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"b-high", "a-low"}) {
		t.Fatalf("Rank() = %v, want [b-high a-low]", order(scored))
	}
}

func TestNewCompositeRejectsNegativePriorityWeight(t *testing.T) {
	if _, err := NewComposite(Weights{Priority: -1}); err == nil {
		t.Fatal("NewComposite() error = nil, want error for negative priority weight")
	}
}

func TestEmptyCandidateSetYieldsNoResults(t *testing.T) {
	tasks := []core.Task{task("a", core.StatusTodo), task("b", core.StatusTodo)}
	composite, err := NewComposite(DefaultWeights())
	if err != nil {
		t.Fatalf("NewComposite() error = %v", err)
	}
	req := request(t, tasks, "")
	req.Candidates = []core.TaskID{}
	scored, err := composite.Rank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if len(scored) != 0 {
		t.Fatalf("Rank() = %v, want none for an explicit empty candidate set", order(scored))
	}
}

func TestRepoScopesCandidates(t *testing.T) {
	data := task("data-1", core.StatusTodo)
	data.Repo = "data"
	devops := task("devops-1", core.StatusTodo)
	devops.Repo = "devops"
	misc := task("misc-1", core.StatusTodo)

	repo := "data"
	req := request(t, []core.Task{data, devops, misc}, "")
	req.Repo = &repo

	scored, err := Unblock{}.Rank(context.Background(), req)
	if err != nil {
		t.Fatalf("Rank() error = %v", err)
	}
	if !equalOrder(order(scored), []core.TaskID{"data-1"}) {
		t.Fatalf("Rank() = %v, want [data-1]", order(scored))
	}
}

func TestBuiltinsRegisterAllRankers(t *testing.T) {
	names := Builtins().Names()
	want := []string{"composite", "milestone", "toward", "unblock"}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", names, want)
		}
	}
}

func TestDefaultRankerIsComposite(t *testing.T) {
	ranker, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	if ranker.Name() != "composite" {
		t.Fatalf("Default().Name() = %q, want composite", ranker.Name())
	}
}
