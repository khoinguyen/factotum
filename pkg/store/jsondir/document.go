package jsondir

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/core"
)

// A jsondir document is a markdown file with a YAML frontmatter block between
// two `---` lines. Structured metadata lives in the frontmatter; the free-text
// description (or artifact body) is the markdown body. Task notes are appended
// as delimited sections so the body stays free of machine markup.
//
// Keys are emitted in a fixed order and unknown frontmatter keys are retained
// verbatim, so a hand-edited file round-trips without losing metadata this
// version does not know about.

const (
	notesMarker = "<!-- ft:notes -->"
	notePrefix  = "<!-- ft:note: "
	noteSuffix  = " -->"
)

// linkDoc is the on-disk shape of a core.Link, shared by frontmatter and note
// metadata.
type linkDoc struct {
	Kind  string `json:"kind" yaml:"kind"`
	URL   string `json:"url" yaml:"url"`
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
}

func linksFromCore(links []core.Link) []linkDoc {
	if len(links) == 0 {
		return nil
	}
	out := make([]linkDoc, 0, len(links))
	for _, link := range links {
		out = append(out, linkDoc{Kind: string(link.Kind), URL: link.URL, Title: link.Title})
	}
	return out
}

func linksToCore(links []linkDoc) []core.Link {
	if len(links) == 0 {
		return nil
	}
	out := make([]core.Link, 0, len(links))
	for _, link := range links {
		out = append(out, core.Link{Kind: core.LinkKind(link.Kind), URL: link.URL, Title: link.Title})
	}
	return out
}

// --- task ---

type taskFrontmatter struct {
	ID                 string        `yaml:"id"`
	Kind               string        `yaml:"kind"`
	Title              string        `yaml:"title"`
	Status             string        `yaml:"status"`
	Project            string        `yaml:"project"`
	Repo               string        `yaml:"repo,omitempty"`
	Priority           int           `yaml:"priority,omitempty"`
	Labels             []string      `yaml:"labels,omitempty"`
	Deps               []string      `yaml:"deps,omitempty"`
	Assignee           *string       `yaml:"assignee,omitempty"`
	Groomed            bool          `yaml:"groomed,omitempty"`
	AcceptanceCriteria []string      `yaml:"acceptance_criteria,omitempty"`
	WaitingOn          []string      `yaml:"waiting_on,omitempty"`
	NotBefore          *time.Time    `yaml:"not_before,omitempty"`
	Snooze             *snoozeDoc    `yaml:"snooze,omitempty"`
	Milestone          *milestoneDoc `yaml:"milestone,omitempty"`
	CreatedAt          time.Time     `yaml:"created_at"`
	UpdatedAt          time.Time     `yaml:"updated_at"`
}

type snoozeDoc struct {
	Until      *time.Time `yaml:"until,omitempty"`
	UntilTask  *string    `yaml:"until_task,omitempty"`
	Indefinite bool       `yaml:"indefinite,omitempty"`
}

type milestoneDoc struct {
	TargetDate *time.Time `yaml:"target_date,omitempty"`
	ReleaseRef string     `yaml:"release_ref,omitempty"`
}

var taskKeys = keySet(
	"id", "kind", "title", "status", "project", "repo", "priority", "labels",
	"deps", "assignee", "groomed", "acceptance_criteria", "waiting_on",
	"not_before", "snooze", "milestone", "created_at", "updated_at",
)

func encodeTask(task core.Task, extra map[string]*yaml.Node) ([]byte, error) {
	fm := taskFrontmatter{
		ID:                 string(task.ID),
		Kind:               string(task.Kind),
		Title:              task.Title,
		Status:             string(task.Status),
		Project:            string(task.ProjectID),
		Repo:               task.Repo,
		Priority:           task.Priority,
		Labels:             task.Labels,
		Deps:               taskIDStrings(task.Deps),
		Groomed:            task.Groomed,
		AcceptanceCriteria: task.AcceptanceCriteria,
		WaitingOn:          actorIDStrings(task.WaitingOn),
		NotBefore:          task.NotBefore,
		CreatedAt:          task.CreatedAt,
		UpdatedAt:          task.UpdatedAt,
	}
	if task.AssigneeID != nil {
		id := string(*task.AssigneeID)
		fm.Assignee = &id
	}
	if task.Snooze != nil {
		fm.Snooze = snoozeFromCore(*task.Snooze)
	}
	if task.Milestone != nil {
		fm.Milestone = milestoneFromCore(*task.Milestone)
	}
	front, err := marshalFrontmatter(fm, extra)
	if err != nil {
		return nil, err
	}
	return composeDocument(front, task.Description, task.Notes), nil
}

