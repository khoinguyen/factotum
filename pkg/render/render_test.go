package render

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/graph"
	"github.com/khoinguyen/factotum/pkg/rank"
)

func task(id string, status core.TicketStatus, deps ...string) *core.Ticket {
	t := &core.Ticket{ID: core.TicketID(id), ProjectID: "prj-1", Kind: core.KindTask, Title: id, Status: status}
	for _, dep := range deps {
		t.Deps = append(t.Deps, core.TicketID(dep))
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
	a.Groomed = true
	a.AcceptanceCriteria = []string{"done"}
	a1 := task("a1", core.StatusTodo, "a")
	b := task("b", core.StatusTodo)
	b.AssigneeID = &humanID
	c := task("c", core.StatusTodo)
	d := task("d", core.StatusDone)

	tasks := []*core.Ticket{a, a1, b, c, d}
	values := make([]core.Ticket, 0, len(tasks))
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
	want := []string{"agent", "dot", "html", "json", "mermaid", "summary", "tree"}
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
	if !strings.Contains(out, "stats: scope=5 captures=0 resolved=1 done=1 ready_agent=1 ready_human=2 dep_blocked=1 blocked=0") {
		t.Fatalf("agent stats should distinguish captures/resolved/done and dep-blocked/blocked:\n%s", out)
	}
	if out != renderString(t, Agent{}, view) {
		t.Fatal("agent render is not deterministic")
	}
}

func ideaView(t *testing.T) View {
	t.Helper()
	policy := core.DefaultResolutionPolicy()
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: policy}
	idea := &core.Ticket{ID: "i-1", ProjectID: "prj-1", Kind: core.KindIdea, Title: "a spark", Status: core.StatusTodo}
	bug := &core.Ticket{ID: "b-1", ProjectID: "prj-1", Kind: core.KindBug, Title: "it crashes", Status: core.StatusTodo}
	a := task("a", core.StatusTodo)
	values := []core.Ticket{*idea, *bug, *a}
	built, err := graph.New(values, policy)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}
	return View{
		Project: project,
		Tasks:   []*core.Ticket{idea, bug, a},
		Graph:   built,
		Ready:   built.ReadyByActor(nil),
	}
}

func TestAgentRenderCaptureSection(t *testing.T) {
	view := ideaView(t)
	out := renderString(t, Agent{}, view)
	if !strings.Contains(out, "capture:") {
		t.Fatalf("agent output should have a capture section:\n%s", out)
	}
	for _, want := range []string{"capture:", "i-1", "a spark", "b-1", "it crashes", "status=todo"} {
		if !strings.Contains(out, want) {
			t.Fatalf("capture section missing %q:\n%s", want, out)
		}
	}
	// The shared line keeps the idea/bug distinction, so a text reader can tell
	// input kinds apart in both formats.
	for _, want := range []string{"kind=idea", "kind=bug"} {
		if !strings.Contains(out, want) {
			t.Fatalf("capture section missing %q:\n%s", want, out)
		}
	}
	// A capture appears only in its capture section: it is not ranked or blocked.
	for _, id := range []string{"i-1", "b-1"} {
		if n := strings.Count(out, id); n != 1 {
			t.Fatalf("capture %s should appear once (capture section), got %d:\n%s", id, n, out)
		}
	}
}

// promotedView is ideaView plus a task promoted from the idea, so a renderer can
// show how it treats the origin edge between an executable task and its capture.
func promotedView(t *testing.T) View {
	t.Helper()
	view := ideaView(t)
	promoted := task("p", core.StatusTodo, "i-1")
	view.Tasks = append(view.Tasks, promoted)
	values := make([]core.Ticket, 0, len(view.Tasks))
	for _, pointer := range view.Tasks {
		values = append(values, *pointer)
	}
	built, err := graph.New(values, view.Project.Policy)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}
	view.Graph = built
	view.Ready = built.ReadyByActor(nil)
	return view
}

