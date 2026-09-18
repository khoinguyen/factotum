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
	Tasks   []*core.Task
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

type Stats struct {
	Scope      int
	Done       int
	ReadyAgent int
	ReadyHuman int
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
	ClassBlocked    Class = "blocked"
	ClassCycle      Class = "cycle"
)

type info struct {
	project    *core.Project
	tasks      map[core.TaskID]core.Task
	ids        []core.TaskID
	readyAgent map[core.TaskID]bool
	readyHuman map[core.TaskID]bool
	cycles     map[core.TaskID]bool
	waves      map[core.TaskID]int
	deep       int
	forest     map[core.TaskID]core.TaskID
	stats      Stats
}

func (v View) info() *info {
	derived := &info{
		project:    v.Project,
		tasks:      make(map[core.TaskID]core.Task, len(v.Tasks)),
		readyAgent: make(map[core.TaskID]bool),
		readyHuman: make(map[core.TaskID]bool),
		cycles:     make(map[core.TaskID]bool),
		forest:     make(map[core.TaskID]core.TaskID),
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
		switch {
		case task.Resolves(v.Project.Policy):
			derived.stats.Done++
		case derived.readyAgent[id] || derived.readyHuman[id]:
		case derived.cycles[id]:
		default:
			derived.stats.Blocked++
		}
	}
	return derived
}

func (v View) Classify(task core.Task) Class {
	derived := v.info()
	switch {
	case derived.cycles[task.ID]:
		return ClassCycle
	case task.Status == core.StatusCancelled:
		return ClassCancelled
	case task.Status == core.StatusDone:
		return ClassDone
	case task.Status == core.StatusReadyForReview:
		return ClassReview
	case derived.readyAgent[task.ID]:
		return ClassReadyAgent
	case derived.readyHuman[task.ID]:
		return ClassReadyHuman
	default:
		return ClassBlocked
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

func (v View) resolved(task core.Task) bool {
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
		if derived.readyAgent[s.TaskID] || derived.readyHuman[s.TaskID] {
			out = append(out, s)
		}
	}
	return out
}

// ByWave groups task IDs by unlock wave, ordered by wave then ID.
type WaveGroup struct {
	Wave int
	IDs  []core.TaskID
}

func (v View) ByWave() []WaveGroup {
	derived := v.info()
	waves := map[int][]core.TaskID{}
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
