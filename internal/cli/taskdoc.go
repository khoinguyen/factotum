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
	// System is read-only provenance: set with `ft task note create --system`.
	System bool `json:"system,omitempty" yaml:"system,omitempty"`
}

type snoozeDoc struct {
	Until      *time.Time `json:"until,omitempty" yaml:"until,omitempty"`
	UntilTask  *string    `json:"until_task,omitempty" yaml:"until_task,omitempty"`
	Indefinite bool       `json:"indefinite,omitempty" yaml:"indefinite,omitempty"`
}

// notReadyDoc is the read-only readiness diagnostic carried by `task get`:
// the stable code a task is excluded from the ready set by, and its detail.
type notReadyDoc struct {
	ReasonCode string `json:"reason_code" yaml:"reason_code"`
	Detail     string `json:"detail" yaml:"detail"`
}

// taskBase is the revision a task document was derived from: the field values
// `task get -o json|yaml` observed when it produced the document. `task apply`
// three-way merges the document against this base and the stored task, so a
// stale document still applies when it and a concurrent change touched
// different fields.
type taskBase struct {
	Repo               *string    `json:"repo,omitempty" yaml:"repo,omitempty"`
	Kind               *string    `json:"kind,omitempty" yaml:"kind,omitempty"`
	Title              *string    `json:"title,omitempty" yaml:"title,omitempty"`
	Description        *string    `json:"description,omitempty" yaml:"description,omitempty"`
	Status             *string    `json:"status,omitempty" yaml:"status,omitempty"`
	Priority           *int       `json:"priority,omitempty" yaml:"priority,omitempty"`
	Labels             *[]string  `json:"labels,omitempty" yaml:"labels,omitempty"`
	Groomed            *bool      `json:"groomed,omitempty" yaml:"groomed,omitempty"`
	AcceptanceCriteria *[]string  `json:"acceptance_criteria,omitempty" yaml:"acceptance_criteria,omitempty"`
	Assignee           *string    `json:"assignee,omitempty" yaml:"assignee,omitempty"`
	Deps               *[]string  `json:"deps,omitempty" yaml:"deps,omitempty"`
	WaitingOn          *[]string  `json:"waiting_on,omitempty" yaml:"waiting_on,omitempty"`
	Notes              []noteDoc  `json:"notes,omitempty" yaml:"notes,omitempty"`
	Snooze             *snoozeDoc `json:"snooze,omitempty" yaml:"snooze,omitempty"`
	NotBefore          *time.Time `json:"not_before,omitempty" yaml:"not_before,omitempty"`
}

// taskDoc is the stable document exchanged by `task get -o json|yaml` and
// `task apply -f`. Pointer fields distinguish an omitted field (leave
// unchanged, or ignore for read-only fields) from an explicit value.
type taskDoc struct {
	ID          *string   `json:"id,omitempty" yaml:"id,omitempty"`
	ProjectID   *string   `json:"project_id,omitempty" yaml:"project_id,omitempty"`
	Repo        *string   `json:"repo,omitempty" yaml:"repo,omitempty"`
	Kind        *string   `json:"kind,omitempty" yaml:"kind,omitempty"`
	Title       *string   `json:"title,omitempty" yaml:"title,omitempty"`
	Description *string   `json:"description,omitempty" yaml:"description,omitempty"`
	Status      *string   `json:"status,omitempty" yaml:"status,omitempty"`
	Priority    *int      `json:"priority,omitempty" yaml:"priority,omitempty"`
	Labels      *[]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Groomed     *bool     `json:"groomed,omitempty" yaml:"groomed,omitempty"`
	// AcceptanceCriteria are the observable conditions that define done; a
	// groomed task carries at least one.
	AcceptanceCriteria *[]string `json:"acceptance_criteria,omitempty" yaml:"acceptance_criteria,omitempty"`
	Assignee           *string   `json:"assignee,omitempty" yaml:"assignee,omitempty"`
	Deps               *[]string `json:"deps,omitempty" yaml:"deps,omitempty"`
	Dependents         *[]string `json:"dependents,omitempty" yaml:"dependents,omitempty"`
	// Origin and OriginTitle are read-only provenance: the capture (idea or bug)
	// this task was promoted from, resolved from its dependencies. They are
	// ignored on apply.
	Origin      *string    `json:"origin,omitempty" yaml:"origin,omitempty"`
	OriginTitle *string    `json:"origin_title,omitempty" yaml:"origin_title,omitempty"`
	WaitingOn   *[]string  `json:"waiting_on,omitempty" yaml:"waiting_on,omitempty"`
	Notes       []noteDoc  `json:"notes,omitempty" yaml:"notes,omitempty"`
	NotBefore   *time.Time `json:"not_before,omitempty" yaml:"not_before,omitempty"`
	Snooze      *snoozeDoc `json:"snooze,omitempty" yaml:"snooze,omitempty"`
	// NotReady is read-only: it is derived from the graph and ignored on apply.
	NotReady *notReadyDoc `json:"not_ready,omitempty" yaml:"not_ready,omitempty"`
	// Checks is read-only: cached check results, populated only by `task get`
	// and ignored on apply.
	Checks    []checkResultDoc `json:"checks,omitempty" yaml:"checks,omitempty"`
	CreatedAt *time.Time       `json:"created_at,omitempty" yaml:"created_at,omitempty"`
	UpdatedAt *time.Time       `json:"updated_at,omitempty" yaml:"updated_at,omitempty"`
	// Base is the revision the document was derived from, captured by
	// `task get -o json|yaml`. `task apply` three-way merges the document's
	// fields against it. It is read-only input, not a field of the task.
	Base *taskBase `json:"base,omitempty" yaml:"base,omitempty"`
}

