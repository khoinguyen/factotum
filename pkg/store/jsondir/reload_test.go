package jsondir

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// A jsondir is opened once by a long-running process (an `ft serve` hub or the
// `ft up` daemon) while short-lived `ft` processes write to the same directory.
// The in-memory index those processes build at Open must not be the hub's only
// view: every operation re-reads what changed on disk, under the same advisory
// lock the promotion guard uses. Two handles on one directory are two open file
// descriptions, so flock makes them contend exactly like two processes (see
// pkg/store/internal/filelock); only a second process would prove the platform,
// not the backend, which is what internal/serve/reload_test.go pins.

// reloadBase is a fixed instant, so fixtures sort and persist deterministically.
var reloadBase = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)

// openReloadHandle opens another handle at dir, like a separate ft process.
func openReloadHandle(t *testing.T, dir string) store.Backend {
	t.Helper()
	backend, err := Open(context.Background(), store.Config{Backend: "jsondir", Options: map[string]string{"path": dir}})
	if err != nil {
		t.Fatalf("Open(%s) error = %v", dir, err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

// TestSecondHandleSeesEveryWrite pins the hub's read side: a handle that opened
// the directory before another handle wrote serves everything that handle wrote,
// for every entity kind. Before every operation re-read under the lock, the hub
// kept serving its Open-time snapshot, so `ft serve` on a jsondir never showed
// the CLI's work.
func TestSecondHandleSeesEveryWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir) // opened first, like a running serve
	cli := openReloadHandle(t, dir)

	const project = core.ProjectID("prj-1")
	if err := cli.Projects().Create(ctx, &core.Project{ID: project, Name: "Acme"}); err != nil {
		t.Fatalf("cli Projects().Create() error = %v", err)
	}
	task := &core.Ticket{ID: "t-1", ProjectID: project, Kind: core.KindTask, Title: "written by the cli", Status: core.StatusTodo}
	if err := cli.Tickets().Create(ctx, task); err != nil {
		t.Fatalf("cli Tickets().Create() error = %v", err)
	}
	if err := cli.Actors().Create(ctx, &core.Actor{ID: "a-1", Kind: core.ActorAgent, Name: "bot"}); err != nil {
		t.Fatalf("cli Actors().Create() error = %v", err)
	}
	if err := cli.Artifacts().Create(ctx, &core.Artifact{
		ID: "art-1", ProjectID: project, Kind: core.ArtifactDoc, Title: "cli doc", CreatedAt: reloadBase, UpdatedAt: reloadBase,
	}); err != nil {
		t.Fatalf("cli Artifacts().Create() error = %v", err)
	}
	if err := cli.Messages().Create(ctx, &core.Message{
		ID: "msg-1", ProjectID: project, To: core.RunAddress("run-1"), Body: "handoff", State: core.MessageQueued, CreatedAt: reloadBase, UpdatedAt: reloadBase,
	}); err != nil {
		t.Fatalf("cli Messages().Create() error = %v", err)
	}
	if _, err := cli.Runs().Register(ctx, &core.Run{
		ID: "run-1", ProjectID: project, ActorID: "bot", Host: "hub.example", SeenAt: reloadBase, LeaseUntil: reloadBase.Add(time.Hour),
	}); err != nil {
		t.Fatalf("cli Runs().Register() error = %v", err)
	}
	if err := cli.Pipelines().Create(ctx, &core.Pipeline{
		ID: "pl-1", ProjectID: project, CaptureID: "i-1", State: core.PipelineQueued, CreatedAt: reloadBase, UpdatedAt: reloadBase,
	}); err != nil {
		t.Fatalf("cli Pipelines().Create() error = %v", err)
	}
	if err := cli.Events().Append(ctx, &core.Event{ID: "ev-1", ProjectID: project, Kind: "task.created", Summary: "cli write", CreatedAt: reloadBase}); err != nil {
		t.Fatalf("cli Events().Append() error = %v", err)
	}

	got, err := hub.Tickets().Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("hub Tickets().Get() error = %v", err)
	}
	if got.Title != task.Title {
		t.Fatalf("hub Tickets().Get() title = %q, want %q", got.Title, task.Title)
	}
	if projects, err := hub.Projects().List(ctx); err != nil || len(projects) != 1 {
		t.Fatalf("hub Projects().List() = %+v, %v; want the cli's project", projects, err)
	}
	if actors, err := hub.Actors().List(ctx); err != nil || len(actors) != 1 {
		t.Fatalf("hub Actors().List() = %+v, %v; want the cli's actor", actors, err)
	}
	if artifacts, err := hub.Artifacts().List(ctx, store.ArtifactFilter{}); err != nil || len(artifacts) != 1 {
		t.Fatalf("hub Artifacts().List() = %+v, %v; want the cli's artifact", artifacts, err)
	}
	claimed, err := hub.Messages().Claim(ctx, store.ClaimRequest{ProjectID: project, RunID: "run-1"})
	if err != nil {
		t.Fatalf("hub Messages().Claim() error = %v", err)
	}
	if claimed == nil || claimed.ID != "msg-1" {
		t.Fatalf("hub Messages().Claim() = %+v, want the cli's message", claimed)
	}
	if runs, err := hub.Runs().List(ctx, store.RunFilter{ProjectID: project}); err != nil || len(runs) != 1 {
		t.Fatalf("hub Runs().List() = %+v, %v; want the cli's run", runs, err)
	}
	if pipelines, err := hub.Pipelines().List(ctx, store.PipelineFilter{ProjectID: project}); err != nil || len(pipelines) != 1 {
		t.Fatalf("hub Pipelines().List() = %+v, %v; want the cli's pipeline", pipelines, err)
	}
	if events, err := hub.Events().List(ctx, store.EventFilter{}); err != nil || len(events) != 1 {
		t.Fatalf("hub Events().List() = %+v, %v; want the cli's event", events, err)
	}
}

