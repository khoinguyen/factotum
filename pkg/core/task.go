package core

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

type TicketKind string

const (
	KindTask      TicketKind = "task"
	KindMilestone TicketKind = "milestone"
	// KindIdea is a non-executable capture: an unrefined thought recorded for
	// later grooming. Ideas never appear in readiness or ranking, are not
	// assignable, and are promoted into a task rather than executed in place.
	KindIdea TicketKind = "idea"
	// KindBug is a non-executable capture: a defect recorded by a human for
	// later triage. Like an idea it never appears in readiness or ranking, is
	// not assignable, and is promoted into a linked task rather than executed
	// in place; the refinement verb is "triage" rather than "groom".
	KindBug TicketKind = "bug"
)

func (k TicketKind) Valid() bool {
	return k == KindTask || k == KindMilestone || k == KindIdea || k == KindBug
}

// KindFamily groups kinds by their role in the workflow: human captures that
// are refined into work, the executable kind an agent runs, and gates that
// hold dependents until a milestone resolves.
type KindFamily string

const (
	// FamilyCapture is a human capture (idea, bug): recorded for refinement,
	// never executed in place.
	FamilyCapture KindFamily = "capture"
	// FamilyExecutable is the agent-facing executable kind (task).
	FamilyExecutable KindFamily = "executable"
	// FamilyGate is a milestone that gates its dependents.
	FamilyGate KindFamily = "gate"
)

// Family reports the kind's workflow family, or "" for an unknown kind.
func (k TicketKind) Family() KindFamily {
	switch k {
	case KindTask:
		return FamilyExecutable
	case KindMilestone:
		return FamilyGate
	case KindIdea, KindBug:
		return FamilyCapture
	default:
		return ""
	}
}

// Executable reports whether a kind participates in execution: readiness,
// ranking, and dependency resolution. Captures (ideas, bugs) are not work.
func (k TicketKind) Executable() bool {
	return k == KindTask || k == KindMilestone
}

// CapturedByHuman reports whether a kind is a human capture that is refined
// into work rather than executed in place.
func (k TicketKind) CapturedByHuman() bool {
	return k.Family() == FamilyCapture
}

// RefineVerb names the command a human uses to refine a capture into work, or
// "" for kinds that are not refined (they are executed or gate directly).
func (k TicketKind) RefineVerb() string {
	switch k {
	case KindIdea:
		return "groom"
	case KindBug:
		return "triage"
	default:
		return ""
	}
}

// AllowsStatus reports whether a kind may hold a status. Captures are refined
// into work, so only todo, done, and cancelled apply; other kinds accept any
// valid status.
func (k TicketKind) AllowsStatus(s TicketStatus) bool {
	if k.CapturedByHuman() {
		return s == StatusTodo || s == StatusDone || s == StatusCancelled
	}
	return s.Valid()
}

type TicketStatus string

const (
	StatusTodo           TicketStatus = "todo"
	StatusInProgress     TicketStatus = "in_progress"
	StatusBlocked        TicketStatus = "blocked"
	StatusReadyForReview TicketStatus = "ready_for_review"
	StatusDone           TicketStatus = "done"
	StatusCancelled      TicketStatus = "cancelled"
)

func (s TicketStatus) Valid() bool {
	switch s {
	case StatusTodo, StatusInProgress, StatusBlocked, StatusReadyForReview, StatusDone, StatusCancelled:
		return true
	default:
		return false
	}
}

type LinkKind string

const (
	LinkPR    LinkKind = "pr"
	LinkIssue LinkKind = "issue"
	LinkDoc   LinkKind = "doc"
	LinkURL   LinkKind = "url"
)

func (k LinkKind) Valid() bool {
	switch k {
	case LinkPR, LinkIssue, LinkDoc, LinkURL:
		return true
	default:
		return false
	}
}

type Link struct {
	Kind  LinkKind
	URL   string
	Title string
}

func (l Link) Validate() error {
	if !l.Kind.Valid() {
		return fmt.Errorf("%w: unknown link kind %q", ErrInvalid, l.Kind)
	}
	if strings.TrimSpace(l.URL) == "" {
		return fmt.Errorf("%w: link url is required", ErrInvalid)
	}
	if l.Title == "" {
		return nil
	}
	// A title renders inline after its url on a single line (agent injection,
	// get/read), so a control rune would forge a section or break the line it
	// names, and a blank title would print a dangling " - ".
	if strings.TrimSpace(l.Title) == "" {
		return fmt.Errorf("%w: link title must not be blank", ErrInvalid)
	}
	if strings.IndexFunc(l.Title, unicode.IsControl) >= 0 {
		return fmt.Errorf("%w: link title must not contain control characters", ErrInvalid)
	}
	return nil
}