// The default human text format carries the same capture facts as the agent
// format: captures are listed in their own section, not woven into the
// execution tree, and a task promoted from a capture stays in the tree.
func TestTreeRenderCaptureSection(t *testing.T) {
	view := promotedView(t)
	out := renderString(t, Tree{}, view)
	for _, want := range []string{"capture:", "i-1", "a spark", "b-1", "it crashes", "status=todo", "kind=idea", "kind=bug"} {
		if !strings.Contains(out, want) {
			t.Fatalf("tree output missing capture fact %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "[capture]") {
		t.Fatalf("captures belong in the capture section, not repeated in the tree body:\n%s", out)
	}
	if !strings.Contains(out, "- p [") {
		t.Fatalf("a task promoted from a capture must stay in the tree:\n%s", out)
	}
}

// A capture nested under a blocked task must not pull its dependents into that
// task's subtree: promotion makes ready work, not work gated by whatever the
// capture itself depends on.
func TestTreeRenderNestedCaptureDoesNotGateItsDependents(t *testing.T) {
	policy := core.DefaultResolutionPolicy()
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: policy}
	blocker := task("a", core.StatusInProgress)
	idea := &core.Ticket{
		ID: "c-1", ProjectID: "prj-1", Kind: core.KindIdea, Title: "a spark",
		Status: core.StatusTodo, Deps: []core.TicketID{"a"},
	}
	promoted := task("t-1", core.StatusTodo, "c-1")
	tasks := []*core.Ticket{blocker, idea, promoted}
	values := make([]core.Ticket, 0, len(tasks))
	for _, pointer := range tasks {
		values = append(values, *pointer)
	}
	built, err := graph.New(values, policy)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}
	out := renderString(t, Tree{}, viewFor(project, tasks, built))

	body := out[:strings.Index(out, "capture:")]
	if !strings.Contains(body, "- a [in-progress]") {
		t.Fatalf("blocker should render as a root:\n%s", out)
	}
	if !strings.Contains(body, "- t-1 [ready-human]") || strings.Contains(body, "  - t-1") {
		t.Fatalf("a promoted task must not nest under the capture's own blocker:\n%s", out)
	}
	if !strings.Contains(out, "capture:") || !strings.Contains(out, "c-1") {
		t.Fatalf("the capture should still be listed:\n%s", out)
	}
}

func viewFor(project *core.Project, tasks []*core.Ticket, built *graph.Graph) View {
	return View{
		Project: project,
		Tasks:   tasks,
		Graph:   built,
		Ready:   built.ReadyByActor(nil),
	}
}

// Captures are input, not execution, so they get their own count instead of
// inflating resolved or the status counts.
func TestCapturesHaveTheirOwnStats(t *testing.T) {
	view := ideaView(t)

	agent := renderString(t, Agent{}, view)
	for _, want := range []string{"captures=2", "resolved=0"} {
		if !strings.Contains(agent, want) {
			t.Fatalf("agent output missing %q:\n%s", want, agent)
		}
	}

	summary := renderString(t, Summary{}, view)
	if !strings.Contains(summary, "captures=2") {
		t.Fatalf("summary should count captures separately:\n%s", summary)
	}
	// The two captures are todo, but only the executable task counts as work.
	if !strings.Contains(summary, "todo=1") {
		t.Fatalf("summary should not count captures as todo work:\n%s", summary)
	}

	var doc struct {
		Stats map[string]int `json:"stats"`
	}
	if err := json.Unmarshal([]byte(renderString(t, JSON{}, view)), &doc); err != nil {
		t.Fatalf("json render invalid: %v", err)
	}
	if doc.Stats["captures"] != 2 || doc.Stats["resolved"] != 0 {
		t.Fatalf("json stats = %v, want captures=2 resolved=0", doc.Stats)
	}

	if html := renderString(t, HTML{}, view); !strings.Contains(html, "<dt>Captures</dt><dd>2</dd>") {
		t.Fatalf("html report should surface the capture count:\n%s", html)
	}
}

