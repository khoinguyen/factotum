package render

import (
	"bytes"
	"context"
	"embed"
	"html/template"
	"io"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
)

//go:embed html/template.html
var templateFS embed.FS

var htmlTemplate = template.Must(template.ParseFS(templateFS, "html/template.html"))

type HTML struct{}

func (HTML) Format() string { return "html" }

func (HTML) Render(_ context.Context, w io.Writer, view View) error {
	var buf bytes.Buffer
	if err := htmlTemplate.Execute(&buf, view.htmlData()); err != nil {
		return err
	}
	return writeString(w, buf.String())
}

type htmlNext struct {
	ID       string
	Title    string
	Assignee string
	Score    float64
	Unblocks int
	Wave     int
}

type htmlNode struct {
	ID        string
	Title     string
	Repo      string
	Class     string
	ChipLabel string
	ShowWave  bool
	Milestone bool
	Assignee  string
	Wave      int
	Refs      []htmlNode
	Children  []*htmlNode
}

type htmlWave struct {
	Wave  int
	Nodes []*htmlNode
}

type htmlFlag struct {
	OK   bool
	Text string
}

type htmlUpdate struct {
	Time    string
	Summary string
}

type htmlData struct {
	Title     string
	Project   string
	Snapshot  string
	Theme     string
	Layout    string
	Stats     Stats
	NextAgent []htmlNext
	NextHuman []htmlNext
	TreeRoots []*htmlNode
	Waves     []htmlWave
	Flags     []htmlFlag
	Updates   []htmlUpdate
}

func (v View) htmlData() *htmlData {
	derived := v.info()
	data := &htmlData{
		Title:    v.title(),
		Project:  v.projectName(),
		Snapshot: v.snapshotTime(),
		Theme:    v.Theme,
		Layout:   v.Layout,
		Stats:    derived.stats,
	}
	if data.Layout == "" {
		data.Layout = "tree"
	}

	for _, scored := range v.ranked() {
		task := derived.tasks[scored.TaskID]
		entry := htmlNext{
			ID:       string(scored.TaskID),
			Title:    task.Title,
			Assignee: v.actorName(task.AssigneeID),
			Score:    scored.Score,
			Unblocks: v.Graph.UnblockCount(scored.TaskID),
			Wave:     derived.waves[scored.TaskID],
		}
		if derived.readyAgent[scored.TaskID] {
			data.NextAgent = append(data.NextAgent, entry)
		}
		if derived.readyHuman[scored.TaskID] {
			data.NextHuman = append(data.NextHuman, entry)
		}
	}

	model := v.BuildTree()
	for _, root := range model.Roots {
		data.TreeRoots = append(data.TreeRoots, v.htmlNode(derived, root))
	}
	for _, group := range v.ByWave() {
		wave := htmlWave{Wave: group.Wave}
		for _, id := range group.IDs {
			task := derived.tasks[id]
			class := v.Classify(task)
			wave.Nodes = append(wave.Nodes, &htmlNode{
				ID:        string(id),
				Title:     task.Title,
				Repo:      task.Repo,
				Class:     string(class),
				ChipLabel: chipLabel(class),
				ShowWave:  !v.resolved(task),
				Milestone: task.IsMilestone(),
				Assignee:  v.actorName(task.AssigneeID),
				Wave:      derived.waves[id],
			})
		}
		data.Waves = append(data.Waves, wave)
	}

	if cycles := v.Graph.Cycles(); len(cycles) > 0 {
		for _, cycle := range cycles {
			data.Flags = append(data.Flags, htmlFlag{Text: "cycle: " + strings.Join(idStrings(cycle), " -> ")})
		}
	} else {
		data.Flags = append(data.Flags, htmlFlag{OK: true, Text: "No dependency cycles."})
	}
	if external := v.Graph.ExternalDeps(); len(external) > 0 {
		data.Flags = append(data.Flags, htmlFlag{Text: "external dependencies: " + strings.Join(idStrings(external), ", ")})
	}

	for _, event := range v.Events {
		data.Updates = append(data.Updates, htmlUpdate{
			Time:    event.CreatedAt.UTC().Format(time.RFC3339),
			Summary: event.Summary,
		})
	}

	return data
}

func (v View) htmlNode(derived *info, node *TreeNode) *htmlNode {
	class := v.Classify(node.Task)
	out := &htmlNode{
		ID:        string(node.Task.ID),
		Title:     node.Task.Title,
		Repo:      node.Task.Repo,
		Class:     string(class),
		ChipLabel: chipLabel(class),
		ShowWave:  !v.resolved(node.Task),
		Milestone: node.Task.IsMilestone(),
		Assignee:  v.actorName(node.Task.AssigneeID),
		Wave:      derived.waves[node.Task.ID],
	}
	for _, ref := range node.Refs {
		refTask := derived.tasks[ref]
		refClass := v.Classify(refTask)
		out.Refs = append(out.Refs, htmlNode{
			ID:        string(ref),
			Title:     refTask.Title,
			Class:     string(refClass),
			ChipLabel: chipLabel(refClass),
			Milestone: refTask.IsMilestone(),
		})
	}
	for _, child := range node.Children {
		out.Children = append(out.Children, v.htmlNode(derived, child))
	}
	return out
}

func chipLabel(class Class) string {
	switch class {
	case ClassDone:
		return "done"
	case ClassCancelled:
		return "cancelled"
	case ClassReview:
		return "in review"
	case ClassReadyAgent:
		return "agent next"
	case ClassReadyHuman:
		return "human next"
	case ClassBlocked:
		return "blocked"
	case ClassCycle:
		return "cycle"
	default:
		return string(class)
	}
}

func idStrings(ids []core.TaskID) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}
