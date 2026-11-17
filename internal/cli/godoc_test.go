package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

// TestDocCommentsAttachToOwnSymbol pins the doc block of a few symbols to the
// symbol it describes. go/doc attributes a whole contiguous comment block to the
// declaration it directly precedes, so an unseparated block moves one symbol's
// godoc onto another (and `go doc -u <stale name>` reports no symbol at all when
// the paragraph belongs to one that no longer exists).
func TestDocCommentsAttachToOwnSymbol(t *testing.T) {
	cases := []struct {
		file    string
		symbol  string
		want    string
		notWant string
	}{
		{
			file:    "message.go",
			symbol:  "newMessageAgentCommand",
			want:    "hidden, stable adapter interface",
			notWant: "validateAckState",
		},
		{
			file:    "message.go",
			symbol:  "validateAckState",
			want:    "rejects a state outside the two",
			notWant: "hidden, stable adapter interface",
		},
		{
			file:    "deps.go",
			symbol:  "maybeRerank",
			want:    "reorders a lexical shortlist by meaning when a judge is configured",
			notWant: "rerankArtifacts",
		},
		{
			file:    "task.go",
			symbol:  "warnDuplicateTitle",
			want:    "advises (without blocking)",
			notWant: "canonical single-result field set",
		},
		{
			file:    "task.go",
			symbol:  "taskFields",
			want:    "canonical single-result field set",
			notWant: "advises (without blocking)",
		},
	}
	docs := make(map[string]map[string]string)
	for _, tc := range cases {
		if docs[tc.file] == nil {
			docs[tc.file] = funcDocs(t, tc.file)
		}
		t.Run(tc.file+"/"+tc.symbol, func(t *testing.T) {
			doc, ok := docs[tc.file][tc.symbol]
			if !ok {
				t.Fatalf("%s has no doc comment", tc.symbol)
			}
			if !strings.Contains(doc, tc.want) {
				t.Errorf("%s doc = %q, want it to mention %q", tc.symbol, doc, tc.want)
			}
			if strings.Contains(doc, tc.notWant) {
				t.Errorf("%s doc = %q, want it not to mention %q", tc.symbol, doc, tc.notWant)
			}
		})
	}
}

// TestDocCommentsStartWithOwnSymbol holds every documented function in the
// package to Go's doc-comment convention: the comment begins with the name of
// the thing it documents. A block that runs into the next declaration starts
// with the *previous* symbol's name instead, which is how the misattribution
// above happens. Checking every declaration catches the class, not only the
// names pinned by the table test.
func TestDocCommentsStartWithOwnSymbol(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, name, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Doc == nil {
				continue
			}
			if got := firstDocWord(fn.Doc.Text()); got != fn.Name.Name {
				t.Errorf("%s: doc of %s starts with %q; each doc comment must start with its own symbol",
					name, fn.Name.Name, got)
			}
		}
	}
}

// funcDocs maps each function declared in path to its doc comment text.
func funcDocs(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	docs := make(map[string]string)
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Doc == nil {
			continue
		}
		docs[fn.Name.Name] = fn.Doc.Text()
	}
	return docs
}

// firstDocWord returns the leading identifier of a doc comment's text, which is
// the symbol name go/doc shows a reader first.
func firstDocWord(doc string) string {
	words := strings.Fields(doc)
	if len(words) == 0 {
		return ""
	}
	word := words[0]
	for i, r := range word {
		ident := r == '_' ||
			r >= 'a' && r <= 'z' ||
			r >= 'A' && r <= 'Z' ||
			i > 0 && r >= '0' && r <= '9'
		if !ident {
			return word[:i]
		}
	}
	return word
}
