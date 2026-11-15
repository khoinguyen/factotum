package jsonfile

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/conformance"
)

// storeBase is a fixed instant, so the run and message fixtures sort and live
// deterministically.
var storeBase = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func openAt(t *testing.T, cfg store.Config) store.Backend {
	t.Helper()
	backend, err := Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = backend.Close() })
	return backend
}

func TestConformance(t *testing.T) {
	conformance.Run(t, func(t *testing.T) store.Backend {
		path := filepath.Join(t.TempDir(), "factotum.json")
		backend, err := Open(context.Background(), store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		return backend
	})
}

func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.json")
	ctx := context.Background()
	cfg := store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}

	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	project := &core.Project{ID: "prj-1", Name: "Acme", Policy: core.DefaultResolutionPolicy()}
	if err := first.Projects().Create(ctx, project); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	reloaded, err := second.Projects().Get(ctx, "prj-1")
	if err != nil {
		t.Fatalf("Get() after reopen error = %v", err)
	}
	if reloaded.Name != "Acme" {
		t.Fatalf("Get() after reopen Name = %q, want Acme", reloaded.Name)
	}
}

// TestLoadKeepsLegacyTaskIDKey pins the on-disk wire key of the ticket link.
// core.Artifact and core.Event are embedded directly in the document, so the
// Task->Ticket rename must not change their JSON key ("TaskID"), or a database
// written by an earlier version silently loses the link on the next rewrite.
func TestLoadKeepsLegacyTaskIDKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.json")
	legacy := `{
  "projects": [{"ID": "prj-1", "Name": "Acme"}],
  "tasks": [{"ID": "t-1", "ProjectID": "prj-1", "Kind": "task", "Title": "one", "Status": "todo"}],
  "artifacts": [{"ID": "art-1", "ProjectID": "prj-1", "TaskID": "t-1", "Kind": "memory", "Title": "m"}],
  "events": [{"ID": "ev-1", "ProjectID": "prj-1", "TaskID": "t-1", "Kind": "task.created", "Summary": "s"}]
}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	ctx := context.Background()
	be, err := Open(ctx, store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	artifact, err := be.Artifacts().Get(ctx, "art-1")
	if err != nil {
		t.Fatalf("Artifacts().Get() error = %v", err)
	}
	if artifact.TicketID == nil || *artifact.TicketID != "t-1" {
		t.Fatalf("artifact ticket link = %v, want t-1", artifact.TicketID)
	}
	events, err := be.Events().List(ctx, store.EventFilter{})
	if err != nil {
		t.Fatalf("Events().List() error = %v", err)
	}
	if len(events) != 1 || events[0].TicketID == nil || *events[0].TicketID != "t-1" {
		t.Fatalf("event ticket link = %+v, want t-1", events)
	}

	if err := be.Projects().Create(ctx, &core.Project{ID: "prj-2", Name: "Beta"}); err != nil {
		t.Fatalf("Projects().Create() error = %v", err)
	}
	if err := be.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !strings.Contains(string(data), `"TaskID": "t-1"`) {
		t.Fatalf("rewritten document lost the legacy TaskID key:\n%s", data)
	}
}

func TestLoadRejectsCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "factotum.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := Open(context.Background(), store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}); err == nil {
		t.Fatal("Open() error = nil, want parse error")
	}
}

// TestHubAndCLIKeepEveryWrite pins the two-process hub path: an `ft serve` hub
// and a local `ft` CLI hold their own handle on one jsonfile store, and each
// must see the other's writes. Before the per-operation file lock, each handle
// rewrote the whole document from the snapshot it loaded at Open, so the CLI
// erased the hub's run registration (and the hub could not claim the CLI's
// message) — the silent loss QA hit with a docker run.
func TestHubAndCLIKeepEveryWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	cfg := store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}

	hub := openAt(t, cfg)
	cli := openAt(t, cfg)

	const project = core.ProjectID("prj-1")
	run := &core.Run{
		ID:         "run-1",
		ProjectID:  project,
		ActorID:    "builder-t-1",
		Host:       "hub.example",
		SeenAt:     storeBase,
		LeaseUntil: storeBase.Add(time.Hour),
	}
	if _, err := hub.Runs().Register(ctx, run); err != nil {
		t.Fatalf("hub Runs().Register() error = %v", err)
	}

	message := &core.Message{
		ID:        "msg-1",
		ProjectID: project,
		To:        core.RunAddress(run.ID),
		Body:      "handoff",
		State:     core.MessageQueued,
		CreatedAt: storeBase,
		UpdatedAt: storeBase,
	}
	if err := cli.Messages().Create(ctx, message); err != nil {
		t.Fatalf("cli Messages().Create() error = %v", err)
	}

	claimed, err := hub.Messages().Claim(ctx, store.ClaimRequest{ProjectID: project, RunID: run.ID})
	if err != nil {
		t.Fatalf("hub Messages().Claim() error = %v", err)
	}
	if claimed == nil || claimed.ID != message.ID {
		t.Fatalf("hub Messages().Claim() = %+v, want %s", claimed, message.ID)
	}

	runs, err := cli.Runs().List(ctx, store.RunFilter{ProjectID: project})
	if err != nil {
		t.Fatalf("cli Runs().List() error = %v", err)
	}
	if len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("cli Runs().List() = %+v, want run %s", runs, run.ID)
	}

	fresh := openAt(t, cfg)
	freshRuns, err := fresh.Runs().List(ctx, store.RunFilter{ProjectID: project})
	if err != nil {
		t.Fatalf("fresh Runs().List() error = %v", err)
	}
	if len(freshRuns) != 1 {
		t.Fatalf("fresh Runs().List() = %+v, want the hub's run", freshRuns)
	}
	freshMessages, err := fresh.Messages().List(ctx, store.MessageFilter{ProjectID: project})
	if err != nil {
		t.Fatalf("fresh Messages().List() error = %v", err)
	}
	if len(freshMessages) != 1 || freshMessages[0].ID != message.ID {
		t.Fatalf("fresh Messages().List() = %+v, want the CLI's message", freshMessages)
	}
}

// TestConcurrentWritersLoseNothing writes through several handles at once and
// requires every append to survive. flock is per open file description, so a
// handle contends exactly like a separate process. Each write must re-read the
// document under the file lock instead of persisting the snapshot the handle
// loaded at Open.
func TestConcurrentWritersLoseNothing(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	cfg := store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}

	const writers, perWriter = 8, 4
	backends := make([]store.Backend, writers)
	for i := range backends {
		backends[i] = openAt(t, cfg)
	}

	var wg sync.WaitGroup
	for w, backend := range backends {
		wg.Add(1)
		go func(w int, backend store.Backend) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				event := &core.Event{
					ID:        core.EventID(fmt.Sprintf("ev-%d-%d", w, i)),
					ProjectID: "prj-1",
					Kind:      core.EventKind("task.created"),
					Summary:   "s",
					CreatedAt: storeBase,
				}
				if err := backend.Events().Append(ctx, event); err != nil {
					t.Errorf("writer %d Events().Append() error = %v", w, err)
					return
				}
			}
		}(w, backend)
	}
	wg.Wait()

	events, err := openAt(t, cfg).Events().List(ctx, store.EventFilter{})
	if err != nil {
		t.Fatalf("Events().List() error = %v", err)
	}
	if len(events) != writers*perWriter {
		t.Fatalf("Events().List() kept %d of %d writes", len(events), writers*perWriter)
	}
}

// TestLockIsASiblingFileThatOutlivesTheDocument pins the property the fix
// rests on: writes replace the document by rename, so the lock must live on
// its own file and keep its inode across a write. Locking the document, or
// persisting in place, would silently reintroduce the lost-write bug.
func TestLockIsASiblingFileThatOutlivesTheDocument(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	be := openAt(t, store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})

	project := &core.Project{ID: "prj-1", Name: "Acme"}
	if err := be.Projects().Create(ctx, project); err != nil {
		t.Fatalf("Projects().Create() error = %v", err)
	}
	lockBefore, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("Stat(lock file) error = %v", err)
	}
	documentBefore, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(document) error = %v", err)
	}

	project.Name = "Acme Renamed"
	if err := be.Projects().Update(ctx, project); err != nil {
		t.Fatalf("Projects().Update() error = %v", err)
	}
	lockAfter, err := os.Stat(path + ".lock")
	if err != nil {
		t.Fatalf("Stat(lock file) after write error = %v", err)
	}
	documentAfter, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(document) after write error = %v", err)
	}

	if os.SameFile(documentBefore, documentAfter) {
		t.Fatal("a write kept the document inode; the lock file exists because writes rename")
	}
	if !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("the lock file was replaced; a lock on the replaced inode would not exclude the next writer")
	}
}

// TestConcurrentClaimersTakeEachMessageOnce pins the claim compare-and-swap
// across writers: one handle per claimer, and no message is delivered twice.
func TestConcurrentClaimersTakeEachMessageOnce(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "factotum.json")
	cfg := store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}

	const project = core.ProjectID("prj-1")
	actor := core.ActorID("builder-t-1")
	const messages = 12
	seed := openAt(t, cfg)
	for i := 0; i < messages; i++ {
		at := storeBase.Add(time.Duration(i) * time.Second)
		message := &core.Message{
			ID:        core.MessageID(fmt.Sprintf("msg-%02d", i)),
			ProjectID: project,
			To:        core.ActorAddress(actor),
			Body:      "handoff",
			State:     core.MessageQueued,
			CreatedAt: at,
			UpdatedAt: at,
		}
		if err := seed.Messages().Create(ctx, message); err != nil {
			t.Fatalf("Messages().Create() error = %v", err)
		}
	}

	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		taken = map[core.MessageID]int{}
	)
	for i := 0; i < messages; i++ {
		claimer := openAt(t, cfg)
		runID := core.RunID(fmt.Sprintf("run-%02d", i))
		wg.Add(1)
		go func() {
			defer wg.Done()
			message, err := claimer.Messages().Claim(ctx, store.ClaimRequest{
				ProjectID: project,
				RunID:     runID,
				Recipient: store.ClaimRecipient{ActorID: &actor},
			})
			if errors.Is(err, core.ErrNotFound) {
				return
			}
			if err != nil {
				t.Errorf("Messages().Claim() error = %v", err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			taken[message.ID]++
		}()
	}
	wg.Wait()

	if len(taken) != messages {
		t.Fatalf("claimers took %d of %d messages", len(taken), messages)
	}
	for id, count := range taken {
		if count != 1 {
			t.Fatalf("message %s was delivered %d times", id, count)
		}
	}
}
