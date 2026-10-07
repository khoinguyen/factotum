package jsondir

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/core"
)

func TestTaskDocumentRoundTrip(t *testing.T) {
	notBefore := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Hour)
	assignee := core.ActorID("act-1")
	task := core.Task{
		ID:          "t-1",
		ProjectID:   "prj-1",
		Repo:        "backend",
		Kind:        core.KindTask,
		Title:       "one",
		Description: "line one\n\nline two",
		Status:      core.StatusInProgress,
		AssigneeID:  &assignee,
		WaitingOn:   []core.ActorID{"act-2"},
		Labels:      []string{"groomed", "urgent"},
		Priority:    3,
		Deps:        []core.TaskID{"t-0"},
		Notes: []core.Note{
			{ID: "note-1", Author: "act-2", Body: "a note\nwith two lines", CreatedAt: notBefore, System: true},
			{ID: "note-2", Body: "human note", Links: []core.Link{{Kind: core.LinkPR, URL: "https://example.test/pr/1", Title: "PR"}}},
		},
		Milestone:          &core.MilestoneMeta{TargetDate: &notBefore, ReleaseRef: "v1"},
		NotBefore:          &notBefore,
		Snooze:             &core.Snooze{Indefinite: true},
		Groomed:            true,
		AcceptanceCriteria: []string{"works", "is documented"},
		CreatedAt:          createdAt,
		UpdatedAt:          updatedAt,
	}

	data, err := encodeTask(task, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	got, extra, err := decodeTask(data)
	if err != nil {
		t.Fatalf("decodeTask() error = %v\n%s", err, data)
	}
	if len(extra) != 0 {
		t.Fatalf("decodeTask() extra = %v, want none", extra)
	}
	if !reflect.DeepEqual(got, task) {
		t.Fatalf("round trip mismatch:\n got  = %#v\n want = %#v", got, task)
	}
}

func TestTaskDocumentCanonicalForm(t *testing.T) {
	task := core.Task{
		ID:        "t-1",
		ProjectID: "prj-1",
		Kind:      core.KindTask,
		Title:     "one",
		Status:    core.StatusTodo,
		CreatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
	}
	first, err := encodeTask(task, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	second, err := encodeTask(task, nil)
	if err != nil {
		t.Fatalf("encodeTask() second call error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("encode is not deterministic:\n%s\n---\n%s", first, second)
	}
	text := string(first)
	if !strings.HasPrefix(text, "---\n") {
		t.Fatalf("document does not open with frontmatter: %q", text)
	}
	if strings.Contains(text, "\r") {
		t.Fatalf("document contains CR: %q", text)
	}
	if strings.HasSuffix(text, "\n\n") {
		t.Fatalf("document does not end with a single newline: %q", text)
	}
	if !strings.HasSuffix(text, "\n") {
		t.Fatalf("document does not end with a newline: %q", text)
	}
	// Fixed key order: the identity keys appear in a stable, documented order.
	order := []string{"id:", "kind:", "title:", "status:", "project:", "created_at:", "updated_at:"}
	last := -1
	for _, key := range order {
		idx := strings.Index(text, "\n"+key)
		if idx < 0 {
			t.Fatalf("document missing %q:\n%s", key, text)
		}
		if idx < last {
			t.Fatalf("key %q out of canonical order:\n%s", key, text)
		}
		last = idx
	}
}

func TestTaskDocumentPreservesUnknownKeys(t *testing.T) {
	input := "---\n" +
		"id: t-1\n" +
		"kind: task\n" +
		"title: one\n" +
		"status: todo\n" +
		"project: prj-1\n" +
		"origin: human\n" +
		"custom:\n" +
		"  nested: 1\n" +
		"---\n" +
		"\n" +
		"the description\n"

	task, extra, err := decodeTask([]byte(input))
	if err != nil {
		t.Fatalf("decodeTask() error = %v", err)
	}
	if task.Title != "one" || task.Description != "the description" {
		t.Fatalf("decodeTask() = %#v, want title one and the body", task)
	}
	if len(extra) != 2 {
		t.Fatalf("extra keys = %v, want origin and custom", extra)
	}

	task.Title = "renamed"
	out, err := encodeTask(task, extra)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "origin: human") {
		t.Fatalf("unknown key origin dropped:\n%s", text)
	}
	if !strings.Contains(text, "custom:\n  nested: 1") {
		t.Fatalf("unknown nested key not preserved (indent 2):\n%s", text)
	}
	if !strings.Contains(text, "title: renamed") {
		t.Fatalf("re-encode did not carry the update:\n%s", text)
	}
}

func TestTaskDocumentUnknownKeysDoNotDuplicateKnown(t *testing.T) {
	// A key already belonging to the schema must never be emitted twice, even if
	// an extra node with the same name was captured.
	extra := map[string]*yaml.Node{
		"title": {Kind: yaml.ScalarNode, Tag: "!!str", Value: "sneaky"},
	}
	out, err := encodeTask(core.Task{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo}, extra)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	if strings.Count(string(out), "title:") != 1 {
		t.Fatalf("title emitted more than once:\n%s", out)
	}
}

func TestTaskDocumentSchemaBreak(t *testing.T) {
	cases := map[string]string{
		"unknown status":     "---\nid: t-1\nkind: task\ntitle: one\nstatus: bogus\nproject: prj-1\n---\n",
		"missing title":      "---\nid: t-1\nkind: task\nstatus: todo\nproject: prj-1\n---\n",
		"wrong field type":   "---\nid: t-1\nkind: task\ntitle: one\nstatus: todo\nproject: prj-1\npriority: high\n---\n",
		"malformed yaml":     "---\nid: [\n---\n",
		"no frontmatter":     "just a body\n",
		"unterminated front": "---\nid: t-1\n",
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := decodeTask([]byte(input)); err == nil {
				t.Fatalf("decodeTask() error = nil, want a schema error for:\n%s", input)
			}
		})
	}
}

