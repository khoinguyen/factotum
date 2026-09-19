package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

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
		CreatedAt:   &createdAt,
		UpdatedAt:   &updatedAt,
	}
	if task.AssigneeID != nil {
		assignee := string(*task.AssigneeID)
		doc.Assignee = &assignee
	}
	return doc
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
