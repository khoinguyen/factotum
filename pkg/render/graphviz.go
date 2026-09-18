package render

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
)

type DOT struct{}

func (DOT) Format() string { return "dot" }

func (DOT) Render(_ context.Context, w io.Writer, view View) error {
	derived := view.info()
	var b strings.Builder
	b.WriteString("digraph tasks {\n  rankdir=LR;\n")
	for _, id := range derived.ids {
		task := derived.tasks[id]
		fmt.Fprintf(&b, "  %q [label=%q];\n", string(id), fmt.Sprintf("%s\\n%s", id, task.Title))
	}
	for _, id := range derived.ids {
		for _, dep := range view.Graph.Deps(id) {
			fmt.Fprintf(&b, "  %q -> %q;\n", string(dep), string(id))
		}
	}
	b.WriteString("}\n")
	return writeString(w, b.String())
}

type Mermaid struct{}

func (Mermaid) Format() string { return "mermaid" }

func (Mermaid) Render(_ context.Context, w io.Writer, view View) error {
	derived := view.info()
	var b strings.Builder
	b.WriteString("graph LR\n")
	for _, id := range derived.ids {
		task := derived.tasks[id]
		fmt.Fprintf(&b, "  %s[\"%s: %s\"]\n", mermaidID(id), id, escapeMermaid(task.Title))
	}
	for _, id := range derived.ids {
		for _, dep := range view.Graph.Deps(id) {
			fmt.Fprintf(&b, "  %s --> %s\n", mermaidID(dep), mermaidID(id))
		}
	}
	return writeString(w, b.String())
}

func mermaidID(id core.TaskID) string {
	var b strings.Builder
	for _, r := range string(id) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

func escapeMermaid(s string) string {
	s = strings.ReplaceAll(s, "\"", "'")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
