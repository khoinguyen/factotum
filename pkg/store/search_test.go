package store

import (
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
)

func TestLexicalTerms(t *testing.T) {
	tests := []struct {
		query string
		want  []string
	}{
		{"", nil},
		{"  ", nil},
		{"Terraform", []string{"terraform"}},
		{"terraform apply", []string{"terraform", "apply"}},
		{"infra-as-code", []string{"infra", "as", "code"}},
		{"  Mixed CASE  ", []string{"mixed", "case"}},
	}
	for _, tt := range tests {
		got := LexicalTerms(tt.query)
		if len(got) != len(tt.want) {
			t.Fatalf("LexicalTerms(%q) = %v, want %v", tt.query, got, tt.want)
		}
		for i := range tt.want {
			if got[i] != tt.want[i] {
				t.Fatalf("LexicalTerms(%q) = %v, want %v", tt.query, got, tt.want)
			}
		}
	}
}

func TestLexicalTaskScore(t *testing.T) {
	task := &core.Ticket{
		Title:       "Terraform notes",
		Description: "apply in the devops repo",
		Notes:       []core.Note{{Body: "remember the kubectl flag"}},
	}
	tests := []struct {
		name    string
		query   string
		want    float64
		matched bool
	}{
		{"title hit", "terraform", 3, true},
		{"title prefix", "terra", 3, true},
		{"description hit", "devops", 2, true},
		{"note hit", "kubectl", 1, true},
		{"title and description", "terraform devops", 5, true},
		{"title and note", "terraform kubectl", 4, true},
		{"case insensitive", "TERRAFORM", 3, true},
		{"all terms required", "terraform missing", 0, false},
		{"no match", "kubernetes", 0, false},
		{"empty query matches", "", 0, true},
	}
	for _, tt := range tests {
		score, matched := LexicalTaskScore(task, LexicalTerms(tt.query))
		if matched != tt.matched || score != tt.want {
			t.Errorf("%s: LexicalTaskScore = (%v, %v), want (%v, %v)", tt.name, score, matched, tt.want, tt.matched)
		}
	}
}

func TestLexicalTaskScoreSkipsSystemNotes(t *testing.T) {
	task := &core.Ticket{
		Title:       "quiet",
		Description: "nothing here",
		Notes: []core.Note{
			{ID: "n-system", Body: "sysprobe in a generated note", System: true},
			{ID: "n-human", Body: "humanprobe in a real note"},
		},
	}
	if score, matched := LexicalTaskScore(task, LexicalTerms("sysprobe")); matched || score != 0 {
		t.Errorf("system note scored (%v, %v), want no match", score, matched)
	}
	if score, matched := LexicalTaskScore(task, LexicalTerms("humanprobe")); !matched || score != 1 {
		t.Errorf("human note = (%v, %v), want (1, true)", score, matched)
	}
}

func TestSortTaskSearchHits(t *testing.T) {
	task := func(id, title string) *core.Ticket {
		return &core.Ticket{ID: core.TicketID(id), Title: title}
	}
	hits := []TicketSearchHit{
		{Ticket: task("t-3", "beta"), Score: 1},
		{Ticket: task("t-1", "Alpha"), Score: 3},
		{Ticket: task("t-2", "alpha"), Score: 3},
		{Ticket: task("t-4", "gamma"), Score: 1},
	}
	SortTaskSearchHits(hits)
	want := []core.TicketID{"t-1", "t-2", "t-3", "t-4"}
	for i, id := range want {
		if hits[i].Ticket.ID != id {
			t.Fatalf("SortTaskSearchHits() = %v, want %v", taskIDs(hits), want)
		}
	}
}

func taskIDs(hits []TicketSearchHit) []core.TicketID {
	out := make([]core.TicketID, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Ticket.ID)
	}
	return out
}

func TestLexicalScore(t *testing.T) {
	artifact := &core.Artifact{Title: "Terraform notes", Body: "apply in the devops repo"}
	tests := []struct {
		name    string
		query   string
		want    float64
		matched bool
	}{
		{"title hit", "terraform", 3, true},
		{"title prefix", "terra", 3, true},
		{"body hit", "devops", 1, true},
		{"title and body", "terraform devops", 4, true},
		{"case insensitive", "TERRAFORM", 3, true},
		{"all terms required", "terraform missing", 0, false},
		{"no match", "kubernetes", 0, false},
		{"empty query matches", "", 0, true},
	}
	for _, tt := range tests {
		score, matched := LexicalScore(artifact, LexicalTerms(tt.query))
		if matched != tt.matched || score != tt.want {
			t.Errorf("%s: LexicalScore = (%v, %v), want (%v, %v)", tt.name, score, matched, tt.want, tt.matched)
		}
	}
}
