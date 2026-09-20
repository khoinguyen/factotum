// Package rank provides the pluggable next-task rankers. A ranker scores the
// startable tasks; the composite ranker combines the built-in strategies.
package rank

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	pluginreg "github.com/khoinguyen/factotum/pkg/registry"
)

type Request struct {
	Graph      *graph.Graph
	Tasks      []*core.Task
	Candidates []core.TaskID
	Toward     core.TaskID
	Actor      *core.ActorID
	Repo       *string
}

type Scored struct {
	TaskID core.TaskID
	Score  float64
}

type Ranker interface {
	Name() string
	Rank(ctx context.Context, req Request) ([]Scored, error)
}

type Unblock struct{}

func (Unblock) Name() string { return "unblock" }

func (Unblock) Rank(_ context.Context, req Request) ([]Scored, error) {
	ids := candidates(req)
	out := make([]Scored, 0, len(ids))
	for _, id := range ids {
		out = append(out, Scored{TaskID: id, Score: float64(req.Graph.UnblockCount(id))})
	}
	return sortScored(out, req), nil
}

type Milestone struct{}

func (Milestone) Name() string { return "milestone" }

func (Milestone) Rank(_ context.Context, req Request) ([]Scored, error) {
	ids := candidates(req)
	out := make([]Scored, 0, len(ids))
	for _, id := range ids {
		score := 0.0
		if distance, ok := req.Graph.DistanceToMilestone(id); ok {
			score = 1 / float64(distance+1)
		}
		out = append(out, Scored{TaskID: id, Score: score})
	}
	return sortScored(out, req), nil
}

type Toward struct{}

func (Toward) Name() string { return "toward" }

func (Toward) Rank(_ context.Context, req Request) ([]Scored, error) {
	ids := candidates(req)
	out := make([]Scored, 0, len(ids))
	for _, id := range ids {
		score := 0.0
		if req.Toward != "" {
			if path, ok := req.Graph.ShortestPathTo(id, req.Toward); ok {
				score = 1 / float64(len(path))
			}
		}
		out = append(out, Scored{TaskID: id, Score: score})
	}
	return sortScored(out, req), nil
}

type Weights struct {
	Unblock   float64
	Milestone float64
	Toward    float64
	Priority  float64
}

func DefaultWeights() Weights {
	return Weights{Unblock: 1, Milestone: 1, Toward: 1, Priority: 1}
}

type Composite struct {
	weights Weights
}

func NewComposite(weights Weights) (*Composite, error) {
	if weights.Unblock < 0 || weights.Milestone < 0 || weights.Toward < 0 || weights.Priority < 0 {
		return nil, fmt.Errorf("%w: ranking weights must be non-negative", core.ErrInvalid)
	}
	return &Composite{weights: weights}, nil
}

func (c *Composite) Name() string { return "composite" }

func (c *Composite) Rank(_ context.Context, req Request) ([]Scored, error) {
	ids := candidates(req)

	priority := priorityByID(req.Tasks)
	rawUnblock := make(map[core.TaskID]float64, len(ids))
	rawMilestone := make(map[core.TaskID]float64, len(ids))
	rawToward := make(map[core.TaskID]float64, len(ids))
	maxUnblock := 0.0
	maxPriority := 0.0

	for _, id := range ids {
		rawUnblock[id] = float64(req.Graph.UnblockCount(id))
		if rawUnblock[id] > maxUnblock {
			maxUnblock = rawUnblock[id]
		}
		if abs := math.Abs(float64(priority[id])); abs > maxPriority {
			maxPriority = abs
		}
		if distance, ok := req.Graph.DistanceToMilestone(id); ok {
			rawMilestone[id] = 1 / float64(distance+1)
		}
		if req.Toward != "" {
			if path, ok := req.Graph.ShortestPathTo(id, req.Toward); ok {
				rawToward[id] = 1 / float64(len(path))
			}
		}
	}

	out := make([]Scored, 0, len(ids))
	for _, id := range ids {
		unblock := 0.0
		if maxUnblock > 0 {
			unblock = rawUnblock[id] / maxUnblock
		}
		priorityTerm := 0.0
		if maxPriority > 0 {
			priorityTerm = float64(priority[id]) / maxPriority
		}
		score := c.weights.Unblock*unblock + c.weights.Milestone*rawMilestone[id] + c.weights.Toward*rawToward[id] + c.weights.Priority*priorityTerm
		out = append(out, Scored{TaskID: id, Score: score})
	}
	return sortScored(out, req), nil
}

func candidates(req Request) []core.TaskID {
	ids := req.Candidates
	if len(ids) == 0 {
		ids = req.Graph.ReadySet()
	}
	if req.Actor == nil && req.Repo == nil {
		return ids
	}
	byID := make(map[core.TaskID]core.Task, len(req.Tasks))
	for _, task := range req.Tasks {
		byID[task.ID] = *task
	}
	out := make([]core.TaskID, 0, len(ids))
	for _, id := range ids {
		task, ok := byID[id]
		if !ok {
			continue
		}
		if req.Actor != nil && task.AssigneeID != nil && *task.AssigneeID != *req.Actor {
			continue
		}
		if req.Repo != nil && task.Repo != *req.Repo {
			continue
		}
		out = append(out, id)
	}
	return out
}

func priorityByID(tasks []*core.Task) map[core.TaskID]int {
	priority := make(map[core.TaskID]int, len(tasks))
	for _, task := range tasks {
		priority[task.ID] = task.Priority
	}
	return priority
}

func sortScored(out []Scored, req Request) []Scored {
	priority := priorityByID(req.Tasks)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		pi, pj := priority[out[i].TaskID], priority[out[j].TaskID]
		if pi != pj {
			return pi > pj
		}
		return out[i].TaskID < out[j].TaskID
	})
	return out
}

func Builtins() *pluginreg.Registry[Ranker] {
	reg := pluginreg.New[Ranker]()
	mustRegister(reg, Unblock{})
	mustRegister(reg, Milestone{})
	mustRegister(reg, Toward{})
	composite, err := NewComposite(DefaultWeights())
	if err != nil {
		panic(err)
	}
	mustRegister(reg, composite)
	return reg
}

func Default() (Ranker, error) {
	return Builtins().MustLookup("composite")
}

func mustRegister(reg *pluginreg.Registry[Ranker], ranker Ranker) {
	if err := reg.Register(ranker.Name(), ranker); err != nil {
		panic(err)
	}
}
