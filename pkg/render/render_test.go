package render

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
)

func task(id string, status core.TaskStatus, deps ...string) *core.Task {
	t := &core.Task{ID: core.TaskID(id), ProjectID: "prj-1", Kind: core.KindTask, Title: id, Status: status}
	for _, dep := range deps {
		t.Deps = append(t.Deps, core.TaskID(dep))
	}
	return t
}

func fixture(t *testing.T, projectName string) View {
	t.Helper()
	policy := core.DefaultResolutionPolicy()
	project := &core.Project{ID: "prj-1", Name: projectName, Policy: policy}

	agentID := core.ActorID("act-1")
	humanID := core.ActorID("act-2")

	a := task("a", core.StatusTodo)
	a.AssigneeID = &agentID
	a1 := task("a1", core.StatusTodo, "a")
	b := task("b", core.StatusTodo)
	b.AssigneeID = &humanID
	c := task("c", core.StatusTodo)
	d := task("d", core.StatusDone)

	tasks := []*core.Task{a, a1, b, c, d}
	values := make([]core.Task, 0, len(tasks))
	for _, pointer := range tasks {
		values = append(values, *pointer)
	}
	built, err := graph.New(values, policy)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}

	actors := map[core.ActorID]core.Actor{
		agentID: {ID: agentID, Kind: core.ActorAgent, Name: "claude"},
		humanID: {ID: humanID, Kind: core.ActorHuman, Name: "Khoi"},
	}

	return View{
		Project: project,
		Tasks:   tasks,
		Graph:   built,
		Actors:  actors,
		Ready:   built.ReadyByActor(actors),
		Events: []*core.Event{{
			ID:        "ev-1",
			ProjectID: project.ID,
			Kind:      core.EventProjectCreated,
			Summary:   "created project",
			CreatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		}},
		Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func renderString(t *testing.T, renderer Renderer, view View) string {
	t.Helper()
	var builder strings.Builder
	if err := renderer.Render(context.Background(), &builder, view); err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	return builder.String()
}

func TestBuiltinsNames(t *testing.T) {
	names := Builtins().Names()
	want := []string{"agent", "dot", "html", "json", "mermaid", "tree"}
	if len(names) != len(want) {
		t.Fatalf("Names() = %v, want %v", names, want)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", names, want)
		}
	}
}

func TestAgentRender(t *testing.T) {
	view := fixture(t, "Acme")
	out := renderString(t, Agent{}, view)
	for _, want := range []string{"Acme", "next agent:", "a ", "next human:", "b ", "dep-blocked:", "a1"} {
		if !strings.Contains(out, want) {
			t.Fatalf("agent output missing %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "stats: scope=5 resolved=1 done=1 ready_agent=1 ready_human=2 dep_blocked=1 blocked=0") {
		t.Fatalf("agent stats should distinguish resolved/done and dep-blocked/blocked:\n%s", out)
	}
	if out != renderString(t, Agent{}, view) {
		t.Fatal("agent render is not deterministic")
	}
}

func TestJSONRender(t *testing.T) {
	view := fixture(t, "Acme")
	out := renderString(t, JSON{}, view)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json render invalid: %v\n%s", err, out)
	}
	if doc["project"] != "Acme" {
		t.Fatalf("project = %v, want Acme", doc["project"])
	}
	if _, ok := doc["cycles"].([]any); !ok {
		t.Fatalf("cycles = %v (%T), want a JSON array", doc["cycles"], doc["cycles"])
	}
	if _, ok := doc["tasks"].([]any); !ok {
		t.Fatalf("tasks = %v (%T), want a JSON array", doc["tasks"], doc["tasks"])
	}
	if out != renderString(t, JSON{}, view) {
		t.Fatal("json render is not deterministic")
	}
}

func TestTreeRender(t *testing.T) {
	view := fixture(t, "Acme")
	out := renderString(t, Tree{}, view)
	for _, want := range []string{"a1", "b", "c"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree output missing %q:\n%s", want, out)
		}
	}
}

func TestGraphvizRender(t *testing.T) {
	view := fixture(t, "Acme")
	dot := renderString(t, DOT{}, view)
	if !strings.Contains(dot, `"a" -> "a1"`) {
		t.Fatalf("dot output missing edge:\n%s", dot)
	}
	mermaid := renderString(t, Mermaid{}, view)
	if !strings.Contains(mermaid, "a --> a1") {
		t.Fatalf("mermaid output missing edge:\n%s", mermaid)
	}
}

func TestHTMLRenderDeterministicAndEscapes(t *testing.T) {
	view := fixture(t, `Acme <script>alert(1)</script>`)
	out := renderString(t, HTML{}, view)
	if strings.Contains(out, "<script>alert(1)</script>") {
		t.Fatal("html render did not escape untrusted project name")
	}
	if !strings.Contains(out, "&lt;script&gt;") {
		t.Fatalf("html render missing escaped project name:\n%s", out)
	}
	for _, want := range []string{"Next up", "The graph", "What the graph flags", "Updates"} {
		if !strings.Contains(out, want) {
			t.Fatalf("html output missing section %q", want)
		}
	}
	if strings.Contains(out, `class="wave"`) {
		t.Fatal("tree HTML uses the colliding wave column class")
	}
	if !strings.Contains(out, "agent next") {
		t.Fatal("ready-agent chip should use the friendly label 'agent next'")
	}
	if !strings.Contains(out, `chip wave">w0`) {
		t.Fatalf("ready task should show a lowercase w0 wave chip\n%s", out)
	}
	if strings.Contains(out, ">W0<") {
		t.Fatal("wave chip must not be uppercased to W0")
	}

	doneStart := strings.Index(out, `<div class="node done"><span class="tkt">d</span>`)
	if doneStart < 0 {
		t.Fatalf("done node not found in output:\n%s", out)
	}
	if segment := out[doneStart : doneStart+220]; strings.Contains(segment, "chip wave") {
		t.Fatal("done task should not show a wave chip")
	}

	waves := view
	waves.Layout = "waves"
	wavesOut := renderString(t, HTML{}, waves)
	if !strings.Contains(wavesOut, `class="wave-col"`) {
		t.Fatal("waves layout should use the wave-col class")
	}
	if strings.Contains(wavesOut, `class="wave"`) {
		t.Fatal("waves layout uses the colliding wave class")
	}

	if out != renderString(t, HTML{}, view) {
		t.Fatal("html render is not deterministic")
	}
}