type Note struct {
	ID        string
	Author    ActorID
	Body      string
	Links     []Link
	CreatedAt time.Time
	// System marks a note ft generated (a triage or reassignment message) rather
	// than one a human or agent wrote. System notes stay visible on the task but
	// are excluded from search indexing as system noise.
	System bool
}

// GeneratedNoteFamily is one family of machine-written notes. Shape is the
// opening template the writer uses, with slots for the varying part: "<date>"
// is a date, "<n>" a number, "<ref>" a base or branch name, "<task>" a task id,
// "<url>" a URL, "<sha>" a commit, "<paren>" a parenthetical label, and
// "<label>" one of the QA run's machine markers (a verdict, a report kind, a task
// id, or a date). Meaning is the one line the backfill's --help shows beside it.
type GeneratedNoteFamily struct {
	Shape   string
	Meaning string
}

// machineNoteFamilies is the table of openings the machine-written notes use:
// the agent loop, the triage flow, the grooming flow, review, and QA write them.
// Notes ft writes itself set System at creation; these openings identify the
// ones written before the marker existed, when generated notes were simply typed
// with no author. Each entry is a family template, not a whole note: the text
// after the shape is the writer's summary. This slice is the single source of
// truth: the backfill predicate and the verb's help both read it, and the shape
// is compiled into the matcher, so a family cannot be documented by hand.
var machineNoteFamilies = []GeneratedNoteFamily{
	{Shape: "Triaged by agent:", Meaning: "the agent's triage decision"},
	{Shape: "Reassigned to you.", Meaning: "the loop handed the work back to you"},
	{Shape: "Needs human decision:", Meaning: "the loop is waiting on your decision"},
	{Shape: "Needs human triage:", Meaning: "the loop is waiting on your triage"},
	{Shape: "Needs human grooming:", Meaning: "the loop is waiting on your grooming"},
	{Shape: "Grooming <date> (advisory).", Meaning: "a grooming pass's advisory note"},
	{Shape: "Grooming <date> (session <n>):", Meaning: "a grooming session's split of the work"},
	{Shape: "Groomed by agent (auto):", Meaning: "the agent's automatic grooming pass"},
	{Shape: "Built (PR base <ref>):", Meaning: "the builder's record of what it built"},
	{Shape: "PR opened.", Meaning: "the builder announced a pull request"},
	{Shape: "PR opened (base <ref>)", Meaning: "the builder announced a pull request against a base"},
	{Shape: "PR #<n> opened", Meaning: "the builder announced a pull request"},
	{Shape: "PR #<n> open:", Meaning: "the builder announced a pull request"},
	{Shape: "PR #<n> open (", Meaning: "the builder announced a pull request"},
	{Shape: "PR #<n> open for review", Meaning: "the builder handed a pull request to review"},
	{Shape: "PR #<n> open on", Meaning: "the builder announced a pull request"},
	{Shape: "PR #<n> open and", Meaning: "the builder announced a pull request"},
	{Shape: "PR #<n> open;", Meaning: "the builder announced a pull request"},
	{Shape: "PR #<n>: <url>", Meaning: "the builder pointed at a pull request"},
	{Shape: "PR #<n> (branch <ref>)", Meaning: "the builder announced a pull request on a branch"},
	{Shape: "PR #<n> (base <ref>)", Meaning: "the builder announced a pull request against a base"},
	{Shape: "PR #<n> for review", Meaning: "the builder handed a pull request to review"},
	{Shape: "Opened PR #<n>", Meaning: "the builder announced a pull request"},
	{Shape: "PR: <url>", Meaning: "the builder pointed at a pull request"},
	{Shape: "PR link attached.", Meaning: "a pull-request link was attached to the task"},
	{Shape: "Ready for review.", Meaning: "the builder's handoff to review"},
	{Shape: "Review round <n>", Meaning: "a review round's findings and fixes"},
	{Shape: "Reviewer round <n>", Meaning: "a review round's findings and fixes"},
	{Shape: "Reviewer APPROVED", Meaning: "the reviewer's approval verdict"},
	{Shape: "Reviewer-<task> APPROVED", Meaning: "the reviewer's approval verdict"},
	{Shape: "Reviewer finding <n>", Meaning: "the reviewer's finding and its fix"},
	{Shape: "Review fix at <sha>", Meaning: "a fix pushed for a review finding"},
	{Shape: "Review fixes at <sha>", Meaning: "fixes pushed for review findings"},
	{Shape: "Residual watch item from PR #<n>", Meaning: "a residual the reviewer wants watched"},
	{Shape: "Residual watch items from PR #<n>", Meaning: "residuals the reviewer wants watched"},
	{Shape: "Follow-up from PR #<n>", Meaning: "a follow-up filed from a PR's review"},
	{Shape: "Follow-up (<paren>) from PR #<n>", Meaning: "a follow-up filed from a PR's review"},
	{Shape: "Follow-ups from PR #<n>", Meaning: "follow-ups filed from a PR's review"},
	{Shape: "Follow-ups (<paren>) from PR #<n>", Meaning: "follow-ups filed from a PR's review"},
	{Shape: "Follow-up filed as <task>", Meaning: "a follow-up filed as a task"},
	{Shape: "Follow-up filed: <task>", Meaning: "a follow-up filed as a task"},
	{Shape: "Follow-ups filed: <task>", Meaning: "follow-ups filed as tasks"},
	{Shape: "Follow-up: <task>", Meaning: "a follow-up filed as a task"},
	{Shape: "Filed from PR #<n>", Meaning: "work filed from a PR's review"},
	{Shape: "Filed from review of PR #<n>", Meaning: "work filed from a PR's review"},
	{Shape: "Nits from PR #<n>", Meaning: "nits recorded from a PR's review"},
	{Shape: "Watch item from PR #<n>", Meaning: "a watch item recorded from a PR's review"},
	{Shape: "INFO residuals (PR #<n>", Meaning: "informational residuals from a PR's review"},
	{Shape: "INFO/LOW residuals (PR #<n>", Meaning: "informational and low residuals from a PR's review"},
	{Shape: "INFO from PR #<n>", Meaning: "an informational finding from a PR's review"},
	{Shape: "INFO cleanup candidate from PR #<n>", Meaning: "a cleanup candidate from a PR's review"},
	{Shape: "Handed to reviewer: PR #<n>", Meaning: "the builder's handoff to review"},
	{Shape: "PR #<n> ready for review.", Meaning: "the builder's handoff to review"},
	{Shape: "PR link recorded for review.", Meaning: "a pull-request link was recorded"},
	{Shape: "QA <label>", Meaning: "a QA run report or verdict (report, PASS/pass/fail, sweep, task id, date)"},
	{Shape: "Merged as PR #<n>", Meaning: "the pull request landed"},
	{Shape: "Landed as PR #<n>", Meaning: "the pull request landed"},
	{Shape: "Re-captured as idea <task>", Meaning: "a task re-captured as an idea"},
	{Shape: "Re-captured from task <task>", Meaning: "a task re-captured from another task"},
	{Shape: "Origin: Khoi's fleet-of-thoughts brainstorm", Meaning: "a capture's recorded origin"},
	{Shape: "Folded into one app idea <task>", Meaning: "a capture folded into another idea"},
	{Shape: "Folded from <task>", Meaning: "a capture folded from another idea"},
	{Shape: "Reconciled by chief", Meaning: "the chief's reconciliation note"},
}

