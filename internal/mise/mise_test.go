// Package mise guards the build recipes in mise.toml. A recipe that compiles
// the ft binary embeds web/dist at build time, so it must rebuild the frontend
// first; otherwise `mise run install` ships a stale SPA.
package mise

import (
	"path/filepath"
	"runtime"
	"testing"

	"github.com/BurntSushi/toml"
)

// config mirrors the slice of mise.toml this guard needs.
type config struct {
	Tasks map[string]task `toml:"tasks"`
}

type task struct {
	Depends []string `toml:"depends"`
}

func loadConfig(t *testing.T) config {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")
	var cfg config
	if _, err := toml.DecodeFile(filepath.Join(root, "mise.toml"), &cfg); err != nil {
		t.Fatalf("decode mise.toml: %v", err)
	}
	return cfg
}

// dependsOn reports whether task reaches dep through the depends graph.
func dependsOn(cfg config, task, dep string) bool {
	seen := map[string]bool{}
	var walk func(string) bool
	walk = func(name string) bool {
		if seen[name] {
			return false
		}
		seen[name] = true
		for _, d := range cfg.Tasks[name].Depends {
			if d == dep || walk(d) {
				return true
			}
		}
		return false
	}
	return walk(task)
}

// TestBinaryRecipesEmbedCurrentFrontend pins the invariant that every recipe
// compiling the embedded frontend into the binary rebuilds web/dist first.
// `mise run install` without build-web embedded a stale web/dist, so the
// installed dashboard showed no change after a frontend fix.
func TestBinaryRecipesEmbedCurrentFrontend(t *testing.T) {
	cfg := loadConfig(t)
	for _, name := range []string{"build", "install"} {
		if !dependsOn(cfg, name, "build-web") {
			t.Errorf("mise task %q must depend (directly or transitively) on build-web, or it embeds a stale web/dist", name)
		}
	}
}

// TestDependsOnWalksTransitively guards the guard: a direct edge, a transitive
// edge, and a missing edge must be distinguished, and a dependency cycle must
// not hang.
func TestDependsOnWalksTransitively(t *testing.T) {
	cfg := config{Tasks: map[string]task{
		"a": {Depends: []string{"b"}},
		"b": {Depends: []string{"build-web"}},
		"c": {},
		"x": {Depends: []string{"y"}},
		"y": {Depends: []string{"x"}},
	}}
	if !dependsOn(cfg, "a", "build-web") {
		t.Error("transitive dependency not followed")
	}
	if !dependsOn(cfg, "b", "build-web") {
		t.Error("direct dependency not followed")
	}
	if dependsOn(cfg, "c", "build-web") {
		t.Error("reported a dependency that does not exist")
	}
	if dependsOn(cfg, "x", "build-web") {
		t.Error("reported a dependency through a cycle")
	}
}
