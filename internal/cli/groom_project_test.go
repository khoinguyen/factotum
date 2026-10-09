package cli

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/internal/groom"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/core"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/registry"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/builtins"
)

// openRegisteredStore opens the store the machine config registers for a
// project, so a cross-project write can be read back independently of the CLI.
// The caller closes it.
func openRegisteredStore(t *testing.T, userPath, projectID string) store.Backend {
	t.Helper()
	st, ok, err := config.StoreFor(userPath, projectID)
	if err != nil {
		t.Fatalf("StoreFor(%s) error = %v", projectID, err)
	}
	if !ok {
		t.Fatalf("no store registered for project %s", projectID)
	}
	reg := registry.New[store.Factory]()
	builtins.RegisterAll(reg)
	factory, err := reg.MustLookup(st.Backend)
	if err != nil {
		t.Fatalf("lookup backend %s: %v", st.Backend, err)
	}
	backend, err := factory(context.Background(), store.Config{Backend: st.Backend, Options: st.Options})
	if err != nil {
		t.Fatalf("open store for %s: %v", projectID, err)
	}
	return backend
}

// registerProjectWithIdea registers a second project's own store in the machine
// config and seeds the project (with one local repo) plus an open idea in it, so
// a cross-project `ft groom -p <id>` has a real target graph. It returns the
// project id, the store's db path, and the idea id.
func registerProjectWithIdea(t *testing.T, r *runner, name string) (projectID, dbPath, ideaID string) {
	t.Helper()
	projectID = firstField(t, r.run("project", "create", name))
	dbPath = filepath.Join(t.TempDir(), projectID+".json")
	mustWrite(t, r.userPath, "[projects."+projectID+".store]\nbackend = \"jsonfile\"\n"+
		"[projects."+projectID+".store.options]\npath = \""+dbPath+"\"\n")

	backend := openRegisteredStore(t, r.userPath, projectID)
	defer func() { _ = backend.Close() }()
	ctx := context.Background()
	if _, err := app.NewProjectService(backend, app.SystemClock{}, app.RandomIDGen{}).CreateWithID(
		ctx, core.ProjectID(projectID), name, "", []core.Repository{{Name: "repo", Path: t.TempDir()}}); err != nil {
		t.Fatalf("seed project %s: %v", projectID, err)
	}
	idea, err := app.NewTicketService(backend, app.SystemClock{}, app.RandomIDGen{}).Add(
		ctx, app.TicketInput{ProjectID: core.ProjectID(projectID), Kind: core.KindIdea, Title: "Maybe cache"})
	if err != nil {
		t.Fatalf("seed idea in %s: %v", projectID, err)
	}
	return projectID, dbPath, string(idea.ID)
}

func listProjectArtifacts(t *testing.T, userPath, projectID string) []*core.Artifact {
	t.Helper()
	backend := openRegisteredStore(t, userPath, projectID)
	defer func() { _ = backend.Close() }()
	arts, err := backend.Artifacts().List(context.Background(), store.ArtifactFilter{ProjectID: core.ProjectID(projectID)})
	if err != nil {
		t.Fatalf("list artifacts for %s: %v", projectID, err)
	}
	return arts
}