func decodeTask(data []byte) (core.Task, map[string]*yaml.Node, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return core.Task{}, nil, err
	}
	var fm taskFrontmatter
	extra, err := decodeFrontmatter(front, &fm, taskKeys)
	if err != nil {
		return core.Task{}, nil, err
	}
	description, notes, err := parseBody(body)
	if err != nil {
		return core.Task{}, nil, err
	}

	task := core.Task{
		ID:                 core.TaskID(fm.ID),
		ProjectID:          core.ProjectID(fm.Project),
		Repo:               fm.Repo,
		Kind:               core.TaskKind(fm.Kind),
		Title:              fm.Title,
		Description:        description,
		Status:             core.TaskStatus(fm.Status),
		WaitingOn:          actorIDsFromStrings(fm.WaitingOn),
		Labels:             fm.Labels,
		Priority:           fm.Priority,
		Deps:               taskIDsFromStrings(fm.Deps),
		Notes:              notes,
		NotBefore:          fm.NotBefore,
		Groomed:            fm.Groomed,
		AcceptanceCriteria: fm.AcceptanceCriteria,
		CreatedAt:          fm.CreatedAt,
		UpdatedAt:          fm.UpdatedAt,
	}
	if fm.Assignee != nil {
		id := core.ActorID(*fm.Assignee)
		task.AssigneeID = &id
	}
	if fm.Snooze != nil {
		task.Snooze = snoozeToCore(*fm.Snooze)
	}
	if fm.Milestone != nil {
		task.Milestone = milestoneToCore(*fm.Milestone)
	}
	if err := task.Validate(); err != nil {
		return core.Task{}, nil, err
	}
	return task, extra, nil
}

func snoozeFromCore(s core.Snooze) *snoozeDoc {
	doc := &snoozeDoc{Indefinite: s.Indefinite, Until: s.Until}
	if s.UntilTask != nil {
		id := string(*s.UntilTask)
		doc.UntilTask = &id
	}
	return doc
}

func snoozeToCore(doc snoozeDoc) *core.Snooze {
	s := &core.Snooze{Indefinite: doc.Indefinite, Until: doc.Until}
	if doc.UntilTask != nil {
		id := core.TaskID(*doc.UntilTask)
		s.UntilTask = &id
	}
	return s
}

func milestoneFromCore(m core.MilestoneMeta) *milestoneDoc {
	return &milestoneDoc{TargetDate: m.TargetDate, ReleaseRef: m.ReleaseRef}
}

func milestoneToCore(doc milestoneDoc) *core.MilestoneMeta {
	return &core.MilestoneMeta{TargetDate: doc.TargetDate, ReleaseRef: doc.ReleaseRef}
}

// --- project ---

type projectFrontmatter struct {
	ID        string    `yaml:"id"`
	Name      string    `yaml:"name"`
	Repos     []repoDoc `yaml:"repos,omitempty"`
	Policy    policyDoc `yaml:"policy"`
	CreatedAt time.Time `yaml:"created_at,omitempty"`
	UpdatedAt time.Time `yaml:"updated_at,omitempty"`
}

type repoDoc struct {
	Name        string `yaml:"name"`
	URL         string `yaml:"url,omitempty"`
	Path        string `yaml:"path,omitempty"`
	Description string `yaml:"description,omitempty"`
}

type policyDoc struct {
	TaskStatuses      []string `yaml:"task_statuses"`
	MilestoneStatuses []string `yaml:"milestone_statuses"`
}

var projectKeys = keySet("id", "name", "repos", "policy", "created_at", "updated_at")

func encodeProject(project core.Project, extra map[string]*yaml.Node) ([]byte, error) {
	fm := projectFrontmatter{
		ID:        string(project.ID),
		Name:      project.Name,
		Policy:    policyFromCore(project.Policy),
		CreatedAt: project.CreatedAt,
		UpdatedAt: project.UpdatedAt,
	}
	for _, repo := range project.Repos {
		fm.Repos = append(fm.Repos, repoDoc{Name: repo.Name, URL: repo.URL, Path: repo.Path, Description: repo.Description})
	}
	front, err := marshalFrontmatter(fm, extra)
	if err != nil {
		return nil, err
	}
	return composeDocument(front, project.Description, nil), nil
}

