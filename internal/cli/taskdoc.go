package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

type linkDoc struct {
	Kind  string `json:"kind" yaml:"kind"`
	URL   string `json:"url" yaml:"url"`
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
}

type noteDoc struct {
	ID        string    `json:"id" yaml:"id"`
	Author    string    `json:"author,omitempty" yaml:"author,omitempty"`
	Body      string    `json:"body" yaml:"body"`
	Links     []linkDoc `json:"links,omitempty" yaml:"links,omitempty"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

type snoozeDoc struct {
	Until      *time.Time `json:"until,omitempty" yaml:"until,omitempty"`
	UntilTask  *string    `json:"until_task,omitempty" yaml:"until_task,omitempty"`
	Indefinite bool       `json:"indefinite,omitempty" yaml:"indefinite,omitempty"`
}

// taskDoc is the stable document exchanged by `task get -o json|yaml` and
// `task apply -f`. Pointer fields distinguish an omitted field (leave
// unchanged, or ignore for read-only fields) from an explicit value.
type taskDoc struct {
	ID          *string    `json:"id,omitempty" yaml:"id,omitempty"`
	ProjectID   *string    `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	Repo        *string    `json:"repo,omitempty" yaml:"repo,omitempty"`
	Kind        *string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Title       *string    `json:"title,omitempty" yaml:"title,omitempty"`
	Description *string    `json:"description,omitempty" yaml:"description,omitempty"`
	Status      *string    `json:"status,omitempty" yaml:"status,omitempty"`
	Priority    *int       `json:"priority,omitempty" yaml:"priority,omitempty"`
	Labels      *[]string  `json:"labels,omitempty" yaml:"labels,omitempty"`
	Assignee    *string    `json:"assignee,omitempty" yaml:"assignee,omitempty"`
	Deps        *[]string  `json:"deps,omitempty" yaml:"deps,omitempty"`
	Dependents  *[]string  `json:"dependents,omitempty" yaml:"dependents,omitempty"`
	WaitingOn   *[]string  `json:"waiting_on,omitempty" yaml:"waiting_on,omitempty"`
	Notes       []noteDoc  `json:"notes,omitempty" yaml:"notes,omitempty"`
	NotBefore   *time.Time `json:"not_before,omitempty" yaml:"not_before,omitempty"`
	Snooze      *snoozeDoc `json:"snooze,omitempty" yaml:"snooze,omitempty"`
	CreatedAt   *time.Time `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
}

// taskDocFrom renders a task as the round-trippable document. Relation fields
// (assignee, deps, waiting_on) are shown but managed by dedicated commands.
func taskDocFrom(task *core.Task) taskDoc {
	id := string(task.ID)
	projectID := string(task.ProjectID)
	repo := task.Repo
	kind := string(task.Kind)
	title := task.Title
	description := task.Description
	status := string(task.Status)
	priority := task.Priority
	labels := append([]string{}, task.Labels...)
	deps := make([]string, 0, len(task.Deps))
	for _, dep := range task.Deps {
		deps = append(deps, string(dep))
	}
	waitingOn := make([]string, 0, len(task.WaitingOn))
	for _, actor := range task.WaitingOn {
		waitingOn = append(waitingOn, string(actor))
	}
	createdAt := task.CreatedAt
	updatedAt := task.UpdatedAt
	notes := make([]noteDoc, 0, len(task.Notes))
	for _, note := range task.Notes {
		entry := noteDoc{ID: note.ID, Author: string(note.Author), Body: note.Body, CreatedAt: note.CreatedAt}
		for _, link := range note.Links {
			entry.Links = append(entry.Links, linkDoc{Kind: string(link.Kind), URL: link.URL, Title: link.Title})
		}
		notes = append(notes, entry)
	}
	doc := taskDoc{
		ID:          &id,
		ProjectID:   &projectID,
		Repo:        &repo,
		Kind:        &kind,
		Title:       &title,
		Description: &description,
		Status:      &status,
		Priority:    &priority,
		Labels:      &labels,
		Deps:        &deps,
		WaitingOn:   &waitingOn,
		Notes:       notes,
		CreatedAt:   &createdAt,
		UpdatedAt:   &updatedAt,
	}
	if task.AssigneeID != nil {
		assignee := string(*task.AssigneeID)
		doc.Assignee = &assignee
	}
	if task.NotBefore != nil {
		notBefore := *task.NotBefore
		doc.NotBefore = &notBefore
	}
	if task.Snooze != nil {
		doc.Snooze = snoozeDocFrom(*task.Snooze)
	}
	return doc
}

func snoozeDocFrom(snooze core.Snooze) *snoozeDoc {
	doc := &snoozeDoc{Indefinite: snooze.Indefinite}
	if snooze.Until != nil {
		until := *snooze.Until
		doc.Until = &until
	}
	if snooze.UntilTask != nil {
		untilTask := string(*snooze.UntilTask)
		doc.UntilTask = &untilTask
	}
	return doc
}

// taskDocFieldNames lists every selectable field of a task document.
var taskDocFieldNames = []string{
	"id", "project_id", "repo", "kind", "title", "description", "status",
	"priority", "labels", "assignee", "deps", "dependents", "waiting_on",
	"notes", "not_before", "created_at", "updated_at",
}

// taskDocValues renders a task document as selectable key/value pairs. Keys
// that the document does not carry are omitted.
func taskDocValues(doc taskDoc) map[string]any {
	out := make(map[string]any, len(taskDocFieldNames))
	if doc.ID != nil {
		out["id"] = *doc.ID
	}
	if doc.ProjectID != nil {
		out["project_id"] = *doc.ProjectID
	}
	if doc.Repo != nil {
		out["repo"] = *doc.Repo
	}
	if doc.Kind != nil {
		out["kind"] = *doc.Kind
	}
	if doc.Title != nil {
		out["title"] = *doc.Title
	}
	if doc.Description != nil {
		out["description"] = *doc.Description
	}
	if doc.Status != nil {
		out["status"] = *doc.Status
	}
	if doc.Priority != nil {
		out["priority"] = *doc.Priority
	}
	if doc.Labels != nil {
		out["labels"] = *doc.Labels
	}
	if doc.Assignee != nil {
		out["assignee"] = *doc.Assignee
	}
	if doc.Deps != nil {
		out["deps"] = *doc.Deps
	}
	if doc.Dependents != nil {
		out["dependents"] = *doc.Dependents
	}
	if doc.WaitingOn != nil {
		out["waiting_on"] = *doc.WaitingOn
	}
	if len(doc.Notes) > 0 {
		out["notes"] = doc.Notes
	}
	if doc.NotBefore != nil {
		out["not_before"] = *doc.NotBefore
	}
	if doc.CreatedAt != nil {
		out["created_at"] = *doc.CreatedAt
	}
	if doc.UpdatedAt != nil {
		out["updated_at"] = *doc.UpdatedAt
	}
	return out
}

// taskSet validates the document against the current task and maps its mutable
// fields into an app.TaskSet. Immutable and command-managed fields may be
// echoed unchanged but must not be modified.
func (doc taskDoc) taskSet(current *core.Task) (app.TaskSet, error) {
	if doc.ID != nil && *doc.ID != string(current.ID) {
		return app.TaskSet{}, fmt.Errorf("%w: id is immutable", core.ErrInvalid)
	}
	if doc.ProjectID != nil && *doc.ProjectID != string(current.ProjectID) {
		return app.TaskSet{}, fmt.Errorf("%w: project_id is immutable", core.ErrInvalid)
	}
	// created_at is server-owned bookkeeping; it is carried for visibility and
	// otherwise ignored. updated_at is a real compare-and-swap token: the store
	// rejects the write if the task changed since the document was read.
	if doc.Assignee != nil && !equalOptionalString(doc.Assignee, actorIDString(current.AssigneeID)) {
		return app.TaskSet{}, fmt.Errorf("%w: assignee is managed by `ft task assign`", core.ErrInvalid)
	}
	currentDeps := make([]string, 0, len(current.Deps))
	for _, dep := range current.Deps {
		currentDeps = append(currentDeps, string(dep))
	}
	if !equalStringSet(doc.Deps, currentDeps) {
		return app.TaskSet{}, fmt.Errorf("%w: deps are managed by `ft task dep`", core.ErrInvalid)
	}
	currentWaiting := make([]string, 0, len(current.WaitingOn))
	for _, actor := range current.WaitingOn {
		currentWaiting = append(currentWaiting, string(actor))
	}
	if !equalStringSet(doc.WaitingOn, currentWaiting) {
		return app.TaskSet{}, fmt.Errorf("%w: waiting_on is managed by `ft task wait`", core.ErrInvalid)
	}
	if !equalNotes(doc.Notes, current.Notes) {
		return app.TaskSet{}, fmt.Errorf("%w: notes are managed by `ft task note`", core.ErrInvalid)
	}
	if !equalSnooze(doc.Snooze, current.Snooze) {
		return app.TaskSet{}, fmt.Errorf("%w: snooze is managed by `ft task snooze`", core.ErrInvalid)
	}

	var set app.TaskSet
	if doc.Kind != nil && *doc.Kind != string(current.Kind) {
		kind := core.TaskKind(*doc.Kind)
		if !kind.Valid() {
			return app.TaskSet{}, fmt.Errorf("%w: unknown task kind %q", core.ErrInvalid, *doc.Kind)
		}
		set.Kind = &kind
	}
	if doc.Status != nil && *doc.Status != string(current.Status) {
		status := core.TaskStatus(*doc.Status)
		if !status.Valid() {
			return app.TaskSet{}, fmt.Errorf("%w: unknown task status %q", core.ErrInvalid, *doc.Status)
		}
		set.Status = &status
	}
	if doc.Repo != nil && *doc.Repo != current.Repo {
		set.Repo = doc.Repo
	}
	if doc.Title != nil && *doc.Title != current.Title {
		set.Title = doc.Title
	}
	if doc.Description != nil && *doc.Description != current.Description {
		set.Description = doc.Description
	}
	if doc.Priority != nil && *doc.Priority != current.Priority {
		set.Priority = doc.Priority
	}
	if doc.NotBefore != nil && (current.NotBefore == nil || !doc.NotBefore.Equal(*current.NotBefore)) {
		notBefore := *doc.NotBefore
		set.NotBefore = &notBefore
	}
	if doc.Labels != nil && !equalStringSet(doc.Labels, current.Labels) {
		set.Labels = *doc.Labels
	}
	set.Expect = doc.UpdatedAt
	return set, nil
}

func parseTaskDoc(data []byte, format string) (taskDoc, error) {
	var doc taskDoc
	switch format {
	case "json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&doc); err != nil {
			return taskDoc{}, fmt.Errorf("parse json: %w", err)
		}
	case "yaml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&doc); err != nil {
			return taskDoc{}, fmt.Errorf("parse yaml: %w", err)
		}
	default:
		return taskDoc{}, fmt.Errorf("unknown format %q, want json or yaml", format)
	}
	return doc, nil
}

// parseTaskDocs decodes one or more task documents: a JSON array, or YAML
// documents separated by `---` (a single document of either form also works).
func parseTaskDocs(data []byte, format string) ([]taskDoc, error) {
	switch format {
	case "json":
		if trimmed := bytes.TrimSpace(data); len(trimmed) > 0 && trimmed[0] == '[' {
			var docs []taskDoc
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&docs); err != nil {
				return nil, fmt.Errorf("parse json: %w", err)
			}
			return docs, nil
		}
		doc, err := parseTaskDoc(data, format)
		if err != nil {
			return nil, err
		}
		return []taskDoc{doc}, nil
	case "yaml":
		var docs []taskDoc
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		for {
			var doc taskDoc
			err := decoder.Decode(&doc)
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("parse yaml: %w", err)
			}
			docs = append(docs, doc)
		}
		if len(docs) == 0 {
			return nil, fmt.Errorf("no documents found")
		}
		return docs, nil
	default:
		return nil, fmt.Errorf("unknown format %q, want json or yaml", format)
	}
}

func marshalTaskDoc(doc taskDoc, format string) ([]byte, error) {
	switch format {
	case "json":
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return nil, err
		}
		return append(data, '\n'), nil
	case "yaml":
		return yaml.Marshal(doc)
	default:
		return nil, fmt.Errorf("unknown format %q, want json or yaml", format)
	}
}

// docFormat picks the parser from an explicit format, else the file extension,
// defaulting to JSON.
func docFormat(filename, explicit string) string {
	if explicit != "" {
		return explicit
	}
	switch strings.ToLower(filepath.Ext(filename)) {
	case ".yaml", ".yml":
		return "yaml"
	default:
		return "json"
	}
}

// editTask opens contents in $VISUAL/$EDITOR (falling back to vi) and returns
// the edited bytes.
func editTask(contents []byte, format string) ([]byte, error) {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	file, err := os.CreateTemp("", "factotum-task-*."+format)
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	path := file.Name()
	defer func() { _ = os.Remove(path) }()
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("write temp file: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("write temp file: %w", err)
	}

	parts := strings.Fields(editor)
	command := exec.Command(parts[0], append(parts[1:], path)...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf("editor %q: %w", editor, err)
	}
	return os.ReadFile(path)
}

// equalNotes reports whether an optional document note list matches the
// current notes. A nil document list means "not supplied".
func equalNotes(doc []noteDoc, current []core.Note) bool {
	if doc == nil {
		return true
	}
	if len(doc) != len(current) {
		return false
	}
	for i := range doc {
		if doc[i].ID != current[i].ID || doc[i].Body != current[i].Body {
			return false
		}
	}
	return true
}

// equalSnooze reports whether an optional document snooze matches the current
// snooze. A nil document snooze means "not supplied".
func equalSnooze(doc *snoozeDoc, current *core.Snooze) bool {
	if doc == nil {
		return true
	}
	if current == nil {
		return false
	}
	if doc.Indefinite != current.Indefinite {
		return false
	}
	if !equalOptionalTime(doc.Until, current.Until) {
		return false
	}
	return equalOptionalTask(doc.UntilTask, current.UntilTask)
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func equalOptionalTask(left *string, right *core.TaskID) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == string(*right)
}

func actorIDString(id *core.ActorID) string {
	if id == nil {
		return ""
	}
	return string(*id)
}

func equalOptionalString(provided *string, current string) bool {
	if provided == nil {
		return true
	}
	return *provided == current
}

// equalStringSet reports whether an optional provided list matches current as
// an unordered set. A nil provided list means "not supplied".
func equalStringSet(provided *[]string, current []string) bool {
	if provided == nil {
		return true
	}
	if len(*provided) != len(current) {
		return false
	}
	counts := make(map[string]int, len(current))
	for _, value := range current {
		counts[value]++
	}
	for _, value := range *provided {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}