// TestGroomDataDirResolution pins which store a project's grooming data is read
// from: the configured store for the configured project (even with a
// [projects.<id>] entry), the machine config's entry for another project, the
// file's top-level [store] when the project has no entry, and the configured
// store only when the machine config names no store at all. This is the single
// resolution list, show, capture, and review share.
func TestGroomDataDirResolution(t *testing.T) {
	dir := t.TempDir()
	configured := filepath.Join(dir, "configured", "db.json")
	entry := filepath.Join(dir, "entry", "db.json")
	machine := filepath.Join(dir, "machine", "db.json")
	userPath := filepath.Join(dir, "user.toml")
	mustWrite(t, userPath, "[store]\nbackend = \"jsonfile\"\n[store.options]\npath = \""+machine+"\"\n"+
		"[projects.alpha.store]\nbackend = \"jsonfile\"\n[projects.alpha.store.options]\npath = \""+entry+"\"\n"+
		"[projects.beta.store]\nbackend = \"jsonfile\"\n[projects.beta.store.options]\npath = \""+entry+"\"\n")

	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	deps.Config = config.Config{
		Project: "alpha",
		Store:   config.Store{Backend: "jsonfile", Options: map[string]string{"path": configured}},
	}
	deps.UserConfigPath = userPath

	cases := []struct {
		name    string
		flag    string
		wantDir string
	}{
		{"configured project ignores its registry entry", "alpha", filepath.Dir(configured)},
		{"registry entry", "beta", filepath.Dir(entry)},
		{"top-level store fallback", "gamma", filepath.Dir(machine)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := deps.groomDataDir(tc.flag)
			if err != nil {
				t.Fatalf("groomDataDir(%s) error = %v", tc.flag, err)
			}
			if got != tc.wantDir {
				t.Fatalf("groomDataDir(%s) = %q, want %q", tc.flag, got, tc.wantDir)
			}
		})
	}

	emptyUser := filepath.Join(dir, "empty.toml")
	mustWrite(t, emptyUser, "default_project = \"alpha\"\n")
	bare := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, nil)
	bare.Config = deps.Config
	bare.UserConfigPath = emptyUser
	got, err := bare.groomDataDir("delta")
	if err != nil {
		t.Fatalf("groomDataDir(delta) error = %v", err)
	}
	if want := filepath.Dir(configured); got != want {
		t.Fatalf("groomDataDir(delta) = %q, want the configured store dir %q", got, want)
	}
}

// TestGroomProjectFlagCapturesIntoAnotherProjectsStore pins that a cross-project
// `ft groom -p b` from an `a`-configured checkout records the session into b: its
// manifest and outputs under b's data dir and its artifacts in b's store, so
// `ft groom list/show -p b` find it.
func TestGroomProjectFlagCapturesIntoAnotherProjectsStore(t *testing.T) {
	r := newRunner(t)
	_, cfgPath := tasklessContext(t, r)
	target, dbPath, _ := registerProjectWithIdea(t, r, "Beta")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	out := r.run("--config", cfgPath, "groom", "-p", target, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())
	sessionID := firstField(t, out)

	dataDir := filepath.Dir(dbPath)
	if !fileExists(filepath.Join(dataDir, "grooming-sessions", sessionID, "report.md")) {
		t.Fatalf("groom -p %s did not record the session under the target data dir %s", target, dataDir)
	}
	if got := r.run("--config", cfgPath, "groom", "list", "-p", target); !strings.Contains(got, sessionID) {
		t.Fatalf("groom list -p %s did not find the captured session %s:\n%s", target, sessionID, got)
	}
	show := r.run("--config", cfgPath, "groom", "show", sessionID, "-p", target)
	for _, want := range []string{"session: " + sessionID, "project: " + target, "Grooming report"} {
		if !strings.Contains(show, want) {
			t.Fatalf("groom show -p %s missing %q:\n%s", target, want, show)
		}
	}
	if arts := listProjectArtifacts(t, r.userPath, target); len(arts) < 5 {
		t.Fatalf("target store has %d artifacts, want the session's five outputs", len(arts))
	}
}