func decodeProject(data []byte) (core.Project, map[string]*yaml.Node, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return core.Project{}, nil, err
	}
	var fm projectFrontmatter
	extra, err := decodeFrontmatter(front, &fm, projectKeys)
	if err != nil {
		return core.Project{}, nil, err
	}
	description, _, err := parseBody(body)
	if err != nil {
		return core.Project{}, nil, err
	}
	project := core.Project{
		ID:          core.ProjectID(fm.ID),
		Name:        fm.Name,
		Description: description,
		Policy:      policyToCore(fm.Policy),
		CreatedAt:   fm.CreatedAt,
		UpdatedAt:   fm.UpdatedAt,
	}
	for _, repo := range fm.Repos {
		project.Repos = append(project.Repos, core.Repository{Name: repo.Name, URL: repo.URL, Path: repo.Path, Description: repo.Description})
	}
	if err := project.Validate(); err != nil {
		return core.Project{}, nil, err
	}
	return project, extra, nil
}

func policyFromCore(policy core.ResolutionPolicy) policyDoc {
	doc := policyDoc{TaskStatuses: statusStrings(policy.TaskStatuses), MilestoneStatuses: statusStrings(policy.MilestoneStatuses)}
	return doc
}

func policyToCore(doc policyDoc) core.ResolutionPolicy {
	policy := core.ResolutionPolicy{TaskStatuses: statusesFromStrings(doc.TaskStatuses), MilestoneStatuses: statusesFromStrings(doc.MilestoneStatuses)}
	if len(policy.TaskStatuses) == 0 && len(policy.MilestoneStatuses) == 0 {
		return core.DefaultResolutionPolicy()
	}
	return policy
}

// --- actor ---

type actorFrontmatter struct {
	ID        string    `yaml:"id"`
	Kind      string    `yaml:"kind"`
	Name      string    `yaml:"name"`
	Active    bool      `yaml:"active,omitempty"`
	CreatedAt time.Time `yaml:"created_at,omitempty"`
}

var actorKeys = keySet("id", "kind", "name", "active", "created_at")

func encodeActor(actor core.Actor, extra map[string]*yaml.Node) ([]byte, error) {
	fm := actorFrontmatter{ID: string(actor.ID), Kind: string(actor.Kind), Name: actor.Name, Active: actor.Active, CreatedAt: actor.CreatedAt}
	front, err := marshalFrontmatter(fm, extra)
	if err != nil {
		return nil, err
	}
	return composeDocument(front, "", nil), nil
}

func decodeActor(data []byte) (core.Actor, map[string]*yaml.Node, error) {
	front, _, err := splitFrontmatter(data)
	if err != nil {
		return core.Actor{}, nil, err
	}
	var fm actorFrontmatter
	extra, err := decodeFrontmatter(front, &fm, actorKeys)
	if err != nil {
		return core.Actor{}, nil, err
	}
	actor := core.Actor{ID: core.ActorID(fm.ID), Kind: core.ActorKind(fm.Kind), Name: fm.Name, Active: fm.Active, CreatedAt: fm.CreatedAt}
	if err := actor.Validate(); err != nil {
		return core.Actor{}, nil, err
	}
	return actor, extra, nil
}

// --- artifact ---

type artifactFrontmatter struct {
	ID        string    `yaml:"id"`
	Project   string    `yaml:"project"`
	Task      *string   `yaml:"task,omitempty"`
	Kind      string    `yaml:"kind"`
	Title     string    `yaml:"title"`
	Brief     string    `yaml:"brief,omitempty"`
	Path      string    `yaml:"path,omitempty"`
	Links     []linkDoc `yaml:"links,omitempty"`
	CreatedAt time.Time `yaml:"created_at,omitempty"`
	UpdatedAt time.Time `yaml:"updated_at,omitempty"`
}

var artifactKeys = keySet("id", "project", "task", "kind", "title", "brief", "path", "links", "created_at", "updated_at")

func encodeArtifact(artifact core.Artifact, extra map[string]*yaml.Node) ([]byte, error) {
	fm := artifactFrontmatter{
		ID:        string(artifact.ID),
		Project:   string(artifact.ProjectID),
		Kind:      string(artifact.Kind),
		Title:     artifact.Title,
		Brief:     artifact.Brief,
		Path:      artifact.Path,
		Links:     linksFromCore(artifact.Links),
		CreatedAt: artifact.CreatedAt,
		UpdatedAt: artifact.UpdatedAt,
	}
	if artifact.TaskID != nil {
		id := string(*artifact.TaskID)
		fm.Task = &id
	}
	front, err := marshalFrontmatter(fm, extra)
	if err != nil {
		return nil, err
	}
	return composeDocument(front, artifact.Body, nil), nil
}