func TestNoteBodyMayContainHeadings(t *testing.T) {
	task := core.Task{
		ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo,
		Notes: []core.Note{{ID: "note-1", Body: "### not a note heading\nstill body"}},
	}
	data, err := encodeTask(task, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	got, _, err := decodeTask(data)
	if err != nil {
		t.Fatalf("decodeTask() error = %v\n%s", err, data)
	}
	if !reflect.DeepEqual(got.Notes, task.Notes) {
		t.Fatalf("notes = %#v, want %#v", got.Notes, task.Notes)
	}
}

func TestDescriptionMayContainDelimiterLines(t *testing.T) {
	description := "before\n<!-- ft:notes -->\n<!-- ft:note: {\"id\":\"fake\"} -->\nafter"
	task := core.Task{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo, Description: description}
	data, err := encodeTask(task, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	got, _, err := decodeTask(data)
	if err != nil {
		t.Fatalf("decodeTask() error = %v\n%s", err, data)
	}
	if got.Description != description {
		t.Fatalf("Description = %q, want %q", got.Description, description)
	}
	if len(got.Notes) != 0 {
		t.Fatalf("Notes = %#v, want none", got.Notes)
	}
}

func TestNoteBodyMayContainDelimiterLines(t *testing.T) {
	body := "<!-- ft:note: {\"id\":\"fake\"} -->\nand\n<!-- ft:notes -->\ntail"
	task := core.Task{
		ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo,
		Notes: []core.Note{{ID: "note-1", Body: body}},
	}
	data, err := encodeTask(task, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	got, _, err := decodeTask(data)
	if err != nil {
		t.Fatalf("decodeTask() error = %v\n%s", err, data)
	}
	if !reflect.DeepEqual(got.Notes, task.Notes) {
		t.Fatalf("notes = %#v, want %#v", got.Notes, task.Notes)
	}
}

func TestBodyMayContainBackslashLines(t *testing.T) {
	description := `\a literal backslash line`
	task := core.Task{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "one", Status: core.StatusTodo, Description: description}
	data, err := encodeTask(task, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	got, _, err := decodeTask(data)
	if err != nil {
		t.Fatalf("decodeTask() error = %v\n%s", err, data)
	}
	if got.Description != description {
		t.Fatalf("Description = %q, want %q", got.Description, description)
	}
}

func TestProjectDocumentRoundTrip(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	project := core.Project{
		ID:          "prj-1",
		Name:        "Acme",
		Description: "company work",
		Repos:       []core.Repository{{Name: "backend", URL: "github:acme/backend", Path: "/src/backend", Description: "api"}},
		Policy:      core.DefaultResolutionPolicy(),
		CreatedAt:   created,
		UpdatedAt:   created,
	}
	data, err := encodeProject(project, nil)
	if err != nil {
		t.Fatalf("encodeProject() error = %v", err)
	}
	got, extra, err := decodeProject(data)
	if err != nil {
		t.Fatalf("decodeProject() error = %v\n%s", err, data)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %v, want none", extra)
	}
	if !reflect.DeepEqual(got, project) {
		t.Fatalf("round trip mismatch:\n got  = %#v\n want = %#v", got, project)
	}
}

func TestActorDocumentRoundTrip(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	actor := core.Actor{ID: "act-1", Kind: core.ActorAgent, Name: "claude", Active: true, CreatedAt: created}
	data, err := encodeActor(actor, nil)
	if err != nil {
		t.Fatalf("encodeActor() error = %v", err)
	}
	got, extra, err := decodeActor(data)
	if err != nil {
		t.Fatalf("decodeActor() error = %v\n%s", err, data)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %v, want none", extra)
	}
	if !reflect.DeepEqual(got, actor) {
		t.Fatalf("round trip mismatch:\n got  = %#v\n want = %#v", got, actor)
	}
}

func TestArtifactDocumentRoundTrip(t *testing.T) {
	created := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	taskID := core.TaskID("t-1")
	artifact := core.Artifact{
		ID:        "art-1",
		ProjectID: "prj-1",
		TaskID:    &taskID,
		Kind:      core.ArtifactMemory,
		Title:     "notes",
		Brief:     "when to load",
		Body:      "the body\n\nwith a blank line",
		Links:     []core.Link{{Kind: core.LinkDoc, URL: "https://example.test/doc"}},
		CreatedAt: created,
		UpdatedAt: created,
	}
	data, err := encodeArtifact(artifact, nil)
	if err != nil {
		t.Fatalf("encodeArtifact() error = %v", err)
	}
	got, extra, err := decodeArtifact(data)
	if err != nil {
		t.Fatalf("decodeArtifact() error = %v\n%s", err, data)
	}
	if len(extra) != 0 {
		t.Fatalf("extra = %v, want none", extra)
	}
	if !reflect.DeepEqual(got, artifact) {
		t.Fatalf("round trip mismatch:\n got  = %#v\n want = %#v", got, artifact)
	}
}

func TestProjectDocumentDefaultsPolicy(t *testing.T) {
	input := "---\nid: prj-1\nname: Acme\n---\n\ndescription\n"
	project, _, err := decodeProject([]byte(input))
	if err != nil {
		t.Fatalf("decodeProject() error = %v", err)
	}
	if len(project.Policy.TaskStatuses) == 0 {
		t.Fatalf("project policy = %#v, want a default", project.Policy)
	}
}