// A task whose only dependency is a capture in another project renders as
// startable work: the capture is non-blocking provenance, so the task joins the
// ready group and the graph reports no external dependency for it. A real
// foreign task keeps blocking.
func TestAgentRenderCrossProjectCaptureIsReady(t *testing.T) {
	policy := core.DefaultResolutionPolicy()
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: policy}
	onlyCapture := task("only", core.StatusTodo, "foreign-idea")
	withForeignTask := task("foreign", core.StatusTodo, "foreign-task")
	tasks := []*core.Ticket{onlyCapture, withForeignTask}
	values := []core.Ticket{*onlyCapture, *withForeignTask}
	built, err := graph.NewAtCaptures(values, policy, time.Time{}, []core.TicketID{"foreign-idea"})
	if err != nil {
		t.Fatalf("graph.NewAtCaptures() error = %v", err)
	}
	view := View{
		Project: project,
		Tasks:   tasks,
		Graph:   built,
		Ready:   built.ReadyByActor(nil),
	}
	out := renderString(t, Agent{}, view)
	if !strings.Contains(out, "next human:") {
		t.Fatalf("agent output missing next human group:\n%s", out)
	}
	human := out[strings.Index(out, "next human:"):strings.Index(out, "dep-blocked:")]
	if !strings.Contains(human, "only") {
		t.Fatalf("capture-dependent task should be ready:\n%s", out)
	}
	blocked := out[strings.Index(out, "dep-blocked:"):]
	if !strings.Contains(blocked, "foreign") {
		t.Fatalf("foreign-task-dependent task should stay blocked:\n%s", out)
	}
	if strings.Contains(out, "external_deps: [foreign-idea]") {
		t.Fatalf("non-blocking capture must not be an external dep:\n%s", out)
	}
	if !strings.Contains(out, "external_deps: [foreign-task]") {
		t.Fatalf("a real foreign task is still an external dep:\n%s", out)
	}
}

// depBlockedSection returns the agent render's dep-blocked body: everything
// between that header and the next top-level section.
func depBlockedSection(t *testing.T, out string) string {
	t.Helper()
	const header = "dep-blocked:\n"
	start := strings.Index(out, header)
	if start < 0 {
		t.Fatalf("agent output missing %q:\n%s", header, out)
	}
	section := out[start+len(header):]
	for _, next := range []string{"cycles:\n", "external_deps: "} {
		if end := strings.Index(section, next); end >= 0 {
			section = section[:end]
		}
	}
	return section
}