// TestSecondHandleKeepsBothSidesWrites pins the hub's write side: a capture the
// hub stores applies to the directory as the CLI left it. Every record owns its
// own file, so nothing is erased, but the hub must still see the CLI's task in
// the index it writes from, and a fresh handle must see both.
func TestSecondHandleKeepsBothSidesWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)
	cli := openReloadHandle(t, dir)

	const project = core.ProjectID("prj-1")
	if err := cli.Tickets().Create(ctx, &core.Ticket{ID: "t-cli", ProjectID: project, Kind: core.KindTask, Title: "cli task", Status: core.StatusTodo}); err != nil {
		t.Fatalf("cli Tickets().Create() error = %v", err)
	}
	if err := hub.Tickets().Create(ctx, &core.Ticket{ID: "t-hub", ProjectID: project, Kind: core.KindIdea, Title: "hub capture", Status: core.StatusTodo}); err != nil {
		t.Fatalf("hub Tickets().Create() error = %v", err)
	}

	fresh := openReloadHandle(t, dir)
	tasks, err := fresh.Tickets().List(ctx, store.TicketFilter{ProjectID: project})
	if err != nil {
		t.Fatalf("fresh Tickets().List() error = %v", err)
	}
	titles := make(map[core.TicketID]string, len(tasks))
	for _, task := range tasks {
		titles[task.ID] = task.Title
	}
	if titles["t-cli"] != "cli task" {
		t.Fatalf("the cli's task is gone after the hub wrote: %+v", titles)
	}
	if titles["t-hub"] != "hub capture" {
		t.Fatalf("the hub's capture is not in the directory: %+v", titles)
	}

	hubTasks, err := hub.Tickets().List(ctx, store.TicketFilter{ProjectID: project})
	if err != nil {
		t.Fatalf("hub Tickets().List() error = %v", err)
	}
	if len(hubTasks) != 2 {
		t.Fatalf("hub Tickets().List() = %d tasks, want both sides' writes", len(hubTasks))
	}
}