// TestGroomProjectFlagTargetsAnotherProjectsStoreEnv pins that the launched
// session's child ft reads the target project's store, not the configured one:
// the session env names b's project and b's store path. Without it, a real
// session would create its tasks and docs in the configured store.
func TestGroomProjectFlagTargetsAnotherProjectsStoreEnv(t *testing.T) {
	r := newRunner(t)
	_, cfgPath := tasklessContext(t, r)
	target, dbPath, _ := registerProjectWithIdea(t, r, "Beta")

	promptPath := filepath.Join(t.TempDir(), "prompt.md")
	mustWrite(t, promptPath, "# Grooming session prompt\n\nYou are the team lead.\n")

	backend := writingGroomBackend(t)
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode")

	r.run("--config", cfgPath, "groom", "-p", target, "--prompt-file", promptPath,
		"--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	specs := backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	env := specs[0].Env
	if env[harnesspkg.EnvProject] != target {
		t.Errorf("env %s = %q, want the target project %q", harnesspkg.EnvProject, env[harnesspkg.EnvProject], target)
	}
	if env[harnesspkg.EnvStore] != "jsonfile" {
		t.Errorf("env %s = %q, want jsonfile", harnesspkg.EnvStore, env[harnesspkg.EnvStore])
	}
	if want := "path=" + dbPath; env[harnesspkg.EnvStoreOpts] != want {
		t.Errorf("env %s = %q, want %q", harnesspkg.EnvStoreOpts, env[harnesspkg.EnvStoreOpts], want)
	}
}

// TestGroomReviewProjectFlagRecordsIntoAnotherProjectsStore pins that
// `ft groom review -p b` records the review into b: the verdict and body land in
// b's session dir and store, so `ft groom show -p b` reads them back.
func TestGroomReviewProjectFlagRecordsIntoAnotherProjectsStore(t *testing.T) {
	r := newRunner(t)
	_, cfgPath := tasklessContext(t, r)
	target, dbPath, ideaID := registerProjectWithIdea(t, r, "Beta")

	dataDir := filepath.Dir(dbPath)
	sessionID := "groom-" + target
	if err := groom.WriteSession(dataDir, groom.SessionRecord{
		ID:        sessionID,
		Project:   target,
		Mode:      "headless",
		CreatedAt: time.Now().UTC(),
		Scope:     []groom.ScopeItem{{ID: ideaID, Kind: "idea", Title: "Maybe cache"}},
	}); err != nil {
		t.Fatalf("WriteSession error = %v", err)
	}
	mustWrite(t, groom.ReportPath(dataDir, sessionID), "# Grooming report - "+target+"\n")
	mustWrite(t, groom.DeferredQuestionsPath(dataDir, sessionID), "# Deferred questions - "+target+"\n")

	reviewFile := filepath.Join(t.TempDir(), "review.md")
	mustWrite(t, reviewFile, "# Review\n\nNeeds rework: split the module.\n")

	out := r.run("--config", cfgPath, "groom", "review", sessionID, "-p", target,
		"--verdict", "needs-rework", "-f", reviewFile)
	for _, want := range []string{"session: " + sessionID, "verdict: needs-rework", "project: " + target} {
		if !strings.Contains(out, want) {
			t.Fatalf("groom review -p output missing %q:\n%s", want, out)
		}
	}

	show := r.run("--config", cfgPath, "groom", "show", sessionID, "-p", target)
	for _, want := range []string{"review_verdict: needs-rework", "split the module"} {
		if !strings.Contains(show, want) {
			t.Fatalf("groom show -p %s did not read the review from the target store (%q missing):\n%s", target, want, show)
		}
	}
}

// TestGroomReviewWithoutProjectUsesConfiguredStore pins the default resolver:
// without -p, review records into the configured project's store and dir.
func TestGroomReviewWithoutProjectUsesConfiguredStore(t *testing.T) {
	r := newRunner(t)
	projectID, cfgPath := tasklessContext(t, r)
	ideaID := firstField(t, r.run("idea", "create", "-p", projectID, "-t", "Maybe cache"))

	dataDir := filepath.Dir(r.path)
	sessionID := "groom-configured"
	if err := groom.WriteSession(dataDir, groom.SessionRecord{
		ID:        sessionID,
		Project:   projectID,
		Mode:      "headless",
		CreatedAt: time.Now().UTC(),
		Scope:     []groom.ScopeItem{{ID: ideaID, Kind: "idea", Title: "Maybe cache"}},
	}); err != nil {
		t.Fatalf("WriteSession error = %v", err)
	}

	reviewFile := filepath.Join(t.TempDir(), "review.md")
	mustWrite(t, reviewFile, "# Review\n\nApproved.\n")

	out := r.run("--config", cfgPath, "groom", "review", sessionID, "--verdict", "approve", "-f", reviewFile)
	for _, want := range []string{"session: " + sessionID, "verdict: approve", "project: " + projectID} {
		if !strings.Contains(out, want) {
			t.Fatalf("groom review output missing %q:\n%s", want, out)
		}
	}
}
