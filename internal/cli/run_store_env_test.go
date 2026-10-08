package cli

import (
	"path/filepath"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestStoreEnvMakesRelativePathAbsolute pins that a file-backed store path is
// resolved against the invocation directory before it reaches the session: the
// session's working directory differs, so a relative path would name a different
// file (or none) inside the checkout.
func TestStoreEnvMakesRelativePathAbsolute(t *testing.T) {
	deps := &Deps{Config: config.Config{Store: config.Store{
		Backend: "jsonfile",
		Options: map[string]string{"path": "rel/db.json"},
	}}}
	want, err := filepath.Abs("rel/db.json")
	if err != nil {
		t.Fatal(err)
	}
	env := deps.storeEnv()
	if env[harnesspkg.EnvStore] != "jsonfile" {
		t.Fatalf("env %s = %q, want jsonfile", harnesspkg.EnvStore, env[harnesspkg.EnvStore])
	}
	if got := env[harnesspkg.EnvStoreOpts]; got != "path="+want {
		t.Fatalf("env %s = %q, want path=%s", harnesspkg.EnvStoreOpts, got, want)
	}
}

// TestStoreEnvKeepsMemoryPath pins that an in-memory store is not corrupted by
// path resolution: ":memory:" is a sentinel, not a file to absolutize.
func TestStoreEnvKeepsMemoryPath(t *testing.T) {
	deps := &Deps{Config: config.Config{Store: config.Store{
		Backend: "sqlite",
		Options: map[string]string{"path": ":memory:"},
	}}}
	env := deps.storeEnv()
	if got := env[harnesspkg.EnvStoreOpts]; got != "path=:memory:" {
		t.Fatalf("env %s = %q, want path=:memory:", harnesspkg.EnvStoreOpts, got)
	}
}

// TestStoreEnvOverridesRealUserDatabase closes the chain: config.Load, given the
// env storeEnv derives and a user config that maps the project to a real
// database, resolves the run's store. That is exactly the fallback the bug was
// about - the session's child ft reading the real user DB because the checkout
// could not resolve the intended store.
func TestStoreEnvOverridesRealUserDatabase(t *testing.T) {
	dir := t.TempDir()
	user := filepath.Join(dir, "user.toml")
	mustWrite(t, user, "default_project = \"factotum\"\n\n[projects.factotum]\ndb_path = \"/home/real/.factotum/factotum.db\"\n")
	project := filepath.Join(dir, "project.toml")
	mustWrite(t, project, "project = \"factotum\"\n")

	lab := filepath.Join(dir, "lab.json")
	deps := &Deps{Config: config.Config{Store: config.Store{
		Backend: "jsonfile",
		Options: map[string]string{"path": lab},
	}}}
	env := deps.storeEnv()
	loaded, err := config.Load(config.Input{
		UserPath:    user,
		ProjectPath: project,
		Getenv:      func(key string) string { return env[key] },
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Store.Backend != "jsonfile" || loaded.Store.Options["path"] != lab {
		t.Fatalf("resolved store = %+v, want the run's lab store %q, not the real user DB", loaded.Store, lab)
	}
}

// TestRunInjectsResolvedStoreEnv proves `ft run` hands the launched session the
// store it resolved (backend and options), so the session's child ft reads that
// store instead of resolving another from the checkout's project or user config.
func TestRunInjectsResolvedStoreEnv(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	r.run("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	specs := backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	env := specs[0].Env
	if env[harnesspkg.EnvStore] != "jsonfile" {
		t.Errorf("env %s = %q, want jsonfile", harnesspkg.EnvStore, env[harnesspkg.EnvStore])
	}
	if want := "path=" + r.path; env[harnesspkg.EnvStoreOpts] != want {
		t.Errorf("env %s = %q, want %q", harnesspkg.EnvStoreOpts, env[harnesspkg.EnvStoreOpts], want)
	}
}

// TestGroomInjectsResolvedStoreEnv proves `ft groom` shares the run's store
// injection, so a grooming session's child ft reads the run's store rather than
// the user database.
func TestGroomInjectsResolvedStoreEnv(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	r.run("task", "create", "-p", projectID, "-t", "Add widget")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	mustWrite(t, r.userPath, "[run]\nsandbox = \"fake\"\nharness = \"fake\"\n")
	r.run("--config", cfgPath, "groom", "-p", projectID, "--prompt-file", promptPath, "--workspace", t.TempDir())

	specs := backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	env := specs[0].Env
	if env[harnesspkg.EnvProject] != projectID {
		t.Errorf("env %s = %q, want the run's project %q", harnesspkg.EnvProject, env[harnesspkg.EnvProject], projectID)
	}

	if env[harnesspkg.EnvStore] != "jsonfile" {
		t.Errorf("env %s = %q, want jsonfile", harnesspkg.EnvStore, env[harnesspkg.EnvStore])
	}
	if want := "path=" + r.path; env[harnesspkg.EnvStoreOpts] != want {
		t.Errorf("env %s = %q, want %q", harnesspkg.EnvStoreOpts, env[harnesspkg.EnvStoreOpts], want)
	}
}
