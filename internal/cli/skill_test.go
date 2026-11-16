package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/internal/skills"
	"github.com/khoinguyen/factotum/pkg/core"
)

func TestSkillList(t *testing.T) {
	r := newRunner(t)
	out := r.run("skill", "list")
	if !strings.Contains(out, "NAME") || !strings.Contains(out, "DESCRIPTION") || !strings.Contains(out, "ft") {
		t.Fatalf("skill list missing header or default skill:\n%s", out)
	}

	jsonOut := r.run("skill", "list", "-o", "json")
	var entries []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal([]byte(jsonOut), &entries); err != nil {
		t.Fatalf("skill list json: %v\n%s", err, jsonOut)
	}
	if len(entries) == 0 || entries[0].Name != "ft" || entries[0].Description == "" {
		t.Fatalf("skill list json = %+v", entries)
	}
}

func TestSkillGetPrintsRawMarkdown(t *testing.T) {
	r := newRunner(t)
	out := r.run("skill", "get")
	raw, err := skills.Get(skills.DefaultName)
	if err != nil {
		t.Fatalf("skills.Get(%q) error = %v", skills.DefaultName, err)
	}
	if out != raw.Body {
		t.Fatalf("skill get output is not the raw body (truncated by the output bound?): got %d bytes, want %d:\n%s", len(out), len(raw.Body), out)
	}
	if named := r.run("skill", "get", "ft"); named != out {
		t.Fatalf("skill get ft differs from the default:\n%s", named)
	}
}

// TestRenderSkillDocTextEndsBodyWithExactlyOneNewline pins the text rendering's
// trailing newline: `ft skill get` prints the raw body, and a body that does not
// end with a newline must gain exactly one so the last line is terminated, while
// a body that already ends with one is not doubled. Every embedded skill ends
// with a newline, so no other test reaches the append branch; without this pin a
// regression there prints a body without its final newline, and the output-bound
// guard cannot see it because it measures rendered bytes rather than their shape.
func TestRenderSkillDocTextEndsBodyWithExactlyOneNewline(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "body without a trailing newline gains one", body: "# ft\n\nA skill body with no final newline."},
		{name: "body with a trailing newline is not doubled", body: "# ft\n\nA skill body that already ends with one.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := renderSkillDoc(&buf, "text", skillDoc{Name: "probe", Body: tc.body}); err != nil {
				t.Fatalf("renderSkillDoc(text) error = %v", err)
			}
			want := strings.TrimSuffix(tc.body, "\n") + "\n"
			if got := buf.String(); got != want {
				t.Fatalf("renderSkillDoc(text) = %q, want %q: the body must end with exactly one newline", got, want)
			}
		})
	}
}

func TestSkillGetUnknown(t *testing.T) {
	r := newRunner(t)
	err := r.runErr("skill", "get", "nope")
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("skill get nope error = %v, want ErrNotFound", err)
	}
}

// TestSkillGetStructuredOutputCarriesWholeBody pins the truncation that the
// text-only byte guard missed: serializing the doc inflates the body past the
// output bound, so `ft skill get ft -o json|yaml` used to print the
// truncated-envelope instead of the skill. Each format must decode back to the
// whole body.
func TestSkillGetStructuredOutputCarriesWholeBody(t *testing.T) {
	r := newRunner(t)
	raw, err := skills.Get(skills.DefaultName)
	if err != nil {
		t.Fatalf("skills.Get(%q) error = %v", skills.DefaultName, err)
	}
	for _, format := range skillFormats {
		if format == "text" {
			continue
		}
		t.Run(format, func(t *testing.T) {
			out := r.run("skill", "get", skills.DefaultName, "-o", format)
			if strings.Contains(out, "truncated:") || strings.Contains(out, "full_output_path") {
				t.Fatalf("skill get -o %s printed the truncation envelope; the skill does not fit the output bound:\n%s", format, out[:min(len(out), 400)])
			}
			var doc skillDoc
			var err error
			if format == "json" {
				err = json.Unmarshal([]byte(out), &doc)
			} else {
				err = yaml.Unmarshal([]byte(out), &doc)
			}
			if err != nil {
				t.Fatalf("decode skill get -o %s: %v", format, err)
			}
			if doc.Name != raw.Name || doc.Description != raw.Description || doc.Body != raw.Body {
				t.Fatalf("skill get -o %s lost the document: name %q, %d body bytes, want %q and %d", format, doc.Name, len(doc.Body), raw.Name, len(raw.Body))
			}
		})
	}
}