// TestSecondHandleKeepsEventOrder pins the append log across handles. Events are
// appended to numbered shards by position, so a hub that keeps its Open-time
// event slice both serves a short log and appends to the wrong shard. Reloading
// must replace the slice, not extend it, or every refresh duplicates history.
func TestSecondHandleKeepsEventOrder(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)
	cli := openReloadHandle(t, dir)

	addEvent := func(backend store.Backend, id, summary string) {
		t.Helper()
		if err := backend.Events().Append(ctx, &core.Event{ID: core.EventID(id), ProjectID: "prj-1", Kind: "task.created", Summary: summary, CreatedAt: reloadBase}); err != nil {
			t.Fatalf("Events().Append(%s) error = %v", id, err)
		}
	}
	addEvent(cli, "ev-1", "cli one")
	addEvent(cli, "ev-2", "cli two")
	addEvent(hub, "ev-3", "hub three")

	// List newest first, and read twice: a reload that appends to the slice it
	// already holds would return duplicates on the second read.
	for range 2 {
		events, err := hub.Events().List(ctx, store.EventFilter{})
		if err != nil {
			t.Fatalf("hub Events().List() error = %v", err)
		}
		want := []core.EventID{"ev-3", "ev-2", "ev-1"}
		if len(events) != len(want) {
			t.Fatalf("hub Events().List() = %d events, want %d: %+v", len(events), len(want), events)
		}
		for i, id := range want {
			if events[i].ID != id {
				t.Fatalf("hub Events().List()[%d] = %s, want %s", i, events[i].ID, id)
			}
		}
	}
}

// TestReloadSeesFilesWrittenOutsideTheStore pins the other half of the contract:
// a file added or replaced by something other than this process (a hand edit, a
// git checkout, a script) is served too. Detection is the generation stamp plus
// each loaded directory's mtime, and a new file changes its directory.
func TestReloadSeesFilesWrittenOutsideTheStore(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)

	handWritten := core.Ticket{ID: "t-hand", ProjectID: "prj-1", Kind: core.KindTask, Title: "written by hand", Status: core.StatusTodo}
	data, err := encodeTask(handWritten, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	if err := writeFileAtomic(filepath.Join(dir, "tasks", "t-hand.md"), data); err != nil {
		t.Fatalf("write hand task: %v", err)
	}

	task, err := hub.Tickets().Get(ctx, "t-hand")
	if err != nil {
		t.Fatalf("hub Tickets().Get(t-hand) error = %v", err)
	}
	if task.Title != handWritten.Title {
		t.Fatalf("hub Tickets().Get(t-hand) title = %q, want %q", task.Title, handWritten.Title)
	}
}

// TestGenerationStampDetectsWriteWithoutDirectoryChange pins why the store keeps
// a generation stamp beside its lock. A record update replaces the record's file
// by rename, and on a filesystem whose directory timestamps are coarse that can
// leave the directory's mtime unchanged; the stamp's content still differs, so a
// second handle sees the update on any filesystem.
func TestGenerationStampDetectsWriteWithoutDirectoryChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)
	cli := openReloadHandle(t, dir)

	task := &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "before", Status: core.StatusTodo}
	if err := cli.Tickets().Create(ctx, task); err != nil {
		t.Fatalf("cli Tickets().Create() error = %v", err)
	}
	if _, err := hub.Tickets().Get(ctx, task.ID); err != nil {
		t.Fatalf("hub Tickets().Get() error = %v", err)
	}

	tasksDir := filepath.Join(dir, "tasks")
	before, err := os.Stat(tasksDir)
	if err != nil {
		t.Fatalf("Stat(%s) error = %v", tasksDir, err)
	}

	updated := *task
	updated.Title = "after"
	if err := cli.Tickets().Update(ctx, &updated); err != nil {
		t.Fatalf("cli Tickets().Update() error = %v", err)
	}
	// Pin the scenario the stamp exists for: the update leaves the directory's
	// mtime alone, so only the stamp can tell the hub the record changed.
	if err := os.Chtimes(tasksDir, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("Chtimes(%s) error = %v", tasksDir, err)
	}

	got, err := hub.Tickets().Get(ctx, task.ID)
	if err != nil {
		t.Fatalf("hub Tickets().Get() after update error = %v", err)
	}
	if got.Title != "after" {
		t.Fatalf("hub Tickets().Get() title = %q, want the second handle's update", got.Title)
	}
}

