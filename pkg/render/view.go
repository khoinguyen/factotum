package render

import (
	"context"
	"sort"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/rank"
)

// View is the fully-derived state a renderer needs. It carries no storage or
// service dependency.
type View struct {
	Project *core.Project
	Tasks   []*core.Ticket
	Graph   *graph.Graph
	Actors  map[core.ActorID]core.Actor
	Ready   graph.ReadyBucket
	Events  []*core.Event
	Ranker  rank.Ranker
	Layout  string
	Theme   string
	Title   string
	Now     time.Time
}

// Stats are unambiguous graph counts: Captures counts human captures (ideas and
// bugs), which are input rather than work, so they are excluded from the
// execution counts below. Resolved counts executable tasks that satisfy the
// resolution policy (ready_for_review/done/cancelled); Done counts status=done;
// DepBlocked counts tasks waiting on unresolved dependencies; Blocked counts
// status=blocked.
type Stats struct {
	Scope      int
	Captures   int
	Resolved   int
	Done       int
	ReadyAgent int
	ReadyHuman int
	DepBlocked int
	Blocked    int
	Cycles     int
	Waves      int
}

// Class is the visual category of a task node.
type Class string

const (
	ClassDone       Class = "done"
	ClassCancelled  Class = "cancelled"
	ClassReview     Class = "review"
	ClassReadyAgent Class = "ready-agent"
	ClassReadyHuman Class = "ready-human"
	ClassInProgress Class = "in-progress"
	ClassBlocked    Class = "blocked"
	// ClassNeedsHuman is an unresolved task whose remaining blocker is a
	// human-next item: a ready task that needs a person. The human is the next
	// action, so the task names that work rather than reading as a generic
	// blocked or waiting task.
	ClassNeedsHuman Class = "needs-human"
	// ClassWaiting is an unresolved task that is neither ready nor explicitly
	// blocked, such as a todo still waiting on an unmet dependency. It mirrors
	// the web board's waiting column, which both surfaces must agree on.
	ClassWaiting Class = "waiting"
	ClassCycle   Class = "cycle"
	ClassCapture Class = "capture"
)

type info struct {
	project    *core.Project
	tasks      map[core.TicketID]core.Ticket
	ids        []core.TicketID
	readyAgent map[core.TicketID]bool
	readyHuman map[core.TicketID]bool
	cycles     map[core.TicketID]bool
	waves      map[core.TicketID]int
	deep       int
	forest     map[core.TicketID]core.TicketID
	stats      Stats
}

func (v View) info() *info {
	derived := &info{
		project:    v.Project,
		tasks:      make(map[core.TicketID]core.Ticket, len(v.Tasks)),
		readyAgent: make(map[core.TicketID]bool),
		readyHuman: make(map[core.TicketID]bool),
		cycles:     make(map[core.TicketID]bool),
		forest:     make(map[core.TicketID]core.TicketID),
	}
	for _, task := range v.Tasks {
		derived.tasks[task.ID] = *task
	}
	derived.ids = v.Graph.IDs()

	bucket := v.Ready
	if len(bucket.Agent) == 0 && len(bucket.Human) == 0 {
		bucket = v.Graph.ReadyByActor(v.Actors)
	}
	for _, id := range bucket.Agent {
		derived.readyAgent[id] = true
	}
	for _, id := range bucket.Human {
		derived.readyHuman[id] = true
	}

	for _, cycle := range v.Graph.Cycles() {
		for _, id := range cycle {
			derived.cycles[id] = true
		}
	}

	if waves, err := v.Graph.Waves(); err == nil {
		derived.waves = waves
	}
	if deep, err := v.Graph.WavesDeep(); err == nil {
		derived.deep = deep
	}
	if forest, err := v.Graph.Forest(); err == nil {
		derived.forest = forest
	}

	derived.stats = Stats{
		Scope:      len(derived.ids),
		ReadyAgent: len(bucket.Agent),
		ReadyHuman: len(bucket.Human),
		Cycles:     len(v.Graph.Cycles()),
		Waves:      derived.deep,
	}
	for _, id := range derived.ids {
		task := derived.tasks[id]
		// A capture is input, not execution: every capture resolves, so counting
		// it under resolved (or its status) would blur what the work counts mean.
		if task.Kind.CapturedByHuman() {
			derived.stats.Captures++
			continue
		}
		if task.Resolves(v.Project.Policy) {
			derived.stats.Resolved++
		}
		if task.Status == core.StatusDone {
			derived.stats.Done++
		}
		if task.Status == core.StatusBlocked {
			derived.stats.Blocked++
		}
		if v.depBlocked(derived, id) {
			derived.stats.DepBlocked++
		}
	}
	return derived
}

