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
