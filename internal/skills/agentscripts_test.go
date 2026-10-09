package skills

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestCIRunsChiefSkillScriptTests guards the wiring that makes script
// regressions fail CI: `mise run ci` must depend on a task that runs every
// .agents/skills/chief/scripts/*_test.sh. Those scripts are hermetic (they stub
// cmux, ft, opencode and pkill on PATH), so without this wiring a broken script
// would only be noticed when a human ran it by hand.
func TestCIRunsChiefSkillScriptTests(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")

	var cfg struct {
		Tasks map[string]struct {
			Depends []string `toml:"depends"`
			Run     string   `toml:"run"`
		} `toml:"tasks"`
	}
	if _, err := toml.DecodeFile(filepath.Join(root, "mise.toml"), &cfg); err != nil {
		t.Fatalf("parse mise.toml: %v", err)
	}

	const task = "test:scripts"
	ci, ok := cfg.Tasks["ci"]
	if !ok {
		t.Fatal("mise.toml has no ci task")
	}
	if !slices.Contains(ci.Depends, task) {
		t.Errorf("mise ci must depend on %q so the chief scripts' hermetic tests fail the build", task)
	}

	runner, ok := cfg.Tasks[task]
	if !ok {
		t.Fatalf("mise.toml has no %q task", task)
	}
	if !strings.Contains(runner.Run, ".agents/skills/chief/scripts/*_test.sh") {
		t.Errorf("%q must run .agents/skills/chief/scripts/*_test.sh, got:\n%s", task, runner.Run)
	}
}

// TestPreCommitRunsChiefSkillScriptTests guards the git pre-commit hook that
// `mise run hooks` installs: it must run `mise run test:scripts`, so a chief
// script regression blocks a commit instead of slipping through to `mise run
// ci`. The hook file itself is uncommitted, so the task that writes it is the
// thing to guard.
func TestPreCommitRunsChiefSkillScriptTests(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	root := filepath.Join(filepath.Dir(file), "..", "..")

	var cfg struct {
		Tasks map[string]struct {
			Run string `toml:"run"`
		} `toml:"tasks"`
	}
	if _, err := toml.DecodeFile(filepath.Join(root, "mise.toml"), &cfg); err != nil {
		t.Fatalf("parse mise.toml: %v", err)
	}

	hooks, ok := cfg.Tasks["hooks"]
	if !ok {
		t.Fatal("mise.toml has no hooks task")
	}
	if !strings.Contains(hooks.Run, "mise run test:scripts") {
		t.Errorf("the installed pre-commit hook must run `mise run test:scripts`, got:\n%s", hooks.Run)
	}
}