// depBlockedIDs lists the task ids in a dep-blocked section, which is one
// "  - <id> ..." line per task.
func depBlockedIDs(section string) []string {
	var ids []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		if fields := strings.Fields(strings.TrimPrefix(line, "- ")); len(fields) > 0 {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

// TestDepBlockedCountsOnlyUnmetDeps pins the reported bug: an in-progress task
// was reported as dep_blocked=1 while its blocked_by list was empty, because
// the stats counted every unresolved non-ready task. Dep-blocked means waiting
// on an unmet dependency, and an in-progress task is being worked on, not
// waiting, so the count and the list must agree with the board's
// classification.
func TestDepBlockedCountsOnlyUnmetDeps(t *testing.T) {
	tests := []struct {
		name     string
		tasks    []*core.Ticket
		want     int
		wantList []string
	}{
		{
			name:  "in progress with every dep done is not dep-blocked",
			tasks: []*core.Ticket{task("done", core.StatusDone), task("doing", core.StatusInProgress, "done")},
			want:  0,
		},
		{
			name:     "todo waiting on an unresolved dep is dep-blocked",
			tasks:    []*core.Ticket{task("blocker", core.StatusInProgress), task("waiting", core.StatusTodo, "blocker")},
			want:     1,
			wantList: []string{"waiting"},
		},
		{
			name:  "in progress with an unmet dep is still being worked on",
			tasks: []*core.Ticket{task("blocker", core.StatusTodo), task("doing", core.StatusInProgress, "blocker")},
			want:  0,
		},
		{
			name:  "blocked with no deps is blocked, not dep-blocked",
			tasks: []*core.Ticket{task("parked", core.StatusBlocked)},
			want:  0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			view := statusView(t, tc.tasks...)

			agent := renderString(t, Agent{}, view)
			if want := fmt.Sprintf("dep_blocked=%d", tc.want); !strings.Contains(agent, want) {
				t.Fatalf("agent stats missing %q:\n%s", want, agent)
			}
			section := depBlockedSection(t, agent)
			if got := depBlockedIDs(section); !slices.Equal(got, tc.wantList) {
				t.Fatalf("dep-blocked list = %v, want %v:\n%s", got, tc.wantList, agent)
			}
			// The reported symptom: a count with an empty blocked_by list.
			if ids := depBlockedIDs(section); len(ids) != tc.want {
				t.Fatalf("dep_blocked=%d but the list has %d entries:\n%s", tc.want, len(ids), agent)
			}
			if strings.Contains(section, "blocked_by=[]") {
				t.Fatalf("a dep-blocked entry must name its unmet dep:\n%s", agent)
			}

			summary := renderString(t, Summary{}, view)
			if want := fmt.Sprintf("dep_blocked=%d", tc.want); !strings.Contains(summary, want) {
				t.Fatalf("summary stats missing %q:\n%s", want, summary)
			}

			var doc struct {
				Stats struct {
					DepBlocked int `json:"depBlocked"`
				} `json:"stats"`
			}
			if err := json.Unmarshal([]byte(renderString(t, JSON{}, view)), &doc); err != nil {
				t.Fatalf("json render invalid: %v", err)
			}
			if doc.Stats.DepBlocked != tc.want {
				t.Fatalf("json depBlocked = %d, want %d", doc.Stats.DepBlocked, tc.want)
			}
		})
	}
}

func TestClassifyIdea(t *testing.T) {
	view := ideaView(t)
	idea, _ := view.Graph.Ticket("i-1")
	if got := view.Classify(idea); got != ClassCapture {
		t.Fatalf("Classify(idea) = %q, want %q", got, ClassCapture)
	}
}

func TestClassifyBug(t *testing.T) {
	view := ideaView(t)
	bug, _ := view.Graph.Ticket("b-1")
	if got := view.Classify(bug); got != ClassCapture {
		t.Fatalf("Classify(bug) = %q, want %q", got, ClassCapture)
	}
}

// statusView builds a view over exactly the given tasks, so a status-class test
// does not inherit the fixture's readiness mix.
func statusView(t *testing.T, tasks ...*core.Ticket) View {
	t.Helper()
	policy := core.DefaultResolutionPolicy()
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: policy}
	values := make([]core.Ticket, 0, len(tasks))
	for _, pointer := range tasks {
		values = append(values, *pointer)
	}
	built, err := graph.New(values, policy)
	if err != nil {
		t.Fatalf("graph.New() error = %v", err)
	}
	return viewFor(project, tasks, built)
}

// TestClassifyStatuses pins each status to its visual class, matching the web
// work board's classifier. In progress is the case this fixes: it used to fall
// through to "blocked".
func TestClassifyStatuses(t *testing.T) {
	tests := []struct {
		name   string
		status core.TicketStatus
		want   Class
	}{
		{"in progress", core.StatusInProgress, ClassInProgress},
		{"blocked", core.StatusBlocked, ClassBlocked},
		{"ready for review", core.StatusReadyForReview, ClassReview},
		{"done", core.StatusDone, ClassDone},
		{"cancelled", core.StatusCancelled, ClassCancelled},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			subject := task("subject", tc.status)
			view := statusView(t, subject)
			if got := view.Classify(*subject); got != tc.want {
				t.Fatalf("Classify(%s) = %q, want %q", tc.status, got, tc.want)
			}
		})
	}
}

