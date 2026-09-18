package render

import (
	"context"
	"encoding/json"
	"io"

	"github.com/khoinguyen/factotum/pkg/core"
)

type JSON struct{}

func (JSON) Format() string { return "json" }

type jsonStats struct {
	Scope      int `json:"scope"`
	Done       int `json:"done"`
	ReadyAgent int `json:"readyAgent"`
	ReadyHuman int `json:"readyHuman"`
	Blocked    int `json:"blocked"`
	Cycles     int `json:"cycles"`
	Waves      int `json:"waves"`
}

type jsonReadyTask struct {
	ID       string  `json:"id"`
	Repo     string  `json:"repo,omitempty"`
	Title    string  `json:"title"`
	Kind     string  `json:"kind"`
	Status   string  `json:"status"`
	Score    float64 `json:"score"`
	Unblocks int     `json:"unblocks"`
	Wave     int     `json:"wave"`
	Assignee string  `json:"assignee,omitempty"`
}

type jsonTask struct {
	ID        string   `json:"id"`
	Repo      string   `json:"repo,omitempty"`
	Kind      string   `json:"kind"`
	Title     string   `json:"title"`
	Status    string   `json:"status"`
	Class     string   `json:"class"`
	Wave      int      `json:"wave"`
	Deps      []string `json:"deps"`
	BlockedBy []string `json:"blockedBy,omitempty"`
	Assignee  string   `json:"assignee,omitempty"`
	WaitingOn []string `json:"waitingOn,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Priority  int      `json:"priority"`
}

type jsonNext struct {
	Agent []jsonReadyTask `json:"agent"`
	Human []jsonReadyTask `json:"human"`
}

type jsonDoc struct {
	Project      string     `json:"project"`
	Snapshot     string     `json:"snapshot"`
	Stats        jsonStats  `json:"stats"`
	Next         jsonNext   `json:"next"`
	Tasks        []jsonTask `json:"tasks"`
	Cycles       [][]string `json:"cycles"`
	ExternalDeps []string   `json:"externalDeps,omitempty"`
}

func (JSON) Render(_ context.Context, w io.Writer, view View) error {
	derived := view.info()
	doc := jsonDoc{
		Project:      view.projectName(),
		Snapshot:     view.snapshotTime(),
		Next:         jsonNext{Agent: []jsonReadyTask{}, Human: []jsonReadyTask{}},
		Tasks:        []jsonTask{},
		Cycles:       [][]string{},
		ExternalDeps: []string{},
		Stats: jsonStats{
			Scope:      derived.stats.Scope,
			Done:       derived.stats.Done,
			ReadyAgent: derived.stats.ReadyAgent,
			ReadyHuman: derived.stats.ReadyHuman,
			Blocked:    derived.stats.Blocked,
			Cycles:     derived.stats.Cycles,
			Waves:      derived.stats.Waves,
		},
	}

	for _, scored := range view.ranked() {
		task := derived.tasks[scored.TaskID]
		entry := jsonReadyTask{
			ID:       string(scored.TaskID),
			Repo:     task.Repo,
			Title:    task.Title,
			Kind:     string(task.Kind),
			Status:   string(task.Status),
			Score:    scored.Score,
			Unblocks: view.Graph.UnblockCount(scored.TaskID),
			Wave:     derived.waves[scored.TaskID],
			Assignee: view.actorName(task.AssigneeID),
		}
		if derived.readyAgent[scored.TaskID] {
			doc.Next.Agent = append(doc.Next.Agent, entry)
		}
		if derived.readyHuman[scored.TaskID] {
			doc.Next.Human = append(doc.Next.Human, entry)
		}
	}

	for _, id := range derived.ids {
		task := derived.tasks[id]
		entry := jsonTask{
			ID:        string(id),
			Repo:      task.Repo,
			Kind:      string(task.Kind),
			Title:     task.Title,
			Status:    string(task.Status),
			Class:     string(view.Classify(task)),
			Wave:      derived.waves[id],
			Deps:      view.stringDeps(id),
			BlockedBy: view.blockers(id),
			Assignee:  view.actorName(task.AssigneeID),
			Labels:    task.Labels,
			Priority:  task.Priority,
		}
		for _, actor := range task.WaitingOn {
			entry.WaitingOn = append(entry.WaitingOn, string(actor))
		}
		doc.Tasks = append(doc.Tasks, entry)
	}

	for _, cycle := range view.Graph.Cycles() {
		ids := make([]string, 0, len(cycle))
		for _, id := range cycle {
			ids = append(ids, string(id))
		}
		doc.Cycles = append(doc.Cycles, ids)
	}
	for _, id := range view.Graph.ExternalDeps() {
		doc.ExternalDeps = append(doc.ExternalDeps, string(id))
	}

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeString(w, string(data))
}

func (v View) actorName(id *core.ActorID) string {
	if id == nil {
		return ""
	}
	if actor, ok := v.Actors[*id]; ok {
		return actor.Name
	}
	return string(*id)
}

func (v View) stringDeps(id core.TaskID) []string {
	deps := v.Graph.Deps(id)
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		out = append(out, string(dep))
	}
	return out
}
