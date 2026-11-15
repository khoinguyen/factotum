package cli

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
)

func TestNoteSnippetClipBoundary(t *testing.T) {
	atBoundary := strings.Repeat("a", 60)
	tests := []struct {
		name string
		body string
		want string
	}{
		{"short body is unchanged", "Needs human decision: pick a transport", "Needs human decision: pick a transport"},
		{"exactly at the boundary is unchanged", atBoundary, atBoundary},
		{"over the boundary clips to 57 runes and an ellipsis", strings.Repeat("a", 61), strings.Repeat("a", 57) + "..."},
		{"whitespace collapses to one line", "PR opened\n(base main).", "PR opened (base main)."},
		{"clip counts runes, not bytes", strings.Repeat("é", 61), strings.Repeat("é", 57) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := noteSnippet(tt.body); got != tt.want {
				t.Fatalf("noteSnippet(%d chars) = %q, want %q", len([]rune(tt.body)), got, tt.want)
			}
		})
	}
}

// TestBackfillHelpListsRecognizedFamilies keeps the verb's documentation on the
// same table the predicate uses: adding a family must not need a second edit,
// and the help must show what each family means, not only its prefix.
func TestBackfillHelpListsRecognizedFamilies(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	backfill, _, err := NewRoot(deps).Find([]string{"task", "note", "backfill"})
	if err != nil {
		t.Fatalf("find task note backfill: %v", err)
	}
	families := core.GeneratedNoteFamilies()
	if len(families) == 0 {
		t.Fatal("no generated-note families to document")
	}
	for _, family := range families {
		if !strings.Contains(backfill.Long, family.Shape) {
			t.Errorf("backfill help does not list the %q family", family.Shape)
		}
		if !strings.Contains(backfill.Long, family.Meaning) {
			t.Errorf("backfill help does not explain the %q family (%q)", family.Shape, family.Meaning)
		}
	}
}

// noteAuthors decodes the note attributions `task get -o json` reports.
func noteAuthors(t *testing.T, out string) []noteDoc {
	t.Helper()
	var doc struct {
		Notes []noteDoc `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode task get json: %v\n%s", err, out)
	}
	return doc.Notes
}

// TestNoteAuthorFollowsCallerAttribution pins a note's author to the same
// attribution the invocation's own event carries. The note path used to
// re-resolve the caller with Actors.Resolve, which knows only Actor records, so
// a loop role - a mailbox a session registers as a run, never a record - wrote
// a note with no author while its task.note_added event named the role. An
// authorless note is exactly what ft task note backfill reads as machine text.
func TestNoteAuthorFollowsCallerAttribution(t *testing.T) {
	cases := []struct {
		name     string
		actor    string
		register bool
		want     string
		wantWarn bool
	}{
		{name: "registered actor", actor: "alice", register: true, want: "alice"},
		{name: "loop role with no record", actor: "builder-t-1", want: "builder-t-1"},
		// The --actor path trusts any LoopRole token, so attribution names the
		// role even for a lookalike; the note records that same actor instead of
		// disagreeing with the event. Actor-valued flags still reject it.
		{name: "role lookalike stays the attributed role", actor: "builder-gpt-4", want: "builder-gpt-4"},
		{name: "unregistered non-role stays unattributed", actor: "ghostrider", want: "", wantWarn: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRunner(t)
			r.getenv = func(string) string { return "" }
			if tc.register {
				r.run("actor", "create", tc.actor)
			}
			projectID := firstField(t, r.run("project", "create", "Acme"))
			taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "attribution"))

			_, stderr := r.runSplit("task", "note", "create", taskID, "-b", "Decision: keep the note author", "--actor", tc.actor)

			notes := noteAuthors(t, r.run("task", "get", taskID, "-o", "json"))
			if len(notes) != 1 {
				t.Fatalf("notes = %+v, want exactly one", notes)
			}
			if notes[0].Author != tc.want {
				t.Fatalf("note author = %q, want %q", notes[0].Author, tc.want)
			}
			added := findEvent(t, listEvents(t, r), string(core.EventTaskNoteAdded))
			by := ""
			if added.By != nil {
				by = *added.By
			}
			if by != notes[0].Author {
				t.Fatalf("task.note_added By = %q but the note author = %q; the call must attribute both the same way", by, notes[0].Author)
			}
			warned := strings.Contains(stderr, "is not a registered actor")
			if warned != tc.wantWarn {
				t.Fatalf("stderr = %q, want warning = %v", stderr, tc.wantWarn)
			}
		})
	}
}

// TestRoleAuthoredNoteSurvivesBackfill pins the consequence end to end: a loop
// role writing a note shaped like a machine-note family keeps that note out of
// the backfill, while the same body written unattributed is still marked. The
// author is what tells them apart.
func TestRoleAuthoredNoteSurvivesBackfill(t *testing.T) {
	r := newRunner(t)
	r.getenv = func(string) string { return "" }
	projectID := firstField(t, r.run("project", "create", "Acme"))
	taskID := firstField(t, r.run("task", "create", "-p", projectID, "-t", "attribution"))

	r.run("task", "note", "create", taskID, "-b", "Built (PR base main): roleauthoredprobe wired the note author", "--actor", "builder-t-1")
	r.run("task", "note", "create", taskID, "-b", "Built (PR base main): unattributedprobe wrote this with no caller")

	out := r.run("task", "note", "backfill", "-p", projectID)
	if !strings.Contains(out, "scanned: 1") || !strings.Contains(out, "marked: 1") {
		t.Fatalf("backfill:\n%s\nwant one note marked, the unattributed one", out)
	}
	notes := noteAuthors(t, r.run("task", "get", taskID, "-o", "json"))
	for _, note := range notes {
		switch {
		case strings.Contains(note.Body, "roleauthoredprobe"):
			if note.Author != "builder-t-1" || note.System {
				t.Fatalf("role-authored note = %+v, want builder-t-1 and unmarked", note)
			}
		case strings.Contains(note.Body, "unattributedprobe"):
			if note.Author != "" || !note.System {
				t.Fatalf("unattributed generated note = %+v, want no author and marked", note)
			}
		}
	}
}
