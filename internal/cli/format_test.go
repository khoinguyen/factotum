package cli

import (
	"strings"
	"testing"
)

func TestWrapTextPreservesParagraphs(t *testing.T) {
	if got := wrapText("a\n\nb\n", 40); got != "a\n\nb" {
		t.Fatalf("wrapText() = %q, want %q", got, "a\n\nb")
	}
}

func TestWrapTextWrapsLongLines(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("word ", 40))
	wrapped := wrapText(long, 40)
	for _, line := range strings.Split(wrapped, "\n") {
		if len(line) > 40 {
			t.Fatalf("wrapped line exceeds width: %q", line)
		}
	}
	if strings.Contains(wrapped, "\n\n") {
		t.Fatalf("unexpected blank line in wrapped text: %q", wrapped)
	}
}

func TestWrapTextKeepsLongTokens(t *testing.T) {
	token := strings.Repeat("x", 120)
	if got := wrapText(token, 40); got != token {
		t.Fatalf("long token should be left intact, got %q", got)
	}
}

func TestWrapTextPreservesIndent(t *testing.T) {
	wrapped := wrapText("  - "+strings.TrimSpace(strings.Repeat("word ", 30)), 40)
	for _, line := range strings.Split(wrapped, "\n") {
		if !strings.HasPrefix(line, "  ") {
			t.Fatalf("continuation line lost indent: %q", line)
		}
	}
}

func TestWrapTextHangingIndentForBullets(t *testing.T) {
	wrapped := wrapText("- "+strings.TrimSpace(strings.Repeat("word ", 30)), 40)
	lines := strings.Split(wrapped, "\n")
	if len(lines) < 2 {
		t.Fatalf("expected the bullet to wrap, got %q", wrapped)
	}
	if !strings.HasPrefix(lines[1], "  ") {
		t.Fatalf("continuation not hanging-indented: %q", lines[1])
	}
}
