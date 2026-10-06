package core

import (
	"go/build"
	"strings"
	"testing"
)

// TestCoreDoesNotImportRunPorts guards the hexagon: pkg/core is pure and must
// not import the run plugin ports or any of their adapters. The launcher
// orchestrates those ports in pkg/app; core stays free of them so no backend or
// harness type can leak into the domain.
func TestCoreDoesNotImportRunPorts(t *testing.T) {
	forbidden := []string{
		"github.com/khoinguyen/factotum/pkg/isolation",
		"github.com/khoinguyen/factotum/pkg/harness",
	}
	pkg, err := build.ImportDir(".", 0)
	if err != nil {
		t.Fatalf("ImportDir(pkg/core) error = %v", err)
	}
	for _, imp := range pkg.Imports {
		for _, prefix := range forbidden {
			if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
				t.Errorf("pkg/core imports %q; core must stay free of the run ports", imp)
			}
		}
	}
}