// TestReloadReportsBrokenRecord keeps the read path honest: a record another
// process left unparseable surfaces as an error instead of being served from the
// stale index, so a caller never reads past the damage.
func TestReloadReportsBrokenRecord(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)

	broken := filepath.Join(dir, "tasks", "t-broken.md")
	if err := os.MkdirAll(filepath.Dir(broken), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(broken, []byte("not a document\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", broken, err)
	}

	if _, err := hub.Tickets().List(ctx, store.TicketFilter{}); err == nil {
		t.Fatal("Tickets().List() error = nil, want the broken record reported")
	}
}

// TestReadsDoNotAnnounceAChange pins that only real writes move the generation.
// A dashboard refresh and an idle message claim run while a hub is open and must
// not mark the store changed, or every long-poll would make every other handle
// re-read the whole directory.
func TestReadsDoNotAnnounceAChange(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)
	cli := openReloadHandle(t, dir)

	if err := cli.Tickets().Create(ctx, &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "task", Status: core.StatusTodo}); err != nil {
		t.Fatalf("cli Tickets().Create() error = %v", err)
	}
	if _, err := hub.Tickets().List(ctx, store.TicketFilter{}); err != nil {
		t.Fatalf("hub Tickets().List() error = %v", err)
	}
	before, err := os.ReadFile(filepath.Clean(dir) + ".generation")
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}

	if _, err := hub.Tickets().List(ctx, store.TicketFilter{}); err != nil {
		t.Fatalf("hub Tickets().List() error = %v", err)
	}
	if _, err := hub.Tickets().Get(ctx, "t-1"); err != nil {
		t.Fatalf("hub Tickets().Get() error = %v", err)
	}
	claimed, err := hub.Messages().Claim(ctx, store.ClaimRequest{ProjectID: "prj-1", RunID: "run-1"})
	if !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("hub Messages().Claim() = %+v, %v; want nothing queued", claimed, err)
	}

	after, err := os.ReadFile(filepath.Clean(dir) + ".generation")
	if err != nil {
		t.Fatalf("read generation: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("a read-only operation moved the store's generation from %q to %q", before, after)
	}
}

// TestConcurrentHandlesLoseNothing appends from several handles at once — as
// many ft processes would — and requires every event to survive exactly once.
// Each append must apply to the log on disk under the exclusive lock, not to the
// shard position the handle loaded at Open, or two handles append into one shard
// slot and one write is lost.
func TestConcurrentHandlesLoseNothing(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	const handles, perHandle = 4, 5

	backends := make([]store.Backend, handles)
	for i := range backends {
		backends[i] = openReloadHandle(t, dir)
	}
	var wg sync.WaitGroup
	for h, backend := range backends {
		wg.Add(1)
		go func(h int, backend store.Backend) {
			defer wg.Done()
			for i := range perHandle {
				event := &core.Event{
					ID:        core.EventID(fmt.Sprintf("ev-%d-%d", h, i)),
					ProjectID: "prj-1",
					Kind:      "task.created",
					Summary:   "concurrent",
					CreatedAt: reloadBase,
				}
				if err := backend.Events().Append(ctx, event); err != nil {
					t.Errorf("handle %d Events().Append() error = %v", h, err)
					return
				}
			}
		}(h, backend)
	}
	wg.Wait()

	fresh := openReloadHandle(t, dir)
	events, err := fresh.Events().List(ctx, store.EventFilter{})
	if err != nil {
		t.Fatalf("fresh Events().List() error = %v", err)
	}
	if len(events) != handles*perHandle {
		t.Fatalf("fresh Events().List() = %d events, want %d", len(events), handles*perHandle)
	}
	seen := make(map[core.EventID]bool, len(events))
	for _, event := range events {
		if seen[event.ID] {
			t.Fatalf("event %s appears twice: %+v", event.ID, events)
		}
		seen[event.ID] = true
	}
}

