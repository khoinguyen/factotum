package core

import (
	"testing"
	"time"
)

func TestSnoozeValidate(t *testing.T) {
	until := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	taskID := TicketID("t-1")
	tests := []struct {
		name    string
		snooze  Snooze
		wantErr bool
	}{
		{"until", Snooze{Until: &until}, false},
		{"until task", Snooze{UntilTask: &taskID}, false},
		{"indefinite", Snooze{Indefinite: true}, false},
		{"empty", Snooze{}, true},
		{"two conditions", Snooze{Until: &until, Indefinite: true}, true},
	}
	for _, tt := range tests {
		if err := tt.snooze.Validate(); (err != nil) != tt.wantErr {
			t.Errorf("%s: Validate() error = %v, wantErr %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestTaskReadyAt(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	tests := []struct {
		name      string
		notBefore *time.Time
		want      bool
	}{
		{"no constraint", nil, true},
		{"past", &past, true},
		{"now", &now, true},
		{"future", &future, false},
	}
	for _, tt := range tests {
		task := Ticket{NotBefore: tt.notBefore}
		if got := task.ReadyAt(now); got != tt.want {
			t.Errorf("%s: ReadyAt() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestTaskKindValid(t *testing.T) {
	tests := []struct {
		kind TicketKind
		want bool
	}{
		{KindTask, true},
		{KindMilestone, true},
		{KindIdea, true},
		{KindBug, true},
		{"epic", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.kind.Valid(); got != tt.want {
			t.Errorf("TicketKind(%q).Valid() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestTaskKindExecutable(t *testing.T) {
	tests := []struct {
		kind TicketKind
		want bool
	}{
		{KindTask, true},
		{KindMilestone, true},
		{KindIdea, false},
		{KindBug, false},
		{"epic", false},
	}
	for _, tt := range tests {
		if got := tt.kind.Executable(); got != tt.want {
			t.Errorf("TicketKind(%q).Executable() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestTaskKindFamily(t *testing.T) {
	tests := []struct {
		kind TicketKind
		want KindFamily
	}{
		{KindTask, FamilyExecutable},
		{KindMilestone, FamilyGate},
		{KindIdea, FamilyCapture},
		{KindBug, FamilyCapture},
		{"epic", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := tt.kind.Family(); got != tt.want {
			t.Errorf("TicketKind(%q).Family() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestTaskKindCapturedByHuman(t *testing.T) {
	tests := []struct {
		kind TicketKind
		want bool
	}{
		{KindIdea, true},
		{KindBug, true},
		{KindTask, false},
		{KindMilestone, false},
		{"epic", false},
	}
	for _, tt := range tests {
		if got := tt.kind.CapturedByHuman(); got != tt.want {
			t.Errorf("TicketKind(%q).CapturedByHuman() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestTaskKindRefineVerb(t *testing.T) {
	tests := []struct {
		kind TicketKind
		want string
	}{
		{KindIdea, "groom"},
		{KindBug, "triage"},
		{KindTask, ""},
		{KindMilestone, ""},
		{"epic", ""},
	}
	for _, tt := range tests {
		if got := tt.kind.RefineVerb(); got != tt.want {
			t.Errorf("TicketKind(%q).RefineVerb() = %q, want %q", tt.kind, got, tt.want)
		}
	}
}

func TestTaskKindAllowsStatus(t *testing.T) {
	tests := []struct {
		kind   TicketKind
		status TicketStatus
		want   bool
	}{
		{KindTask, StatusInProgress, true},
		{KindMilestone, StatusInProgress, true},
		{KindIdea, StatusTodo, true},
		{KindIdea, StatusDone, true},
		{KindIdea, StatusCancelled, true},
		{KindIdea, StatusInProgress, false},
		{KindIdea, StatusBlocked, false},
		{KindIdea, StatusReadyForReview, false},
		{KindIdea, "archived", false},
		{KindBug, StatusTodo, true},
		{KindBug, StatusDone, true},
		{KindBug, StatusCancelled, true},
		{KindBug, StatusInProgress, false},
		{KindBug, StatusBlocked, false},
		{KindBug, StatusReadyForReview, false},
	}
	for _, tt := range tests {
		if got := tt.kind.AllowsStatus(tt.status); got != tt.want {
			t.Errorf("TicketKind(%q).AllowsStatus(%q) = %v, want %v", tt.kind, tt.status, got, tt.want)
		}
	}
}

func TestTaskIsIdea(t *testing.T) {
	if !(Ticket{Kind: KindIdea}).IsIdea() {
		t.Fatal("idea task should report IsIdea")
	}
	if (Ticket{Kind: KindTask}).IsIdea() {
		t.Fatal("plain task should not report IsIdea")
	}
}

func TestTaskIsBug(t *testing.T) {
	if !(Ticket{Kind: KindBug}).IsBug() {
		t.Fatal("bug task should report IsBug")
	}
	if (Ticket{Kind: KindTask}).IsBug() {
		t.Fatal("plain task should not report IsBug")
	}
}

func TestTaskStatusValid(t *testing.T) {
	tests := []struct {
		status TicketStatus
		want   bool
	}{
		{StatusTodo, true},
		{StatusInProgress, true},
		{StatusBlocked, true},
		{StatusReadyForReview, true},
		{StatusDone, true},
		{StatusCancelled, true},
		{"archived", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.status.Valid(); got != tt.want {
			t.Errorf("TicketStatus(%q).Valid() = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestTaskValidate(t *testing.T) {
	base := Ticket{ID: "t-1", ProjectID: "prj-1", Kind: KindTask, Title: "Do the thing", Status: StatusTodo}

	tests := []struct {
		name    string
		mutate  func(*Ticket)
		wantErr bool
	}{
		{"valid", func(*Ticket) {}, false},
		{"missing id", func(t *Ticket) { t.ID = "" }, true},
		{"missing project", func(t *Ticket) { t.ProjectID = "" }, true},
		{"blank title", func(t *Ticket) { t.Title = "   " }, true},
		{"unknown kind", func(t *Ticket) { t.Kind = "epic" }, true},
		{"unknown status", func(t *Ticket) { t.Status = "archived" }, true},
		{"self dependency", func(t *Ticket) { t.Deps = []TicketID{"t-1"} }, true},
		{"empty dependency", func(t *Ticket) { t.Deps = []TicketID{""} }, true},
		{"duplicate dependency", func(t *Ticket) { t.Deps = []TicketID{"t-2", "t-2"} }, true},
		{"valid dependencies", func(t *Ticket) { t.Deps = []TicketID{"t-2", "t-3"} }, false},
		{"idea todo", func(t *Ticket) { t.Kind = KindIdea }, false},
		{"idea in progress", func(t *Ticket) { t.Kind = KindIdea; t.Status = StatusInProgress }, true},
		{"idea assigned", func(t *Ticket) { t.Kind = KindIdea; id := ActorID("act-1"); t.AssigneeID = &id }, true},
		{"groomed without criteria", func(t *Ticket) { t.Groomed = true }, true},
		{"groomed with criteria", func(t *Ticket) { t.Groomed = true; t.AcceptanceCriteria = []string{"it works"} }, false},
		{"criteria without groomed", func(t *Ticket) { t.AcceptanceCriteria = []string{"it works"} }, false},
		{"blank criterion", func(t *Ticket) { t.AcceptanceCriteria = []string{"  "} }, true},
		{"groomed idea", func(t *Ticket) { t.Kind = KindIdea; t.Groomed = true; t.AcceptanceCriteria = []string{"it works"} }, true},
		{"bug todo", func(t *Ticket) { t.Kind = KindBug }, false},
		{"bug in progress", func(t *Ticket) { t.Kind = KindBug; t.Status = StatusInProgress }, true},
		{"bug assigned", func(t *Ticket) { t.Kind = KindBug; id := ActorID("act-1"); t.AssigneeID = &id }, true},
		{"groomed bug", func(t *Ticket) { t.Kind = KindBug; t.Groomed = true; t.AcceptanceCriteria = []string{"it works"} }, true},
		{"task origin artifact", func(t *Ticket) { id := ArtifactID("art-1"); t.OriginArtifact = &id }, true},
		{"idea origin artifact", func(t *Ticket) { t.Kind = KindIdea; id := ArtifactID("art-1"); t.OriginArtifact = &id }, false},
		{"bug origin artifact", func(t *Ticket) { t.Kind = KindBug; id := ArtifactID("art-1"); t.OriginArtifact = &id }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := base
			tt.mutate(&task)
			if err := task.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestTaskResolves(t *testing.T) {
	p := DefaultResolutionPolicy()
	tests := []struct {
		name string
		task Ticket
		want bool
	}{
		{"task todo", Ticket{Kind: KindTask, Status: StatusTodo}, false},
		{"task in progress", Ticket{Kind: KindTask, Status: StatusInProgress}, false},
		{"task in review", Ticket{Kind: KindTask, Status: StatusReadyForReview}, true},
		{"task done", Ticket{Kind: KindTask, Status: StatusDone}, true},
		{"milestone in review", Ticket{Kind: KindMilestone, Status: StatusReadyForReview}, false},
		{"milestone done", Ticket{Kind: KindMilestone, Status: StatusDone}, true},
		{"idea todo", Ticket{Kind: KindIdea, Status: StatusTodo}, true},
		{"idea done", Ticket{Kind: KindIdea, Status: StatusDone}, true},
		{"idea cancelled", Ticket{Kind: KindIdea, Status: StatusCancelled}, true},
		{"bug todo", Ticket{Kind: KindBug, Status: StatusTodo}, true},
		{"bug cancelled", Ticket{Kind: KindBug, Status: StatusCancelled}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.task.Resolves(p); got != tt.want {
				t.Fatalf("Resolves() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTaskIsMilestone(t *testing.T) {
	if !(Ticket{Kind: KindMilestone}).IsMilestone() {
		t.Fatal("milestone task should report IsMilestone")
	}
	if (Ticket{Kind: KindTask}).IsMilestone() {
		t.Fatal("plain task should not report IsMilestone")
	}
}

func TestLinkValidate(t *testing.T) {
	tests := []struct {
		name    string
		link    Link
		wantErr bool
	}{
		{"pr", Link{Kind: LinkPR, URL: "https://example.com/pr/1"}, false},
		{"issue with title", Link{Kind: LinkIssue, URL: "https://example.com/1", Title: "bug"}, false},
		{"doc", Link{Kind: LinkDoc, URL: "https://example.com/doc"}, false},
		{"url", Link{Kind: LinkURL, URL: "https://example.com"}, false},
		{"unknown kind", Link{Kind: "wiki", URL: "https://example.com"}, true},
		{"missing url", Link{Kind: LinkPR}, true},
		{"missing kind", Link{URL: "https://example.com"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.link.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestNoteValidate(t *testing.T) {
	tests := []struct {
		name    string
		note    Note
		wantErr bool
	}{
		{"valid", Note{ID: "n-1", Body: "needs a decision"}, false},
		{"valid with link", Note{ID: "n-1", Body: "see PR", Links: []Link{{Kind: LinkPR, URL: "https://example.com/pr/1"}}}, false},
		{"missing id", Note{Body: "x"}, true},
		{"missing body", Note{ID: "n-1"}, true},
		{"invalid link", Note{ID: "n-1", Body: "x", Links: []Link{{Kind: LinkPR}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.note.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// generatedNoteFamilyCases is the per-family contract: one representative body
// that must match, and one near miss that must not. The near miss is the shape a
// human note takes when it opens with the same words but is not the template.
var generatedNoteFamilyCases = []struct {
	family   string
	match    string
	nearMiss string
}{
	{"Triaged by agent:", "Triaged by agent: split the work", "Triaged by agent notes are useful"},
	{"Reassigned to you.", "Reassigned to you.", "Reassigned to you earlier"},
	{"Needs human decision:", "Needs human decision: pick a transport", "Needs human decision makers are listed"},
	{"Needs human triage:", "Needs human triage: pick a transport", "Needs human triage later this week"},
	{"Needs human grooming:", "Needs human grooming: scope the transport", "Needs human grooming standards apply"},
	{"Groomed by agent (auto):", "Groomed by agent (auto): closed the gaps", "Groomed by agentic tooling last week"},
	{"PR opened.", "PR opened.", "PR opened the door to a redesign"},
	{"PR opened (base <ref>)", "PR opened (base main).", "PR opened by Khoi last week"},
	{"PR #<n> opened", "PR #106 opened: https://example.com/pull/106 (branch ft/x)", "PR #999 open questions remain unresolved"},
	{"PR #<n> open:", "PR #104 open: https://example.com/pull/104 (branch ft/x)", "PR #999 open questions remain unresolved"},
	{"PR #<n> open (", "PR #100 open (branch ft/x): non-executable idea kind", "PR #999 open questions remain unresolved"},
	{"PR #<n> open for review", "PR #138 open for review: https://example.com/pull/138", "PR #999 open questions remain unresolved"},
	{"PR #<n> open on", "PR #135 open on branch ft/x: durable prompt committed", "PR #999 open questions remain unresolved"},
	{"PR #<n> open and", "PR #308 open and CI green: https://example.com/pull/308", "PR #999 open questions remain unresolved"},
	{"PR #<n> open;", "PR #118 open; handed to reviewer-t-abc", "PR #999 open questions remain unresolved"},
	{"PR #<n>: <url>", "PR #69: https://example.com/pull/69 - bound stdout", "PR #93: needs a rebase"},
	{"PR #<n> (branch <ref>)", "PR #198 (branch ft/x): ft run injects the env", "PR #93 (branching off) needs care"},
	{"PR #<n> (base <ref>)", "PR #10 (base main): usageError prints the reason", "PR #93 (based on review feedback) needs care"},
	{"PR #<n> for review", "PR #93 for review", "PR #93 for the record"},
	{"Opened PR #<n>", "Opened PR #106: https://example.com/pull/106", "Opened PR discussion on the topic"},
	{"PR: <url>", "PR: https://example.com/pull/88", "PR: needs a rebase"},
	{"PR link attached.", "PR link attached.", "PR link attached by hand"},
	{"Grooming <date> (session <n>):", "Grooming 2026-10-08 (session 2): split for reviewability", "Grooming session notes live in the artifact"},
	{"Grooming <date> (advisory).", "Grooming 2026-10-07 (advisory). Ideas are managed by hand", "Grooming advisory boards meet weekly"},
	{"Built (PR base <ref>):", "Built (PR base main): claim and CAS assignment", "Built: TaskRepo.Search port and lexical scoring"},
	{"Review round <n>", "Review round 1 (reviewer-t-abc123): fixed all findings", "Review round table needed for the kitchen"},
	{"Reviewer round <n>", "Reviewer round 1 on PR #189: findings fixed", "Reviewer rounds are scheduled weekly"},
	{"Reviewer APPROVED", "Reviewer APPROVED PR #107 at 17b715f", "Reviewer approval still pending"},
	{"Ready for review.", "Ready for review. Build to ./bin/ft", "Ready for review tomorrow morning"},
	{"Merged as PR #<n>", "Merged as PR #77 (9585a0a)", "Merged as PR review feedback arrived"},
	{"Landed as PR #<n>", "Landed as PR #70 (merged 5a052a0)", "Landed as PR without a number"},
	{"QA <label>", "QA report — ft msg end-to-end", "Quality assurance is pending"},
	{"Re-captured as idea <task>", "Re-captured as idea t-aa5c2dqqki (Khoi 2026-10-06).", "Re-captured as ideas from the session"},
	{"Re-captured from task <task>", "Re-captured from task t-ewr3xidxtk; kind converted to idea", "Re-captured from tasks in the sprint"},
}

func TestIsGeneratedNoteFamilies(t *testing.T) {
	for _, tt := range generatedNoteFamilyCases {
		t.Run(tt.family, func(t *testing.T) {
			if !IsGeneratedNote(Note{ID: "n-1", Body: tt.match}) {
				t.Errorf("family %q does not match its own template %q", tt.family, tt.match)
			}
			if IsGeneratedNote(Note{ID: "n-1", Body: tt.nearMiss}) {
				t.Errorf("family %q marks the near miss %q", tt.family, tt.nearMiss)
			}
		})
	}
}

// TestIsGeneratedNoteQANearMisses pins the human shapes that open with the QA
// token but are not a QA run: a bare colon label, a deferred sentence, and a
// sentence about QA coverage. The QA family matches the run's machine marker
// instead of the token alone.
func TestIsGeneratedNoteQANearMisses(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"bare colon label", "QA: needs a look before merge"},
		{"deferred sentence", "QA later this week when the lab is free"},
		{"human coverage concern", "QA coverage is thin in pkg/store; add tests"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if IsGeneratedNote(Note{ID: "n-1", Body: tt.body}) {
				t.Fatalf("marked a human note that opens with QA: %q", tt.body)
			}
		})
	}
}

func TestIsGeneratedNote(t *testing.T) {
	tests := []struct {
		name string
		note Note
		want bool
	}{
		{"indented template", Note{ID: "n-1", Body: "  Triaged by agent: split the work"}, true},
		{"human note", Note{ID: "n-1", Body: "Decision: ship the small thing"}, false},
		{"human decision with date", Note{ID: "n-1", Body: "Decision (Khoi 2026-10-06): keep XOR"}, false},
		{"human needs wording, not the machine label", Note{ID: "n-1", Body: "We need human decision on this later"}, false},
		{"authored template", Note{ID: "n-1", Author: "act-1", Body: "Triaged by agent: split the work"}, false},
		{"authored groomed note", Note{ID: "n-1", Author: "act-1", Body: "Groomed by agent (auto): closed the gaps"}, false},
		{"authored pr-opened note", Note{ID: "n-1", Author: "act-1", Body: "PR opened (base main)."}, false},
		{"authored needs-decision note", Note{ID: "n-1", Author: "act-1", Body: "Needs human decision: pick a transport"}, false},
		{"authored needs-grooming note", Note{ID: "n-1", Author: "act-1", Body: "Needs human grooming: scope the transport"}, false},
		{"already marked", Note{ID: "n-1", Body: "Reassigned to you.", System: true}, false},
		{"template without colon", Note{ID: "n-1", Body: "Triaged by agent"}, false},
		{"empty body", Note{}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsGeneratedNote(tt.note); got != tt.want {
				t.Fatalf("IsGeneratedNote() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGeneratedNoteFamiliesDocumented keeps the table and its per-family cases
// in step: every family needs a shape and a meaning, and every family needs a
// match and a near-miss row, so a new family cannot land documented but
// untested (or tested but undocumented).
func TestGeneratedNoteFamiliesDocumented(t *testing.T) {
	families := GeneratedNoteFamilies()
	if len(families) == 0 {
		t.Fatal("no generated-note families")
	}
	cases := make(map[string]bool, len(generatedNoteFamilyCases))
	for _, tt := range generatedNoteFamilyCases {
		cases[tt.family] = true
	}
	for _, f := range families {
		if f.Shape == "" {
			t.Errorf("family %+v has no shape", f)
		}
		if f.Meaning == "" {
			t.Errorf("family %q has no meaning", f.Shape)
		}
		if !cases[f.Shape] {
			t.Errorf("family %q has no match/near-miss case", f.Shape)
		}
		delete(cases, f.Shape)
	}
	for shape := range cases {
		t.Errorf("case for %q has no family in the table", shape)
	}
}
