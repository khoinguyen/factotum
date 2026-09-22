// Package graph is the DAG engine: readiness, waves, cycles, canonical parents,
// and the primitives used by rankers and renderers. It depends only on pkg/core.
package graph

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

type Graph struct {
	policy     core.ResolutionPolicy
	tasks      map[core.TaskID]core.Task
	deps       map[core.TaskID][]core.TaskID
	dependents map[core.TaskID][]core.TaskID
	ids        []core.TaskID
	now        time.Time
}

// ReadyBucket classifies startable tasks by the kind of actor that should pick
// them up. Ready tasks with no assignee are treated as human work, because an
// unowned decision needs a person.
type ReadyBucket struct {
	Agent []core.TaskID
	Human []core.TaskID
}

// New builds a graph with no time reference, so not_before constraints are
// ignored. Use NewAt to evaluate readiness against a clock.
func New(tasks []core.Task, policy core.ResolutionPolicy) (*Graph, error) {
	return newGraph(tasks, policy, time.Time{})
}

// NewAt builds a graph that evaluates not_before constraints at now: a task
// whose not_before is in the future is excluded from the ready set while still
// blocking its dependents.
func NewAt(tasks []core.Task, policy core.ResolutionPolicy, now time.Time) (*Graph, error) {
	return newGraph(tasks, policy, now)
}

func newGraph(tasks []core.Task, policy core.ResolutionPolicy, now time.Time) (*Graph, error) {
	g := &Graph{
		policy:     policy,
		tasks:      make(map[core.TaskID]core.Task, len(tasks)),
		deps:       make(map[core.TaskID][]core.TaskID, len(tasks)),
		dependents: make(map[core.TaskID][]core.TaskID, len(tasks)),
		now:        now,
	}
	for _, t := range tasks {
		if _, ok := g.tasks[t.ID]; ok {
			return nil, fmt.Errorf("%w: duplicate task id %s", core.ErrConflict, t.ID)
		}
		g.tasks[t.ID] = t
		g.ids = append(g.ids, t.ID)
	}
	sort.Slice(g.ids, func(i, j int) bool { return g.ids[i] < g.ids[j] })

	for _, t := range tasks {
		deps := uniqueSorted(t.Deps)
		g.deps[t.ID] = deps
		for _, d := range deps {
			if _, ok := g.tasks[d]; !ok {
				continue
			}
			g.dependents[d] = append(g.dependents[d], t.ID)
		}
	}
	for _, ids := range g.dependents {
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	}

	return g, nil
}

func (g *Graph) Task(id core.TaskID) (core.Task, bool) {
	t, ok := g.tasks[id]
	return t, ok
}

func (g *Graph) IDs() []core.TaskID {
	return append([]core.TaskID(nil), g.ids...)
}

func (g *Graph) Deps(id core.TaskID) []core.TaskID {
	return append([]core.TaskID(nil), g.deps[id]...)
}

func (g *Graph) Dependents(id core.TaskID) []core.TaskID {
	return append([]core.TaskID(nil), g.dependents[id]...)
}