var shapeSlotRE = regexp.MustCompile(`<[a-z]+>`)

var shapeSlots = map[string]string{
	"<date>":  `\d{4}-\d{2}-\d{2}`,
	"<n>":     `\d+`,
	"<ref>":   `[A-Za-z0-9._/-]+`,
	"<task>":  `t-[a-z0-9]+`,
	"<url>":   `https?://[^\s]+`,
	"<sha>":   `[0-9a-f]{7,40}`,
	"<paren>": `[^)]+`,
	"<label>": `(?:\(qa-t-[a-z0-9]+\)|\(continued\)|report|PASS|FAIL|pass|fail|verdict|sweep|re-verified|observation|evidence|follow-up|re-run|retest|continued|F[0-9]+|t-[a-z0-9]+|qa-t-[a-z0-9]+|\d{4}-\d{2}-\d{2})`,
}

// machineNoteMatchers is compiled once from the shapes, so the table stays the
// only place a family is written.
var machineNoteMatchers = func() []*regexp.Regexp {
	matchers := make([]*regexp.Regexp, len(machineNoteFamilies))
	for i, family := range machineNoteFamilies {
		matchers[i] = compileShape(family.Shape)
	}
	return matchers
}()

// shapePart is one piece of a family shape: a literal when name is empty, a slot
// when it is set.
type shapePart struct {
	name string
	text string
}