// depBlocked reports whether a task is waiting on at least one unmet
// dependency. Resolved, ready, and cycle tasks are not waiting, and neither is
// an in-progress task: it is being worked on, so the board classifies it
// in-progress even when a dependency is unmet. Requiring an actual blocker
// keeps the dep_blocked count and the dep-blocked list from disagreeing.
func (v View) depBlocked(derived *info, id core.TicketID) bool {
	task := derived.tasks[id]
	if derived.cycles[id] || task.Status == core.StatusInProgress {
		return false
	}
	if task.Resolves(v.Project.Policy) || derived.readyAgent[id] || derived.readyHuman[id] {
		return false
	}
	return len(v.blockers(id)) > 0
}

// humanBlockers names the ready-human tasks a task waits on: the dependencies
// that are themselves ready human work, so completing one is what unblocks it.
func (v View) humanBlockers(derived *info, id core.TicketID) []core.TicketID {
	return v.Graph.HumanBlockers(id, derived.readyHuman)
}

func (v View) Classify(task core.Ticket) Class {
	derived := v.info()
	switch {
	case derived.cycles[task.ID]:
		return ClassCycle
	case task.Kind.CapturedByHuman():
		return ClassCapture
	case task.Status == core.StatusCancelled:
		return ClassCancelled
	case task.Status == core.StatusDone:
		return ClassDone
	case task.Status == core.StatusReadyForReview:
		return ClassReview
	case task.Status == core.StatusInProgress:
		return ClassInProgress
	case derived.readyAgent[task.ID]:
		return ClassReadyAgent
	case derived.readyHuman[task.ID]:
		return ClassReadyHuman
	case len(v.humanBlockers(derived, task.ID)) > 0:
		return ClassNeedsHuman
	case task.Status == core.StatusBlocked:
		return ClassBlocked
	default:
		return ClassWaiting
	}
}

func (v View) projectName() string {
	if v.Project == nil {
		return "project"
	}
	return v.Project.Name
}

func (v View) title() string {
	if v.Title != "" {
		return v.Title
	}
	return v.projectName() + " task graph"
}

func (v View) resolved(task core.Ticket) bool {
	return task.Resolves(v.Project.Policy)
}

func (v View) ranker() rank.Ranker {
	if v.Ranker != nil {
		return v.Ranker
	}
	ranker, err := rank.Default()
	if err != nil {
		return rank.Unblock{}
	}
	return ranker
}

func (v View) ranked() []rank.Scored {
	derived := v.info()
	scored, err := v.ranker().Rank(context.Background(), rank.Request{
		Graph: v.Graph,
		Tasks: v.Tasks,
	})
	if err != nil {
		return nil
	}
	out := make([]rank.Scored, 0, len(scored))
	for _, s := range scored {
		if derived.readyAgent[s.TicketID] || derived.readyHuman[s.TicketID] {
			out = append(out, s)
		}
	}
	return out
}

// ByWave groups task IDs by unlock wave, ordered by wave then ID.
type WaveGroup struct {
	Wave int
	IDs  []core.TicketID
}

func (v View) ByWave() []WaveGroup {
	derived := v.info()
	waves := map[int][]core.TicketID{}
	for _, id := range derived.ids {
		wave := derived.waves[id]
		waves[wave] = append(waves[wave], id)
	}
	keys := make([]int, 0, len(waves))
	for wave := range waves {
		keys = append(keys, wave)
	}
	sort.Ints(keys)
	out := make([]WaveGroup, 0, len(keys))
	for _, wave := range keys {
		ids := waves[wave]
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		out = append(out, WaveGroup{Wave: wave, IDs: ids})
	}
	return out
}
