package cli

import (
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
// same table the predicate uses: adding a family must not need a second edit.
func TestBackfillHelpListsRecognizedFamilies(t *testing.T) {
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	backfill, _, err := NewRoot(deps).Find([]string{"task", "note", "backfill"})
	if err != nil {
		t.Fatalf("find task note backfill: %v", err)
	}
	families := core.GeneratedNoteOpenings()
	if len(families) == 0 {
		t.Fatal("no generated-note families to document")
	}
	for _, opening := range families {
		if !strings.Contains(backfill.Long, opening) {
			t.Errorf("backfill help does not list the %q family", opening)
		}
	}
}