func shapeParts(shape string) []shapePart {
	var parts []shapePart
	last := 0
	for _, loc := range shapeSlotRE.FindAllStringIndex(shape, -1) {
		if loc[0] > last {
			parts = append(parts, shapePart{text: shape[last:loc[0]]})
		}
		parts = append(parts, shapePart{name: shape[loc[0]:loc[1]]})
		last = loc[1]
	}
	if last < len(shape) {
		parts = append(parts, shapePart{text: shape[last:]})
	}
	return parts
}

// compileShape turns a shape into the anchored opening matcher. Slots become
// permissive patterns and literals are quoted, so a shape such as "PR opened."
// cannot turn its period into a wildcard.
func compileShape(shape string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for _, part := range shapeParts(shape) {
		if part.name == "" {
			b.WriteString(regexp.QuoteMeta(part.text))
			continue
		}
		pattern, ok := shapeSlots[part.name]
		if !ok {
			panic("core: unknown generated-note shape slot " + part.name)
		}
		b.WriteString(pattern)
	}
	return regexp.MustCompile(b.String())
}

// GeneratedNoteFamilies returns the recognized generated-note families, so
// documentation renders from the same table the predicate uses.
func GeneratedNoteFamilies() []GeneratedNoteFamily {
	return append([]GeneratedNoteFamily(nil), machineNoteFamilies...)
}

// IsGeneratedNote reports whether n is an unmarked machine-written note: it
// carries no author and opens with a known generated-note family. An authored
// note is never generated, whatever its body, so a backfill driven by this
// predicate cannot touch a note a human or agent wrote.
func IsGeneratedNote(n Note) bool {
	if n.System || n.Author != "" {
		return false
	}
	body := strings.TrimSpace(n.Body)
	for _, matcher := range machineNoteMatchers {
		if matcher.MatchString(body) {
			return true
		}
	}
	return false
}

func (n Note) Validate() error {
	if n.ID == "" {
		return fmt.Errorf("%w: note id is required", ErrInvalid)
	}
	if strings.TrimSpace(n.Body) == "" {
		return fmt.Errorf("%w: note body is required", ErrInvalid)
	}
	for _, l := range n.Links {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("note link: %w", err)
		}
	}
	return nil
}

type MilestoneMeta struct {
	TargetDate *time.Time
	ReleaseRef string
}

// Snooze parks a task out of ranking until a condition passes: a date, another
// task resolving, or indefinitely (until explicitly unsnoozed). Exactly one
// condition is set.
type Snooze struct {
	Until      *time.Time
	UntilTask  *TicketID
	Indefinite bool
}

func (s Snooze) Validate() error {
	conditions := 0
	if s.Until != nil {
		conditions++
	}
	if s.UntilTask != nil {
		conditions++
	}
	if s.Indefinite {
		conditions++
	}
	if conditions != 1 {
		return fmt.Errorf("%w: snooze needs exactly one of until, until_task, or indefinite", ErrInvalid)
	}
	return nil
}

// Describe renders a snooze condition for humans.
func (s Snooze) Describe() string {
	switch {
	case s.Indefinite:
		return "indefinitely"
	case s.Until != nil:
		return "until " + s.Until.UTC().Format(time.RFC3339)
	case s.UntilTask != nil:
		return "until " + string(*s.UntilTask)
	default:
		return ""
	}
}

