package cli

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestMessageDocCommentsAttachToOwnSymbol guards message.go against a doc block
// that runs into the next declaration: go/doc attributes a whole comment block
// to the symbol it directly precedes, so a missing blank line moves one
// symbol's godoc onto another.
func TestMessageDocCommentsAttachToOwnSymbol(t *testing.T) {
	docs := funcDocs(t, "message.go")

	cases := []struct {
		symbol  string
		want    string
		notWant string
	}{
		{
			symbol:  "newMessageAgentCommand",
			want:    "hidden, stable adapter interface",
			notWant: "validateAckState",
		},
		{
			symbol:  "validateAckState",
			want:    "rejects a state outside the two",
			notWant: "hidden, stable adapter interface",
		},
	}
	for _, tc := range cases {
		t.Run(tc.symbol, func(t *testing.T) {
			doc, ok := docs[tc.symbol]
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