// TestInPlaceEditIsNotNoticed pins the one kind of change the store does not
// notice, which is the documented difference from jsonfile (that backend parses
// its whole document on every operation, so it sees this too). An edit that
// rewrites an existing file in place leaves both stamps alone: it changes
// neither the directory's mtime nor the generation. A running handle keeps
// serving what it read until something it does notice arrives, or it restarts —
// which is the contract the package doc and README state. If detection
// improves, this test is the thing to change.
func TestInPlaceEditIsNotNoticed(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)
	cli := openReloadHandle(t, dir)

	task := &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "before", Status: core.StatusTodo}
	if err := cli.Tickets().Create(ctx, task); err != nil {
		t.Fatalf("cli Tickets().Create() error = %v", err)
	}
	if got, err := hub.Tickets().Get(ctx, task.ID); err != nil || got.Title != "before" {
		t.Fatalf("hub Tickets().Get() = %+v, %v; want the task as written", got, err)
	}

	edited := *task
	edited.Title = "edited in place"
	data, err := encodeTask(edited, nil)
	if err != nil {
		t.Fatalf("encodeTask() error = %v", err)
	}
	// os.WriteFile truncates and rewrites the existing file: no rename, so the
	// tasks directory keeps its mtime, and nothing bumps the generation.
	if err := os.WriteFile(filepath.Join(dir, "tasks", "t-1.md"), data, 0o644); err != nil {
		t.Fatalf("in-place write error = %v", err)
	}
	if got, err := hub.Tickets().Get(ctx, task.ID); err != nil || got.Title != "before" {
		t.Fatalf("hub Tickets().Get() after an in-place edit = %+v, %v; want the stale title the contract documents", got, err)
	}

	// The next change the store does notice re-reads everything, the edit
	// included: an added file moves the directory's mtime.
	added := &core.Ticket{ID: "t-2", ProjectID: "prj-1", Kind: core.KindTask, Title: "added later", Status: core.StatusTodo}
	if err := cli.Tickets().Create(ctx, added); err != nil {
		t.Fatalf("cli Tickets().Create(t-2) error = %v", err)
	}
	if got, err := hub.Tickets().Get(ctx, task.ID); err != nil || got.Title != "edited in place" {
		t.Fatalf("hub Tickets().Get() after a noticed change = %+v, %v; want the in-place edit picked up", got, err)
	}
}

// TestSecondHandlePromotionUsesItsWrite pins that promotion still answers from
// the directory, now that reloading covers every operation rather than only the
// promotion guard: a task another handle promoted is found, not promoted again.
func TestSecondHandlePromotionUsesItsWrite(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	hub := openReloadHandle(t, dir)
	cli := openReloadHandle(t, dir)

	capture := &core.Ticket{ID: "i-1", ProjectID: "prj-1", Kind: core.KindIdea, Title: "capture", Status: core.StatusTodo}
	if err := cli.Tickets().Create(ctx, capture); err != nil {
		t.Fatalf("cli Tickets().Create() error = %v", err)
	}
	promoted := &core.Ticket{ID: "t-1", ProjectID: "prj-1", Kind: core.KindTask, Title: "promoted", Status: core.StatusTodo, Deps: []core.TicketID{capture.ID}}
	if _, created, err := cli.Tickets().CreatePromoted(ctx, capture.ID, promoted); err != nil || !created {
		t.Fatalf("cli Tickets().CreatePromoted() = %v, %v; want created", created, err)
	}

	if existing, err := hub.Tickets().Promoted(ctx, capture.ID); err != nil || existing == nil {
		t.Fatalf("hub Tickets().Promoted() = %+v, %v; want the cli's promotion", existing, err)
	}
	again := &core.Ticket{ID: "t-2", ProjectID: "prj-1", Kind: core.KindTask, Title: "promoted again", Status: core.StatusTodo, Deps: []core.TicketID{capture.ID}}
	existing, created, err := hub.Tickets().CreatePromoted(ctx, capture.ID, again)
	if err != nil {
		t.Fatalf("hub Tickets().CreatePromoted() error = %v", err)
	}
	if created || existing == nil || existing.ID != promoted.ID {
		t.Fatalf("hub Tickets().CreatePromoted() = %+v, %v; want the existing promotion %s", existing, created, promoted.ID)
	}
}