func decodeArtifact(data []byte) (core.Artifact, map[string]*yaml.Node, error) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return core.Artifact{}, nil, err
	}
	var fm artifactFrontmatter
	extra, err := decodeFrontmatter(front, &fm, artifactKeys)
	if err != nil {
		return core.Artifact{}, nil, err
	}
	artifactBody, _, err := parseBody(body)
	if err != nil {
		return core.Artifact{}, nil, err
	}
	artifact := core.Artifact{
		ID:        core.ArtifactID(fm.ID),
		ProjectID: core.ProjectID(fm.Project),
		Kind:      core.ArtifactKind(fm.Kind),
		Title:     fm.Title,
		Brief:     fm.Brief,
		Body:      artifactBody,
		Path:      fm.Path,
		Links:     linksToCore(fm.Links),
		CreatedAt: fm.CreatedAt,
		UpdatedAt: fm.UpdatedAt,
	}
	if fm.Task != nil {
		id := core.TaskID(*fm.Task)
		artifact.TaskID = &id
	}
	if err := artifact.Validate(); err != nil {
		return core.Artifact{}, nil, err
	}
	return artifact, extra, nil
}

// --- notes ---

type noteMetadata struct {
	ID        string     `json:"id"`
	Author    string     `json:"author,omitempty"`
	CreatedAt *time.Time `json:"created_at,omitempty"`
	System    bool       `json:"system,omitempty"`
	Links     []linkDoc  `json:"links,omitempty"`
}

func noteMetadataFrom(note core.Note) noteMetadata {
	meta := noteMetadata{
		ID:     note.ID,
		Author: string(note.Author),
		System: note.System,
		Links:  linksFromCore(note.Links),
	}
	if !note.CreatedAt.IsZero() {
		created := note.CreatedAt
		meta.CreatedAt = &created
	}
	return meta
}

func (meta noteMetadata) note(body string) core.Note {
	note := core.Note{
		ID:     meta.ID,
		Author: core.ActorID(meta.Author),
		Body:   body,
		Links:  linksToCore(meta.Links),
		System: meta.System,
	}
	if meta.CreatedAt != nil {
		note.CreatedAt = *meta.CreatedAt
	}
	return note
}

// --- shared encoding helpers ---

func composeDocument(frontmatter []byte, description string, notes []core.Note) []byte {
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(frontmatter)
	b.WriteString("---\n")

	body := escapeBody(strings.TrimRight(description, "\n"))
	if len(notes) > 0 {
		body += "\n\n" + notesMarker + "\n"
		for _, note := range notes {
			meta, _ := json.Marshal(noteMetadataFrom(note))
			body += "\n" + notePrefix + string(meta) + noteSuffix + "\n"
			body += escapeBody(strings.TrimRight(note.Body, "\n")) + "\n"
		}
	}
	document := b.String() + "\n" + body
	document = strings.TrimRight(document, "\n") + "\n"
	return []byte(document)
}

// splitFrontmatter separates a document's YAML frontmatter from its body. The
// frontmatter is delimited by a leading `---` line and the next `---` line.
func splitFrontmatter(data []byte) (frontmatter, body []byte, err error) {
	text := string(data)
	if !strings.HasPrefix(text, "---\n") {
		return nil, nil, fmt.Errorf("missing opening frontmatter delimiter")
	}
	rest := text[len("---\n"):]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		if strings.HasSuffix(rest, "\n---") {
			return []byte(rest[:len(rest)-len("\n---")+1]), nil, nil
		}
		return nil, nil, fmt.Errorf("unterminated frontmatter")
	}
	return []byte(rest[:end+1]), []byte(rest[end+len("\n---\n"):]), nil
}

func marshalFrontmatter(value any, extra map[string]*yaml.Node) ([]byte, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, fmt.Errorf("encode frontmatter: %w", err)
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("encode frontmatter: unexpected node kind %d", node.Kind)
	}
	appendExtraKeys(&node, extra)
	var buf bytes.Buffer
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(&node); err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("marshal frontmatter: %w", err)
	}
	return buf.Bytes(), nil
}