// TestClassifyWaiting pins the remaining board parity gap: a todo task that is
// only waiting on an unmet dependency is "waiting" on the web board, not
// "blocked". A ready todo still gets a ready class, so the waiting case must not
// swallow ordinary ready work.
func TestClassifyWaiting(t *testing.T) {
	blocker := task("blocker", core.StatusInProgress)
	waiting := task("waiting", core.StatusTodo, "blocker")
	ready := task("ready", core.StatusTodo)
	view := statusView(t, blocker, waiting, ready)
	if got := view.Classify(*waiting); got != ClassWaiting {
		t.Fatalf("Classify(dep-blocked todo) = %q, want %q", got, ClassWaiting)
	}
	if got := view.Classify(*ready); got != ClassReadyAgent && got != ClassReadyHuman {
		t.Fatalf("Classify(ready todo) = %q, want a ready class", got)
	}
}

// TestWaitingClassRendersAcrossFormats proves the waiting class reaches every
// render format, not only the classifier: tree, json, and html all report what
// the web board shows for a dep-blocked todo.
func TestWaitingClassRendersAcrossFormats(t *testing.T) {
	blocker := task("blocker", core.StatusInProgress)
	waiting := task("waiting", core.StatusTodo, "blocker")
	view := statusView(t, blocker, waiting)

	if tree := renderString(t, Tree{}, view); !strings.Contains(tree, "waiting [waiting]") {
		t.Fatalf("tree render missing waiting class:\n%s", tree)
	}

	var doc struct {
		Tasks []struct {
			ID    string `json:"id"`
			Class string `json:"class"`
		} `json:"tasks"`
	}
	out := renderString(t, JSON{}, view)
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json render invalid: %v\n%s", err, out)
	}
	classes := map[string]string{}
	for _, entry := range doc.Tasks {
		classes[entry.ID] = entry.Class
	}
	if got := classes["waiting"]; got != "waiting" {
		t.Fatalf("json class for dep-blocked todo = %q, want %q\n%s", got, "waiting", out)
	}

	html := renderString(t, HTML{}, view)
	if !strings.Contains(html, `class="chip waiting"`) {
		t.Fatalf("html output missing waiting chip class:\n%s", html)
	}
	if !strings.Contains(html, ">waiting</span>") {
		t.Fatalf("html output missing 'waiting' chip label:\n%s", html)
	}
}