// taskDocFrom renders a task as the round-trippable document. Relation fields
// (assignee, deps, waiting_on) are shown but managed by dedicated commands.
func taskDocFrom(task *core.Ticket) taskDoc {
	id := string(task.ID)
	projectID := string(task.ProjectID)
	repo := task.Repo
	kind := string(task.Kind)
	title := task.Title
	description := task.Description
	status := string(task.Status)
	priority := task.Priority
	labels := append([]string{}, task.Labels...)
	groomed := task.Groomed
	acceptanceCriteria := append([]string{}, task.AcceptanceCriteria...)
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
		ID:                 &id,
		ProjectID:          &projectID,
		Repo:               &repo,
		Kind:               &kind,
		Title:              &title,
		Description:        &description,
		Status:             &status,
		Priority:           &priority,
		Labels:             &labels,
		Groomed:            &groomed,
		AcceptanceCriteria: &acceptanceCriteria,
		Deps:               &deps,
		WaitingOn:          &waitingOn,
		Notes:              noteDocsFrom(task.Notes),
		CreatedAt:          &createdAt,
		UpdatedAt:          &updatedAt,
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

func noteDocsFrom(notes []core.Note) []noteDoc {
	out := make([]noteDoc, 0, len(notes))
	for _, note := range notes {
		entry := noteDoc{ID: note.ID, Author: string(note.Author), Body: note.Body, CreatedAt: note.CreatedAt, System: note.System}
		for _, link := range note.Links {
			entry.Links = append(entry.Links, linkDoc{Kind: string(link.Kind), URL: link.URL, Title: link.Title})
		}
		out = append(out, entry)
	}
	return out
}

// taskBaseFrom snapshots the fields a document can carry a change to, so a
// document produced by `task get` remembers the revision it was derived from.
func taskBaseFrom(task *core.Ticket) *taskBase {
	repo := task.Repo
	kind := string(task.Kind)
	title := task.Title
	description := task.Description
	status := string(task.Status)
	priority := task.Priority
	labels := append([]string{}, task.Labels...)
	groomed := task.Groomed
	acceptanceCriteria := append([]string{}, task.AcceptanceCriteria...)
	deps := make([]string, 0, len(task.Deps))
	for _, dep := range task.Deps {
		deps = append(deps, string(dep))
	}
	waitingOn := make([]string, 0, len(task.WaitingOn))
	for _, actor := range task.WaitingOn {
		waitingOn = append(waitingOn, string(actor))
	}
	base := &taskBase{
		Repo:               &repo,
		Kind:               &kind,
		Title:              &title,
		Description:        &description,
		Status:             &status,
		Priority:           &priority,
		Labels:             &labels,
		Groomed:            &groomed,
		AcceptanceCriteria: &acceptanceCriteria,
		Deps:               &deps,
		WaitingOn:          &waitingOn,
		Notes:              noteDocsFrom(task.Notes),
	}
	if task.AssigneeID != nil {
		assignee := string(*task.AssigneeID)
		base.Assignee = &assignee
	}
	if task.NotBefore != nil {
		notBefore := *task.NotBefore
		base.NotBefore = &notBefore
	}
	if task.Snooze != nil {
		base.Snooze = snoozeDocFrom(*task.Snooze)
	}
	return base
}

// taskListEntry is the stable, snake_case shape of one `task list` row. Its
// keys are a deliberate subset of taskDoc's, so a single parser handles both
// `task get` and `task list`.
type taskListEntry struct {
	ID        string    `json:"id" yaml:"id"`
	ProjectID string    `json:"project_id" yaml:"project_id"`
	Repo      string    `json:"repo" yaml:"repo"`
	Kind      string    `json:"kind" yaml:"kind"`
	Title     string    `json:"title" yaml:"title"`
	Status    string    `json:"status" yaml:"status"`
	Priority  int       `json:"priority" yaml:"priority"`
	Labels    []string  `json:"labels" yaml:"labels"`
	Groomed   bool      `json:"groomed,omitempty" yaml:"groomed,omitempty"`
	Assignee  string    `json:"assignee,omitempty" yaml:"assignee,omitempty"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

func taskListEntryFrom(task *core.Ticket) taskListEntry {
	entry := taskListEntry{
		ID:        string(task.ID),
		ProjectID: string(task.ProjectID),
		Repo:      task.Repo,
		Kind:      string(task.Kind),
		Title:     task.Title,
		Status:    string(task.Status),
		Priority:  task.Priority,
		Labels:    append([]string{}, task.Labels...),
		Groomed:   task.Groomed,
		CreatedAt: task.CreatedAt,
		UpdatedAt: task.UpdatedAt,
	}
	if task.AssigneeID != nil {
		entry.Assignee = string(*task.AssigneeID)
	}
	return entry
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
	"priority", "labels", "groomed", "acceptance_criteria", "assignee", "deps",
	"dependents", "origin", "origin_title", "waiting_on", "notes", "checks",
	"not_ready", "not_before", "created_at", "updated_at",
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
	if doc.Groomed != nil {
		out["groomed"] = *doc.Groomed
	}
	if doc.AcceptanceCriteria != nil {
		out["acceptance_criteria"] = *doc.AcceptanceCriteria
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
	if doc.Origin != nil {
		out["origin"] = *doc.Origin
	}
	if doc.OriginTitle != nil {
		out["origin_title"] = *doc.OriginTitle
	}
	if doc.WaitingOn != nil {
		out["waiting_on"] = *doc.WaitingOn
	}
	if len(doc.Notes) > 0 {
		out["notes"] = doc.Notes
	}
	if doc.Checks != nil {
		out["checks"] = doc.Checks
	}
	if doc.NotReady != nil {
		out["not_ready"] = *doc.NotReady
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

// taskSet validates the document against the current task and maps its changed
// fields into an app.TicketSet.
//
// When the document carries the Base revision it was derived from, changes are
// three-way merged with concurrent writes: a field only the document touched
// takes the document's value, a field only a concurrent write touched keeps the
// stored value, and a field both changed to different values fails with a
// conflict naming it. Without a Base (a hand-written document), the document is
// compared directly to the current task and updated_at stays a compare-and-swap
// token.
func (doc taskDoc) taskSet(current *core.Ticket) (app.TicketSet, error) {
	if doc.ID != nil && *doc.ID != string(current.ID) {
		return app.TicketSet{}, fmt.Errorf("%w: id is immutable", core.ErrInvalid)
	}
	if doc.ProjectID != nil && *doc.ProjectID != string(current.ProjectID) {
		return app.TicketSet{}, fmt.Errorf("%w: project_id is immutable", core.ErrInvalid)
	}
	// The revision the document was derived from; without a base block this is
	// the current task, which reduces the merge to a direct comparison.
	base := doc.baseValues(current)

	// Immutable and command-managed fields may be echoed back unchanged but
	// must not be modified. Compared against the base so a concurrent change to
	// a managed field is preserved rather than blamed on the document.
	if !equalOptionalString(doc.Assignee, base.assignee) {
		return app.TicketSet{}, fmt.Errorf("%w: assignee is managed by `ft task assign`", core.ErrInvalid)
	}
	if !equalStringSet(doc.Deps, base.deps) {
		return app.TicketSet{}, fmt.Errorf("%w: deps are managed by `ft task dep`", core.ErrInvalid)
	}
	if !equalStringSet(doc.WaitingOn, base.waitingOn) {
		return app.TicketSet{}, fmt.Errorf("%w: waiting_on is managed by `ft task wait`", core.ErrInvalid)
	}
	if doc.Notes != nil && !equalNoteDocs(doc.Notes, base.notes) {
		return app.TicketSet{}, fmt.Errorf("%w: notes are managed by `ft task note`", core.ErrInvalid)
	}
	if doc.Snooze != nil && !equalSnoozeDocs(doc.Snooze, base.snooze) {
		return app.TicketSet{}, fmt.Errorf("%w: snooze is managed by `ft task snooze`", core.ErrInvalid)
	}

	var set app.TicketSet
	var conflicts []string
	record := func(field string) { conflicts = append(conflicts, field) }

	if value, changed, conflict := mergeScalar(doc.Kind, base.kind, string(current.Kind)); conflict {
		record("kind")
	} else if changed {
		kind := core.TicketKind(*value)
		if !kind.Valid() {
			return app.TicketSet{}, fmt.Errorf("%w: unknown task kind %q", core.ErrInvalid, *doc.Kind)
		}
		set.Kind = &kind
	}
	if value, changed, conflict := mergeScalar(doc.Status, base.status, string(current.Status)); conflict {
		record("status")
	} else if changed {
		status := core.TicketStatus(*value)
		if !status.Valid() {
			return app.TicketSet{}, fmt.Errorf("%w: unknown task status %q", core.ErrInvalid, *doc.Status)
		}
		set.Status = &status
	}
	if value, changed, conflict := mergeScalar(doc.Repo, base.repo, current.Repo); conflict {
		record("repo")
	} else if changed {
		set.Repo = value
	}
	if value, changed, conflict := mergeScalar(doc.Title, base.title, current.Title); conflict {
		record("title")
	} else if changed {
		set.Title = value
	}
	if value, changed, conflict := mergeScalar(doc.Description, base.description, current.Description); conflict {
		record("description")
	} else if changed {
		set.Description = value
	}
	if value, changed, conflict := mergeScalar(doc.Priority, base.priority, current.Priority); conflict {
		record("priority")
	} else if changed {
		set.Priority = value
	}
	if value, changed, conflict := mergeScalar(doc.Groomed, base.groomed, current.Groomed); conflict {
		record("groomed")
	} else if changed {
		set.Groomed = value
	}
	if value, changed, conflict := mergeStringSet(doc.Labels, base.labels, current.Labels); conflict {
		record("labels")
	} else if changed {
		set.Labels = *value
	}
	if value, changed, conflict := mergeStringSlice(doc.AcceptanceCriteria, base.acceptanceCriteria, current.AcceptanceCriteria); conflict {
		record("acceptance_criteria")
	} else if changed {
		set.AcceptanceCriteria = *value
	}
	if value, changed, conflict := mergeTime(doc.NotBefore, base.notBefore, current.NotBefore); conflict {
		record("not_before")
	} else if changed {
		set.NotBefore = value
	}

	if len(conflicts) > 0 {
		return app.TicketSet{}, fmt.Errorf("%w: task %s was modified concurrently; conflicting fields: %s",
			core.ErrConflict, current.ID, strings.Join(conflicts, ", "))
	}
	if doc.Base != nil {
		expected := current.UpdatedAt
		set.Expect = &expected
	} else {
		set.Expect = doc.UpdatedAt
	}
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

// equalNoteDocs reports whether two note lists match by identity and body, the
// fields a document can carry back.
func equalNoteDocs(left, right []noteDoc) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].ID != right[i].ID || left[i].Body != right[i].Body {
			return false
		}
	}
	return true
}

// equalSnoozeDocs reports whether two snooze documents match.
func equalSnoozeDocs(left, right *snoozeDoc) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.Indefinite != right.Indefinite {
		return false
	}
	if !equalOptionalTime(left.Until, right.Until) {
		return false
	}
	return equalOptionalStringPtr(left.UntilTask, right.UntilTask)
}

func equalOptionalTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func equalOptionalStringPtr(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func currentDeps(current *core.Ticket) []string {
	out := make([]string, 0, len(current.Deps))
	for _, dep := range current.Deps {
		out = append(out, string(dep))
	}
	return out
}

func currentWaiting(current *core.Ticket) []string {
	out := make([]string, 0, len(current.WaitingOn))
	for _, actor := range current.WaitingOn {
		out = append(out, string(actor))
	}
	return out
}

// taskBaseValues is the base revision with every field resolved to a concrete
// value. A document without a base block resolves to the current task, which
// makes every three-way merge a direct comparison.
type taskBaseValues struct {
	repo               string
	kind               string
	title              string
	description        string
	status             string
	priority           int
	labels             []string
	groomed            bool
	acceptanceCriteria []string
	notBefore          *time.Time
	assignee           string
	deps               []string
	waitingOn          []string
	notes              []noteDoc
	snooze             *snoozeDoc
}

// baseValues resolves the revision the document was derived from. Without a
// base block the current task is the base.
func (doc taskDoc) baseValues(current *core.Ticket) taskBaseValues {
	values := taskBaseValues{
		repo:               current.Repo,
		kind:               string(current.Kind),
		title:              current.Title,
		description:        current.Description,
		status:             string(current.Status),
		priority:           current.Priority,
		labels:             append([]string{}, current.Labels...),
		groomed:            current.Groomed,
		acceptanceCriteria: append([]string{}, current.AcceptanceCriteria...),
		notBefore:          current.NotBefore,
		assignee:           actorIDString(current.AssigneeID),
		deps:               currentDeps(current),
		waitingOn:          currentWaiting(current),
		notes:              noteDocsFrom(current.Notes),
	}
	if current.Snooze != nil {
		values.snooze = snoozeDocFrom(*current.Snooze)
	}
	if doc.Base == nil {
		return values
	}
	// A base block is a complete snapshot: a nil optional field means the base
	// carried no value, not that the value is unknown.
	values.repo = derefString(doc.Base.Repo)
	values.kind = derefString(doc.Base.Kind)
	values.title = derefString(doc.Base.Title)
	values.description = derefString(doc.Base.Description)
	values.status = derefString(doc.Base.Status)
	if doc.Base.Priority != nil {
		values.priority = *doc.Base.Priority
	}
	values.labels = derefStringSlice(doc.Base.Labels)
	if doc.Base.Groomed != nil {
		values.groomed = *doc.Base.Groomed
	}
	values.acceptanceCriteria = derefStringSlice(doc.Base.AcceptanceCriteria)
	values.notBefore = doc.Base.NotBefore
	values.assignee = derefString(doc.Base.Assignee)
	values.deps = derefStringSlice(doc.Base.Deps)
	values.waitingOn = derefStringSlice(doc.Base.WaitingOn)
	values.notes = doc.Base.Notes
	values.snooze = doc.Base.Snooze
	return values
}

func derefStringSlice(value *[]string) []string {
	if value == nil {
		return nil
	}
	return *value
}

// mergeScalar three-way merges one comparable field. ours is the document's
// value (nil means "not supplied"); base and theirs are the revision the
// document was derived from and the stored value. It reports whether the
// document changed the field to a new value and whether the document and a
// concurrent write diverged.
func mergeScalar[T comparable](ours *T, base, theirs T) (value *T, changed, conflict bool) {
	if ours == nil {
		return nil, false, false
	}
	if *ours == base {
		return nil, false, false
	}
	if *ours == theirs {
		return nil, false, false
	}
	if theirs != base {
		return nil, false, true
	}
	return ours, true, false
}

// mergeStringSet three-way merges an unordered string-list field.
func mergeStringSet(ours *[]string, base, theirs []string) (value *[]string, changed, conflict bool) {
	if ours == nil {
		return nil, false, false
	}
	if equalStringSet(ours, base) {
		return nil, false, false
	}
	if equalStringSet(ours, theirs) {
		return nil, false, false
	}
	if !equalStringSet(&theirs, base) {
		return nil, false, true
	}
	return ours, true, false
}

// mergeStringSlice three-way merges an ordered string-list field.
func mergeStringSlice(ours *[]string, base, theirs []string) (value *[]string, changed, conflict bool) {
	if ours == nil {
		return nil, false, false
	}
	if equalStringSlice(ours, base) {
		return nil, false, false
	}
	if equalStringSlice(ours, theirs) {
		return nil, false, false
	}
	if !equalStringSlice(&theirs, base) {
		return nil, false, true
	}
	return ours, true, false
}

// mergeTime three-way merges an optional timestamp field.
func mergeTime(ours, base, theirs *time.Time) (value *time.Time, changed, conflict bool) {
	if ours == nil {
		return nil, false, false
	}
	if equalOptionalTime(ours, base) {
		return nil, false, false
	}
	if equalOptionalTime(ours, theirs) {
		return nil, false, false
	}
	if !equalOptionalTime(theirs, base) {
		return nil, false, true
	}
	return ours, true, false
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

// equalStringSlice reports whether an optional provided list matches current in
// order. A nil provided list means "not supplied".
func equalStringSlice(provided *[]string, current []string) bool {
	if provided == nil {
		return true
	}
	if len(*provided) != len(current) {
		return false
	}
	for i := range *provided {
		if (*provided)[i] != current[i] {
			return false
		}
	}
	return true
}