// appendExtraKeys appends unknown frontmatter keys after the schema keys, in
// sorted order, so a hand-added key survives a rewrite at a stable position.
func appendExtraKeys(node *yaml.Node, extra map[string]*yaml.Node) {
	if len(extra) == 0 {
		return
	}
	known := make(map[string]bool, len(node.Content)/2)
	for i := 0; i+1 < len(node.Content); i += 2 {
		known[node.Content[i].Value] = true
	}
	keys := make([]string, 0, len(extra))
	for key := range extra {
		if !known[key] {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		node.Content = append(node.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			cloneNode(extra[key]))
	}
}

func decodeFrontmatter(frontmatter []byte, out any, known map[string]bool) (map[string]*yaml.Node, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(frontmatter, &root); err != nil {
		return nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	node := &root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	if node.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("frontmatter is not a mapping")
	}
	if err := node.Decode(out); err != nil {
		return nil, fmt.Errorf("decode frontmatter: %w", err)
	}
	extra := make(map[string]*yaml.Node)
	for i := 0; i+1 < len(node.Content); i += 2 {
		key := node.Content[i].Value
		if known[key] {
			continue
		}
		extra[key] = cloneNode(node.Content[i+1])
	}
	if len(extra) == 0 {
		return nil, nil
	}
	return extra, nil
}

// parseBody splits a markdown body into its description and any note sections.
func parseBody(body []byte) (string, []core.Note, error) {
	lines := strings.Split(string(body), "\n")
	separator := -1
	for i, line := range lines {
		if line == notesMarker {
			separator = i
			break
		}
	}
	descPart, notesPart := string(body), ""
	if separator >= 0 {
		descPart = strings.Join(lines[:separator], "\n")
		notesPart = strings.Join(lines[separator+1:], "\n")
	}
	descPart = strings.TrimPrefix(descPart, "\n")
	description := unescapeBody(strings.TrimRight(descPart, "\n"))
	notes, err := parseNotes(notesPart)
	if err != nil {
		return "", nil, err
	}
	return description, notes, nil
}

func parseNotes(part string) ([]core.Note, error) {
	lines := strings.Split(part, "\n")
	var notes []core.Note
	for i := 0; i < len(lines); {
		line := lines[i]
		if !isNoteHeader(line) {
			i++
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(line, notePrefix), noteSuffix)
		var meta noteMetadata
		if err := json.Unmarshal([]byte(raw), &meta); err != nil {
			return nil, fmt.Errorf("note metadata: %w", err)
		}
		i++
		var bodyLines []string
		for i < len(lines) && !isNoteHeader(lines[i]) {
			bodyLines = append(bodyLines, lines[i])
			i++
		}
		notes = append(notes, meta.note(unescapeBody(strings.TrimRight(strings.Join(bodyLines, "\n"), "\n"))))
	}
	return notes, nil
}

func isNoteHeader(line string) bool {
	return strings.HasPrefix(line, notePrefix) && strings.HasSuffix(line, noteSuffix)
}

func isDelimiterLine(line string) bool {
	return line == notesMarker || isNoteHeader(line)
}

// escapeBody guards body lines that would otherwise be read as structural
// markup: the notes separator, a note header, or any line already starting with
// the escape backslash. unescapeBody is its exact inverse, so a description or
// note body round-trips even when it contains the delimiters verbatim.
func escapeBody(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if isDelimiterLine(line) || strings.HasPrefix(line, `\`) {
			lines[i] = `\` + line
		}
	}
	return strings.Join(lines, "\n")
}

func unescapeBody(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, `\`) {
			lines[i] = line[1:]
		}
	}
	return strings.Join(lines, "\n")
}

func cloneNode(node *yaml.Node) *yaml.Node {
	if node == nil {
		return nil
	}
	clone := *node
	clone.Content = make([]*yaml.Node, len(node.Content))
	for i, child := range node.Content {
		clone.Content[i] = cloneNode(child)
	}
	return &clone
}

// --- small conversions ---

func keySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

func taskIDStrings(ids []core.TaskID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func taskIDsFromStrings(ids []string) []core.TaskID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]core.TaskID, 0, len(ids))
	for _, id := range ids {
		out = append(out, core.TaskID(id))
	}
	return out
}

func actorIDStrings(ids []core.ActorID) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, string(id))
	}
	return out
}

func actorIDsFromStrings(ids []string) []core.ActorID {
	if len(ids) == 0 {
		return nil
	}
	out := make([]core.ActorID, 0, len(ids))
	for _, id := range ids {
		out = append(out, core.ActorID(id))
	}
	return out
}

func statusStrings(statuses []core.TaskStatus) []string {
	if len(statuses) == 0 {
		return nil
	}
	out := make([]string, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, string(status))
	}
	return out
}

func statusesFromStrings(statuses []string) []core.TaskStatus {
	if len(statuses) == 0 {
		return nil
	}
	out := make([]core.TaskStatus, 0, len(statuses))
	for _, status := range statuses {
		out = append(out, core.TaskStatus(status))
	}
	return out
}