// ExternalDeps returns dependency IDs referenced by tasks in the graph but not
// present in it (out of scope or missing).
func (g *Graph) ExternalDeps() []core.TaskID {
	seen := make(map[core.TaskID]struct{})
	var out []core.TaskID
	for _, id := range g.ids {
		for _, d := range g.deps[id] {
			if _, ok := g.tasks[d]; ok {
				continue
			}
			if _, ok := seen[d]; ok {
				continue
			}
			seen[d] = struct{}{}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ReasonCode is the stable, machine-readable cause a task is excluded from the
// ready set.
type ReasonCode string

const (
	ReasonDepUnresolved ReasonCode = "dep_unresolved"
	ReasonBlocked       ReasonCode = "blocked"
	ReasonInProgress    ReasonCode = "in_progress"
	ReasonSnoozed       ReasonCode = "snoozed"
	ReasonNotBefore     ReasonCode = "not_before"
)

// NotReadyReason pairs a stable code with a human-readable detail.
type NotReadyReason struct {
	Code   ReasonCode
	Detail string
}

// Readiness reports whether a task is startable and, when it is not, the single
// reason it is excluded, chosen by a fixed precedence:
// dep_unresolved > blocked > in_progress > snoozed > not_before. A task that
// resolves is complete, not waiting, so it is not startable and carries no
// reason. ReadySet is derived from this, so the reason and the ready decision
// cannot disagree.
func (g *Graph) Readiness(id core.TaskID) (bool, *NotReadyReason) {
	t, ok := g.tasks[id]
	if !ok {
		return false, nil
	}
	if t.Resolves(g.policy) {
		return false, nil
	}
	if reason := g.notReadyReason(t); reason != nil {
		return false, reason
	}
	return true, nil
}

func (g *Graph) notReadyReason(t core.Task) *NotReadyReason {
	if unresolved := g.unresolvedDeps(t); len(unresolved) > 0 {
		return &NotReadyReason{Code: ReasonDepUnresolved, Detail: joinIDs(unresolved)}
	}
	switch t.Status {
	case core.StatusBlocked:
		return &NotReadyReason{Code: ReasonBlocked}
	case core.StatusInProgress:
		return &NotReadyReason{Code: ReasonInProgress}
	}
	if g.snoozeActive(t) {
		return &NotReadyReason{Code: ReasonSnoozed, Detail: t.Snooze.Describe()}
	}
	if !g.now.IsZero() && !t.ReadyAt(g.now) {
		return &NotReadyReason{Code: ReasonNotBefore, Detail: t.NotBefore.UTC().Format(time.RFC3339)}
	}
	return nil
}

// ReadySet returns the startable tasks: unresolved tasks whose known
// dependencies all resolve under the policy. Tasks that are blocked or already
// in progress are not startable, so they are excluded. A dependency that is
// missing from the graph blocks the task.
func (g *Graph) ReadySet() []core.TaskID {
	out := make([]core.TaskID, 0, len(g.ids))
	for _, id := range g.ids {
		if ready, _ := g.Readiness(id); ready {
			out = append(out, id)
		}
	}
	return out
}

// unresolvedDeps returns the dependency ids that keep a task out of the ready
// set: unknown to the graph, or not resolved under the policy.
func (g *Graph) unresolvedDeps(t core.Task) []core.TaskID {
	var out []core.TaskID
	for _, d := range g.deps[t.ID] {
		if dep, ok := g.tasks[d]; ok && dep.Resolves(g.policy) {
			continue
		}
		out = append(out, d)
	}
	return out
}

// snoozeActive reports whether a task is parked by a snooze whose condition has
// not passed. A date or task condition auto-clears once it is met; an
// indefinite snooze stays until explicitly removed.
func (g *Graph) snoozeActive(t core.Task) bool {
	snooze := t.Snooze
	if snooze == nil {
		return false
	}
	if snooze.Indefinite {
		return true
	}
	if snooze.Until != nil && (g.now.IsZero() || snooze.Until.After(g.now)) {
		return true
	}
	if snooze.UntilTask != nil {
		dep, ok := g.tasks[*snooze.UntilTask]
		if !ok || !dep.Resolves(g.policy) {
			return true
		}
	}
	return false
}

func joinIDs(ids []core.TaskID) string {
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, string(id))
	}
	return strings.Join(parts, ", ")
}

func (g *Graph) ReadyByActor(actors map[core.ActorID]core.Actor) ReadyBucket {
	var bucket ReadyBucket
	for _, id := range g.ReadySet() {
		t := g.tasks[id]
		isAgent := false
		if t.AssigneeID != nil {
			if a, ok := actors[*t.AssigneeID]; ok && a.Kind == core.ActorAgent {
				isAgent = true
			}
		}
		if isAgent {
			bucket.Agent = append(bucket.Agent, id)
		} else {
			bucket.Human = append(bucket.Human, id)
		}
	}
	return bucket
}

func (g *Graph) HasCycle() bool {
	return len(g.Cycles()) > 0
}

func (g *Graph) Cycles() [][]core.TaskID {
	const (
		white = iota
		gray
		black
	)

	color := make(map[core.TaskID]int, len(g.ids))
	var stack []core.TaskID
	var cycles [][]core.TaskID
	seen := make(map[string]struct{})

	var dfs func(core.TaskID)
	dfs = func(id core.TaskID) {
		color[id] = gray
		stack = append(stack, id)
		for _, next := range g.dependents[id] {
			switch color[next] {
			case gray:
				cycle := append([]core.TaskID(nil), stack[indexOf(stack, next):]...)
				key := cycleKey(cycle)
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					cycles = append(cycles, cycle)
				}
			case white:
				dfs(next)
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
	}

	for _, id := range g.ids {
		if color[id] == white {
			dfs(id)
		}
	}

	sort.Slice(cycles, func(i, j int) bool { return lessIDs(cycles[i], cycles[j]) })
	return cycles
}

// TopoSort returns a deterministic topological order of the graph's tasks,
// or core.ErrCycle when the graph contains a cycle.
func (g *Graph) TopoSort() ([]core.TaskID, error) {
	indeg := make(map[core.TaskID]int, len(g.ids))
	for _, id := range g.ids {
		for _, d := range g.deps[id] {
			if _, ok := g.tasks[d]; ok {
				indeg[id]++
			}
		}
	}

	queue := make([]core.TaskID, 0, len(g.ids))
	for _, id := range g.ids {
		if indeg[id] == 0 {
			queue = append(queue, id)
		}
	}

	out := make([]core.TaskID, 0, len(g.ids))
	for len(queue) > 0 {
		sort.Slice(queue, func(i, j int) bool { return queue[i] < queue[j] })
		id := queue[0]
		queue = queue[1:]
		out = append(out, id)
		for _, dep := range g.dependents[id] {
			indeg[dep]--
			if indeg[dep] == 0 {
				queue = append(queue, dep)
			}
		}
	}

	if len(out) != len(g.ids) {
		return nil, fmt.Errorf("%w: %d tasks unresolved", core.ErrCycle, len(g.ids)-len(out))
	}
	return out, nil
}

// Waves returns the unlock wave of every task: the longest chain of
// *unresolved* dependencies ending at that task. Resolved dependencies are
// transparent (they add no wave), so a task whose blockers are all resolved is
// wave 0 and therefore startable. This matches how a release/DAG report reads:
// "how many rounds of work until this can start".
func (g *Graph) Waves() (map[core.TaskID]int, error) {
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	waves := make(map[core.TaskID]int, len(order))
	for _, id := range order {
		w := 0
		for _, d := range g.deps[id] {
			dep, known := g.tasks[d]
			if known && dep.Resolves(g.policy) {
				continue
			}
			depWave := 0
			if known {
				depWave = waves[d]
			}
			if depWave+1 > w {
				w = depWave + 1
			}
		}
		waves[id] = w
	}
	return waves, nil
}

// structuralDepth is the longest path over all dependency edges ignoring
// status. It is used to pick canonical parents so the rendered tree keeps its
// structural shape even as tasks resolve.
func (g *Graph) structuralDepth() (map[core.TaskID]int, error) {
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}
	depth := make(map[core.TaskID]int, len(order))
	for _, id := range order {
		d := 0
		for _, dep := range g.deps[id] {
			if _, ok := g.tasks[dep]; !ok {
				continue
			}
			if depth[dep]+1 > d {
				d = depth[dep] + 1
			}
		}
		depth[id] = d
	}
	return depth, nil
}

// WavesDeep returns the number of waves (0 for an empty graph).
func (g *Graph) WavesDeep() (int, error) {
	waves, err := g.Waves()
	if err != nil {
		return 0, err
	}
	deep := 0
	for _, w := range waves {
		if w+1 > deep {
			deep = w + 1
		}
	}
	return deep, nil
}

// UnblockCount counts the direct dependents that would become startable if this
// task resolved.
func (g *Graph) UnblockCount(id core.TaskID) int {
	count := 0
	for _, dependent := range g.dependents[id] {
		dt, ok := g.tasks[dependent]
		if !ok || dt.Resolves(g.policy) {
			continue
		}
		blockedByOther := false
		for _, other := range g.deps[dependent] {
			if other == id {
				continue
			}
			ot, ok := g.tasks[other]
			if !ok || !ot.Resolves(g.policy) {
				blockedByOther = true
				break
			}
		}
		if !blockedByOther {
			count++
		}
	}
	return count
}

// TransitiveDependents returns every task reachable downstream of id, sorted.
func (g *Graph) TransitiveDependents(id core.TaskID) []core.TaskID {
	visited := map[core.TaskID]struct{}{}
	queue := []core.TaskID{id}
	var out []core.TaskID
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range g.dependents[cur] {
			if _, ok := visited[next]; ok {
				continue
			}
			visited[next] = struct{}{}
			out = append(out, next)
			queue = append(queue, next)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// DistanceToMilestone returns the number of edges from id to the nearest
// reachable milestone. A milestone has distance 0.
func (g *Graph) DistanceToMilestone(id core.TaskID) (int, bool) {
	t, ok := g.tasks[id]
	if !ok {
		return 0, false
	}
	if t.IsMilestone() {
		return 0, true
	}

	type node struct {
		id       core.TaskID
		distance int
	}

	visited := map[core.TaskID]struct{}{id: {}}
	queue := []node{{id: id}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range g.dependents[cur.id] {
			if _, ok := visited[next]; ok {
				continue
			}
			visited[next] = struct{}{}
			nt, ok := g.tasks[next]
			if !ok {
				continue
			}
			if nt.IsMilestone() {
				return cur.distance + 1, true
			}
			queue = append(queue, node{id: next, distance: cur.distance + 1})
		}
	}
	return 0, false
}

// ShortestPathTo returns a shortest path of dependent edges from `from` to `to`.
func (g *Graph) ShortestPathTo(from, to core.TaskID) ([]core.TaskID, bool) {
	if _, ok := g.tasks[from]; !ok {
		return nil, false
	}
	if _, ok := g.tasks[to]; !ok {
		return nil, false
	}
	if from == to {
		return []core.TaskID{from}, true
	}

	prev := make(map[core.TaskID]core.TaskID)
	visited := map[core.TaskID]struct{}{from: {}}
	queue := []core.TaskID{from}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, next := range g.dependents[cur] {
			if _, ok := visited[next]; ok {
				continue
			}
			visited[next] = struct{}{}
			prev[next] = cur
			if next == to {
				return reconstruct(prev, from, to), true
			}
			queue = append(queue, next)
		}
	}
	return nil, false
}

// CriticalPath returns the longest dependency chain in the graph.
func (g *Graph) CriticalPath() ([]core.TaskID, error) {
	order, err := g.TopoSort()
	if err != nil {
		return nil, err
	}

	dist := make(map[core.TaskID]int, len(order))
	prev := make(map[core.TaskID]core.TaskID)
	for _, id := range order {
		dist[id] = 1
		for _, d := range g.deps[id] {
			if _, ok := g.tasks[d]; !ok {
				continue
			}
			if dist[d]+1 > dist[id] {
				dist[id] = dist[d] + 1
				prev[id] = d
			}
		}
	}

	end := core.TaskID("")
	best := 0
	for _, id := range g.ids {
		if dist[id] > best {
			best = dist[id]
			end = id
		}
	}
	if end == "" {
		return nil, nil
	}
	return reconstruct(prev, "", end), nil
}

// Forest returns the canonical parent of every task that has one. The canonical
// parent is the dependency with the lowest structural depth, breaking ties by
// ID. Roots (tasks with no known dependency) are absent.
func (g *Graph) Forest() (map[core.TaskID]core.TaskID, error) {
	depth, err := g.structuralDepth()
	if err != nil {
		return nil, err
	}
	forest := make(map[core.TaskID]core.TaskID, len(g.ids))
	for _, id := range g.ids {
		best := core.TaskID("")
		bestDepth := 0
		for _, d := range g.deps[id] {
			if _, ok := g.tasks[d]; !ok {
				continue
			}
			w := depth[d]
			if best == "" || w < bestDepth || (w == bestDepth && d < best) {
				best = d
				bestDepth = w
			}
		}
		if best != "" {
			forest[id] = best
		}
	}
	return forest, nil
}

func reconstruct(prev map[core.TaskID]core.TaskID, from, to core.TaskID) []core.TaskID {
	var path []core.TaskID
	for cur := to; ; {
		path = append(path, cur)
		if cur == from {
			break
		}
		p, ok := prev[cur]
		if !ok {
			break
		}
		cur = p
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func uniqueSorted(ids []core.TaskID) []core.TaskID {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[core.TaskID]struct{}, len(ids))
	out := make([]core.TaskID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func indexOf(ids []core.TaskID, target core.TaskID) int {
	for i, id := range ids {
		if id == target {
			return i
		}
	}
	return -1
}

func cycleKey(cycle []core.TaskID) string {
	if len(cycle) == 0 {
		return ""
	}
	min := 0
	for i := 1; i < len(cycle); i++ {
		if cycle[i] < cycle[min] {
			min = i
		}
	}
	rotated := append(append([]core.TaskID(nil), cycle[min:]...), cycle[:min]...)
	key := ""
	for i, id := range rotated {
		if i > 0 {
			key += "->"
		}
		key += string(id)
	}
	return key
}

func lessIDs(a, b []core.TaskID) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}