// TestJSONRenderLabelsInProgress is the reported bug: `ft graph render --format
// json` labelled an in-progress task "blocked".
func TestJSONRenderLabelsInProgress(t *testing.T) {
	doing := task("doing", core.StatusInProgress)
	parked := task("parked", core.StatusBlocked)
	view := statusView(t, doing, parked)
	out := renderString(t, JSON{}, view)
	var doc struct {
		Tasks []struct {
			ID    string `json:"id"`
			Class string `json:"class"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("json render invalid: %v\n%s", err, out)
	}
	classes := map[string]string{}
	for _, entry := range doc.Tasks {
		classes[entry.ID] = entry.Class
	}
	if got := classes["doing"]; got != "in-progress" {
		t.Fatalf("json class for in-progress task = %q, want %q\n%s", got, "in-progress", out)
	}
	if got := classes["parked"]; got != "blocked" {
		t.Fatalf("json class for blocked task = %q, want %q", got, "blocked")
	}
}

// TestHTMLChipInProgress proves the HTML view carries the new class and its
// human label, the way the board's "in progress" chip does.
func TestHTMLChipInProgress(t *testing.T) {
	doing := task("doing", core.StatusInProgress)
	view := statusView(t, doing)
	out := renderString(t, HTML{}, view)
	if !strings.Contains(out, `class="chip in-progress"`) {
		t.Fatalf("html output missing in-progress chip class:\n%s", out)
	}
	if !strings.Contains(out, ">in progress</span>") {
		t.Fatalf("html output missing 'in progress' chip label:\n%s", out)
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

func TestSummaryRenderIsCompact(t *testing.T) {
	view := fixture(t, "Acme")
	out := renderString(t, Summary{}, view)
	if strings.Contains(out, "snapshot") || strings.Contains(out, "2026-") {
		t.Fatalf("summary must not include a timestamp:\n%s", out)
	}
	for _, want := range []string{"scope=5", "captures=0", "todo=4", "done=1", "ready_agent=1", "ready_human=2", "dep_blocked=1", "next:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("summary missing %q:\n%s", want, out)
		}
	}
}

// scaleView builds the synthetic scale fixture used by the agent-render budget
// test and benchmark: n todo tasks where every tenth depends on its predecessor,
// matching the end-to-end harness. Almost every task is ready at once, so the
// agent renderer must derive the whole graph rather than a bounded neighborhood.
func scaleView(tb testing.TB, n int) View {
	tb.Helper()
	policy := core.DefaultResolutionPolicy()
	values := make([]core.Ticket, n)
	for i := range values {
		values[i] = core.Ticket{
			ID: core.TicketID(fmt.Sprintf("t-%06d", i)), ProjectID: "prj-1", Kind: core.KindTask,
			Title: fmt.Sprintf("task %d", i), Status: core.StatusTodo, Priority: i % 5,
		}
		if i > 0 && i%10 == 0 {
			values[i].Deps = []core.TicketID{values[i-1].ID}
		}
	}
	ptrs := make([]*core.Ticket, len(values))
	for i := range values {
		ptrs[i] = &values[i]
	}
	built, err := graph.New(values, policy)
	if err != nil {
		tb.Fatalf("graph.New() error = %v", err)
	}
	ranker, err := rank.Default()
	if err != nil {
		tb.Fatalf("rank.Default() error = %v", err)
	}
	return View{
		Project: &core.Project{ID: "prj-1", Name: "Scale", Policy: policy},
		Tasks:   ptrs,
		Graph:   built,
		Ready:   built.ReadyByActor(nil),
		Ranker:  ranker,
		Now:     time.Unix(0, 0).UTC(),
	}
}

// TestAgentRenderBudgetSmoke fails when the agent render path regresses past the
// heavy budget at 10k tasks. It mirrors pkg/app's budget smoke: a generous
// multiple of the @10k budget so an order-of-magnitude regression (such as the
// superlinear graph traversal this test was added for, t-lvlqecjqgm) fails while
// ordinary CI noise does not.
//
// The timed section measures process CPU time, not wall time. `mise run ci` fans
// out `test` and `cover` in parallel, and on a loaded machine that contention can
// inflate wall-clock render past the ceiling while the work itself is unchanged
// (t-vofx7ijled). CPU time is the invariant under load and still catches a
// constant-factor regression.
func TestAgentRenderBudgetSmoke(t *testing.T) {
	const (
		heavyBudget   = 250 * time.Millisecond
		smokeHeadroom = 5
	)
	view := scaleView(t, 10000)
	var renderErr error
	elapsed := measureCPU(func() {
		renderErr = (Agent{}).Render(context.Background(), io.Discard, view)
	})
	if renderErr != nil {
		t.Fatalf("Render() error = %v", renderErr)
	}
	ceiling := smokeHeadroom * heavyBudget
	if elapsed > ceiling {
		t.Fatalf("agent render at 10000 used %v of CPU, over the %v ceiling (budget %v)", elapsed, ceiling, heavyBudget)
	}
}

func BenchmarkAgentRender(b *testing.B) {
	for _, n := range []int{1000, 10000, 50000} {
		b.Run(fmt.Sprintf("%d", n), func(b *testing.B) {
			view := scaleView(b, n)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := (Agent{}).Render(context.Background(), io.Discard, view); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