type Ticket struct {
	ID          TicketID
	ProjectID   ProjectID
	Repo        string
	Kind        TicketKind
	Title       string
	Description string
	Status      TicketStatus
	AssigneeID  *ActorID
	WaitingOn   []ActorID
	Labels      []string
	Priority    int
	Deps        []TicketID
	// OriginArtifact is the document artifact a capture was groomed from, when
	// its provenance is a prompt rather than another capture (a web-captured
	// prompt is stored as a doc artifact; a grooming agent reads it and records
	// each capture it produces against it). It is provenance only: unlike Deps
	// it never gates the ticket.
	OriginArtifact *ArtifactID
	Notes          []Note
	Milestone      *MilestoneMeta
	NotBefore      *time.Time
	Snooze         *Snooze
	// Groomed marks a task as refined enough for an agent to execute on its
	// own: scope, approach, and acceptance criteria are decided. It is set
	// explicitly, never inferred from the "groomed" label or from the presence
	// of acceptance criteria, and it gates agent readiness.
	Groomed bool
	// AcceptanceCriteria are the observable conditions that define done. A
	// groomed task must carry at least one; an ungroomed task may carry a
	// partial set while it is still being refined.
	AcceptanceCriteria []string
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// ReadyAt reports whether the task's not_before constraint has elapsed at now.
// A task with no constraint is always ready.
func (t Ticket) ReadyAt(now time.Time) bool {
	return t.NotBefore == nil || !t.NotBefore.After(now)
}

func (t Ticket) Validate() error {
	if t.ID == "" {
		return fmt.Errorf("%w: task id is required", ErrInvalid)
	}
	if t.ProjectID == "" {
		return fmt.Errorf("%w: task project id is required", ErrInvalid)
	}
	if strings.TrimSpace(t.Title) == "" {
		return fmt.Errorf("%w: task title is required", ErrInvalid)
	}
	if !t.Kind.Valid() {
		return fmt.Errorf("%w: unknown task kind %q", ErrInvalid, t.Kind)
	}
	if !t.Status.Valid() {
		return fmt.Errorf("%w: unknown task status %q", ErrInvalid, t.Status)
	}
	if !t.Kind.AllowsStatus(t.Status) {
		return fmt.Errorf("%w: kind %q does not allow status %q", ErrInvalid, t.Kind, t.Status)
	}
	if t.Kind.CapturedByHuman() && t.AssigneeID != nil {
		return fmt.Errorf("%w: %s %s is not assignable", ErrInvalid, t.Kind, t.ID)
	}
	if t.Kind.CapturedByHuman() && t.Groomed {
		return fmt.Errorf("%w: %s %s is not groomable", ErrInvalid, t.Kind, t.ID)
	}
	if t.OriginArtifact != nil && !t.Kind.CapturedByHuman() {
		return fmt.Errorf("%w: %s %s cannot carry an origin artifact; only captures are groomed from a prompt", ErrInvalid, t.Kind, t.ID)
	}
	for _, criterion := range t.AcceptanceCriteria {
		if strings.TrimSpace(criterion) == "" {
			return fmt.Errorf("%w: task %s has an empty acceptance criterion", ErrInvalid, t.ID)
		}
	}
	if t.Groomed && len(t.AcceptanceCriteria) == 0 {
		return fmt.Errorf("%w: groomed task %s requires at least one acceptance criterion", ErrInvalid, t.ID)
	}

	seen := make(map[TicketID]struct{}, len(t.Deps))
	for _, dep := range t.Deps {
		if dep == "" {
			return fmt.Errorf("%w: task %s has an empty dependency", ErrInvalid, t.ID)
		}
		if dep == t.ID {
			return fmt.Errorf("%w: task %s cannot depend on itself", ErrInvalid, t.ID)
		}
		if _, ok := seen[dep]; ok {
			return fmt.Errorf("%w: task %s has duplicate dependency %s", ErrInvalid, t.ID, dep)
		}
		seen[dep] = struct{}{}
	}

	for _, n := range t.Notes {
		if err := n.Validate(); err != nil {
			return fmt.Errorf("task note: %w", err)
		}
	}

	if t.Snooze != nil {
		if err := t.Snooze.Validate(); err != nil {
			return fmt.Errorf("task snooze: %w", err)
		}
	}

	return nil
}

func (t Ticket) IsMilestone() bool {
	return t.Kind == KindMilestone
}

func (t Ticket) IsIdea() bool {
	return t.Kind == KindIdea
}

func (t Ticket) IsBug() bool {
	return t.Kind == KindBug
}

func (t Ticket) Resolves(policy ResolutionPolicy) bool {
	return policy.Resolves(t.Kind, t.Status)
}
