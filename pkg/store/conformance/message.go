package conformance

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// msgBase is a fixed, well-in-the-past instant so ordering and pruning are
// deterministic.
var msgBase = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

func queuedMessage(id string, project core.ProjectID, to core.Address, at time.Time) *core.Message {
	return &core.Message{
		ID:        core.MessageID(id),
		ProjectID: project,
		To:        to,
		Body:      "body " + id,
		State:     core.MessageQueued,
		CreatedAt: at,
		UpdatedAt: at,
	}
}

func actorRecipient(project core.ProjectID, id core.ActorID) store.ClaimRequest {
	return store.ClaimRequest{ProjectID: project, Recipient: store.ClaimRecipient{ActorID: &id}}
}

func testMessage(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Messages()

	t.Run("roundtrip", func(t *testing.T) { testMessageRoundtrip(t, repo, ctx) })
	t.Run("validate", func(t *testing.T) { testMessageValidate(t, repo, ctx) })
	t.Run("list", func(t *testing.T) { testMessageList(t, repo, ctx) })
	t.Run("claim", func(t *testing.T) { testMessageClaim(t, repo, ctx) })
	t.Run("claim-addressing", func(t *testing.T) { testMessageClaimAddressing(t, repo, ctx) })
	t.Run("claim-concurrency", func(t *testing.T) { testMessageClaimConcurrency(t, repo, ctx) })
	t.Run("ack", func(t *testing.T) { testMessageAck(t, repo, ctx) })
	t.Run("nack", func(t *testing.T) { testMessageNack(t, repo, ctx) })
	t.Run("requeue-expired", func(t *testing.T) { testMessageRequeueExpired(t, repo, ctx) })
	t.Run("prune", func(t *testing.T) { testMessagePrune(t, repo, ctx) })
}

func testMessageRoundtrip(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-rt")
	taskID := core.TicketID("t-1")
	from := core.ActorID("act-1")
	message := &core.Message{
		ID:        "msg-1",
		ProjectID: project,
		From:      &from,
		To:        core.TaskAddress(taskID),
		TaskID:    &taskID,
		Body:      "hello world",
		Links:     []core.Link{{Kind: core.LinkPR, URL: "https://example.test/pr/1"}},
		State:     core.MessageQueued,
		CreatedAt: msgBase,
		UpdatedAt: msgBase,
	}
	if err := repo.Create(ctx, message); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if err := repo.Create(ctx, message); !errors.Is(err, core.ErrAlreadyExists) {
		t.Fatalf("Create() duplicate error = %v, want ErrAlreadyExists", err)
	}

	got, err := repo.Get(ctx, "msg-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Body != "hello world" || got.To != core.TaskAddress(taskID) || got.TaskID == nil || *got.TaskID != taskID {
		t.Fatalf("Get() = %+v, want body/to/task preserved", got)
	}
	if got.From == nil || *got.From != from {
		t.Fatalf("Get().From = %v, want %q", got.From, from)
	}
	if len(got.Links) != 1 || got.Links[0].Kind != core.LinkPR {
		t.Fatalf("Get().Links = %v, want one pr link", got.Links)
	}
	if _, err := repo.Get(ctx, "missing"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Get() missing error = %v, want ErrNotFound", err)
	}

	got.Error = "note"
	got.State = core.MessageDelivered
	got.UpdatedAt = msgBase.Add(time.Minute)
	if err := repo.Update(ctx, got); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reloaded, err := repo.Get(ctx, "msg-1")
	if err != nil {
		t.Fatalf("Get() after update error = %v", err)
	}
	if reloaded.State != core.MessageDelivered || reloaded.Error != "note" {
		t.Fatalf("Get() after update = %+v, want delivered/note", reloaded)
	}
}

func testMessageValidate(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-v")
	taskID := core.TicketID("t-1")
	invalids := []struct {
		name    string
		message core.Message
	}{
		{"empty id", core.Message{ProjectID: project, To: core.ActorAddress("act-1"), Body: "x", State: core.MessageQueued, CreatedAt: msgBase}},
		{"empty project", core.Message{ID: "m", To: core.ActorAddress("act-1"), Body: "x", State: core.MessageQueued, CreatedAt: msgBase}},
		{"empty to", core.Message{ID: "m", ProjectID: project, Body: "x", State: core.MessageQueued, CreatedAt: msgBase}},
		{"unknown state", core.Message{ID: "m", ProjectID: project, To: core.ActorAddress("act-1"), Body: "x", State: "lost", CreatedAt: msgBase}},
		{"empty body", core.Message{ID: "m", ProjectID: project, To: core.ActorAddress("act-1"), Body: "  ", State: core.MessageQueued, CreatedAt: msgBase}},
		{"task disagreement", core.Message{ID: "m", ProjectID: project, To: core.TaskAddress("t-2"), TaskID: &taskID, Body: "x", State: core.MessageQueued, CreatedAt: msgBase}},
	}
	for _, tt := range invalids {
		t.Run(tt.name, func(t *testing.T) {
			message := tt.message
			if err := repo.Create(ctx, &message); !errors.Is(err, core.ErrInvalid) {
				t.Fatalf("Create() error = %v, want ErrInvalid", err)
			}
		})
	}

	// The body limit is a pure Validate concern: the store does not know the
	// configured maximum.
	over := queuedMessage("big", project, core.ActorAddress("act-1"), msgBase)
	over.Body = strings.Repeat("x", 100)
	if err := over.Validate(10); !errors.Is(err, core.ErrInvalid) {
		t.Fatalf("Validate(limit) error = %v, want ErrInvalid", err)
	}
	if err := over.Validate(0); err != nil {
		t.Fatalf("Validate(no limit) error = %v, want nil", err)
	}
}

func testMessageList(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-list")
	other := core.ProjectID("prj-list-other")
	taskA := core.TicketID("t-a")
	taskB := core.TicketID("t-b")

	messages := []*core.Message{
		queuedMessage("l-1", project, core.ActorAddress("act-1"), msgBase),
		queuedMessage("l-2", project, core.RunAddress("run-1"), msgBase.Add(time.Second)),
		queuedMessage("l-3", project, core.TaskAddress(taskA), msgBase.Add(2*time.Second)),
		queuedMessage("l-4", other, core.ActorAddress("act-1"), msgBase.Add(3*time.Second)),
	}
	messages[2].TaskID = &taskA
	// A message resolved to run: but still carrying the originating task.
	messages[1].TaskID = &taskB
	messages[1].State = core.MessageDelivered
	messages[0].State = core.MessageRead

	for _, message := range messages {
		if err := repo.Create(ctx, message); err != nil {
			t.Fatalf("Create(%s) error = %v", message.ID, err)
		}
	}

	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project}, []core.MessageID{"l-1", "l-2", "l-3"})

	addr := core.ActorAddress("act-1")
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, To: &addr}, []core.MessageID{"l-1"})
	actor := core.ActorID("act-1")
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, Actor: &actor}, []core.MessageID{"l-1"})
	run := core.RunID("run-1")
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, Run: &run}, []core.MessageID{"l-2"})
	// The Task filter matches Message.TaskID regardless of To.
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, Task: &taskB}, []core.MessageID{"l-2"})
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, Task: &taskA}, []core.MessageID{"l-3"})
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, States: []core.MessageState{core.MessageDelivered}}, []core.MessageID{"l-2"})
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, Limit: 2}, []core.MessageID{"l-1", "l-2"})

	since := msgBase.Add(time.Second)
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: project, Since: &since}, []core.MessageID{"l-2", "l-3"})

	// No cross-project leakage.
	assertMessageIDs(t, repo, ctx, store.MessageFilter{ProjectID: other}, []core.MessageID{"l-4"})
}

func assertMessageIDs(t *testing.T, repo store.MessageRepo, ctx context.Context, filter store.MessageFilter, want []core.MessageID) {
	t.Helper()
	messages, err := repo.List(ctx, filter)
	if err != nil {
		t.Fatalf("List(%+v) error = %v", filter, err)
	}
	got := make([]core.MessageID, 0, len(messages))
	for _, message := range messages {
		got = append(got, message.ID)
	}
	if len(got) != len(want) {
		t.Fatalf("List(%+v) = %v, want %v", filter, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List(%+v) = %v, want %v", filter, got, want)
		}
	}
}

func testMessageClaim(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-claim")
	at := msgBase
	early := queuedMessage("c-1", project, core.ActorAddress("act-1"), at)
	late := queuedMessage("c-2", project, core.ActorAddress("act-1"), at.Add(time.Second))
	other := queuedMessage("c-3", project, core.ActorAddress("act-2"), at.Add(2*time.Second))
	for _, message := range []*core.Message{late, early, other} {
		if err := repo.Create(ctx, message); err != nil {
			t.Fatalf("Create(%s) error = %v", message.ID, err)
		}
	}

	req := actorRecipient(project, "act-1")
	req.RunID = "run-1"
	req.Lease = time.Minute
	before := time.Now()
	claimed, err := repo.Claim(ctx, req)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	after := time.Now()
	if claimed.ID != "c-1" {
		t.Fatalf("Claim() = %s, want oldest c-1", claimed.ID)
	}
	if claimed.State != core.MessageDelivered {
		t.Fatalf("Claim() state = %q, want delivered", claimed.State)
	}
	if claimed.RunID == nil || *claimed.RunID != "run-1" {
		t.Fatalf("Claim() RunID = %v, want run-1", claimed.RunID)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("Claim() Attempts = %d, want 1", claimed.Attempts)
	}
	if claimed.LeaseUntil == nil || !claimed.LeaseUntil.After(before) || claimed.LeaseUntil.After(after.Add(2*time.Minute)) {
		t.Fatalf("Claim() LeaseUntil = %v, want within the lease window", claimed.LeaseUntil)
	}
	if claimed.DeliveredAt == nil {
		t.Fatal("Claim() DeliveredAt = nil, want set")
	}

	// The claimed message is no longer queued; the next claim takes the next one.
	second, err := repo.Claim(ctx, req)
	if err != nil {
		t.Fatalf("second Claim() error = %v", err)
	}
	if second.ID != "c-2" {
		t.Fatalf("second Claim() = %s, want c-2", second.ID)
	}

	// A different actor only sees its own message.
	otherRecipient := actorRecipient(project, "act-2")
	otherRecipient.RunID = "run-2"
	third, err := repo.Claim(ctx, otherRecipient)
	if err != nil {
		t.Fatalf("third Claim() error = %v", err)
	}
	if third.ID != "c-3" {
		t.Fatalf("third Claim() = %s, want c-3", third.ID)
	}

	if _, err := repo.Claim(ctx, req); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Claim() empty error = %v, want ErrNotFound", err)
	}
}

func testMessageClaimAddressing(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-addr")
	taskID := core.TicketID("t-1")
	actorMsg := queuedMessage("a-1", project, core.ActorAddress("act-1"), msgBase)
	runMsg := queuedMessage("r-1", project, core.RunAddress("run-1"), msgBase.Add(time.Second))
	taskMsg := queuedMessage("t-1", project, core.TaskAddress(taskID), msgBase.Add(2*time.Second))
	taskMsg.TaskID = &taskID
	// Resolved to run: but still carrying the task link.
	resolved := queuedMessage("x-1", project, core.RunAddress("run-9"), msgBase.Add(3*time.Second))
	resolved.TaskID = &taskID

	for _, message := range []*core.Message{actorMsg, runMsg, taskMsg, resolved} {
		if err := repo.Create(ctx, message); err != nil {
			t.Fatalf("Create(%s) error = %v", message.ID, err)
		}
	}

	// An actor run claims the actor mailbox (and only that), in FIFO order. Its
	// own run id is unrelated so it cannot also claim the run: message below.
	actorRun := actorRecipient(project, "act-1")
	actorRun.RunID = "run-100"
	first, err := repo.Claim(ctx, actorRun)
	if err != nil {
		t.Fatalf("actor Claim() error = %v", err)
	}
	if first.ID != "a-1" {
		t.Fatalf("actor Claim() = %s, want a-1", first.ID)
	}
	if _, err := repo.Claim(ctx, actorRun); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("actor second Claim() error = %v, want ErrNotFound", err)
	}

	// A run claims its own run: address.
	runOwned := store.ClaimRequest{ProjectID: project, RunID: "run-1"}
	second, err := repo.Claim(ctx, runOwned)
	if err != nil {
		t.Fatalf("run Claim() error = %v", err)
	}
	if second.ID != "r-1" {
		t.Fatalf("run Claim() = %s, want r-1", second.ID)
	}

	// A run whose task differs does not claim another task's stored message.
	otherTask := core.TicketID("t-2")
	wrongRun := store.ClaimRequest{ProjectID: project, RunID: "run-2", Recipient: store.ClaimRecipient{TaskID: &otherTask}}
	if _, err := repo.Claim(ctx, wrongRun); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("wrong-run Claim() error = %v, want ErrNotFound (task fallback is only by matching TaskID)", err)
	}

	// The run whose TaskID matches claims the stored task: fallback.
	taskRun := store.ClaimRequest{ProjectID: project, RunID: "run-2", Recipient: store.ClaimRecipient{TaskID: &taskID}}
	third, err := repo.Claim(ctx, taskRun)
	if err != nil {
		t.Fatalf("task Claim() error = %v", err)
	}
	if third.ID != "t-1" {
		t.Fatalf("task Claim() = %s, want t-1", third.ID)
	}

	// The resolved run: message is claimable by its run and still matches the
	// Task list filter.
	filtered, err := repo.List(ctx, store.MessageFilter{ProjectID: project, Task: &taskID})
	if err != nil {
		t.Fatalf("List(task) error = %v", err)
	}
	// Both the stored task: message and the resolved run: message carry the
	// same TaskID, so both match the Task filter.
	if len(filtered) != 2 || filtered[0].ID != "t-1" || filtered[1].ID != "x-1" {
		t.Fatalf("List(task) = %v, want [t-1 x-1]", messageIDs(filtered))
	}
	resolvedRun := store.ClaimRequest{ProjectID: project, RunID: "run-9"}
	fourth, err := repo.Claim(ctx, resolvedRun)
	if err != nil {
		t.Fatalf("resolved Claim() error = %v", err)
	}
	if fourth.ID != "x-1" {
		t.Fatalf("resolved Claim() = %s, want x-1", fourth.ID)
	}
}

func testMessageClaimConcurrency(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-race")
	message := queuedMessage("race-1", project, core.ActorAddress("act-1"), msgBase)
	if err := repo.Create(ctx, message); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	const workers = 8
	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		mu      sync.Mutex
		winners int
		errs    []error
	)
	start.Add(1)
	done.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			claimed, err := repo.Claim(ctx, actorRecipient(project, "act-1"))
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil && claimed != nil:
				winners++
			case errors.Is(err, core.ErrNotFound):
			default:
				errs = append(errs, err)
			}
		}()
	}
	start.Done()
	done.Wait()

	if len(errs) > 0 {
		t.Fatalf("concurrent Claim() errors = %v", errs)
	}
	if winners != 1 {
		t.Fatalf("concurrent claims winners = %d, want exactly 1", winners)
	}
}

func testMessageAck(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-ack")
	message := queuedMessage("ack-1", project, core.ActorAddress("act-1"), msgBase)
	if err := repo.Create(ctx, message); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	// Ack on a non-delivered message errors.
	if err := repo.Ack(ctx, store.AckRequest{ID: "ack-1", RunID: "run-1", State: core.MessageRead}); err == nil {
		t.Fatal("Ack(queued) = nil, want error")
	}

	claim := actorRecipient(project, "act-1")
	claim.RunID = "run-1"
	claimed, err := repo.Claim(ctx, claim)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	// A non-owner cannot ack.
	if err := repo.Ack(ctx, store.AckRequest{ID: claimed.ID, RunID: "run-2", State: core.MessageRead}); err == nil {
		t.Fatal("Ack(wrong owner) = nil, want error")
	}
	if err := repo.Ack(ctx, store.AckRequest{ID: claimed.ID, RunID: "run-1", State: core.MessageRead}); err != nil {
		t.Fatalf("Ack(read) error = %v", err)
	}
	read, err := repo.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if read.State != core.MessageRead || read.ReadAt == nil {
		t.Fatalf("after Ack(read) = %+v, want read with ReadAt", read)
	}
	// Idempotent repeat.
	if err := repo.Ack(ctx, store.AckRequest{ID: claimed.ID, RunID: "run-1", State: core.MessageRead}); err != nil {
		t.Fatalf("repeat Ack(read) error = %v, want nil", err)
	}

	// Ack(failed) records the reason and is terminal.
	failed := queuedMessage("ack-2", project, core.ActorAddress("act-1"), msgBase.Add(time.Second))
	if err := repo.Create(ctx, failed); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := repo.Claim(ctx, claim); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repo.Ack(ctx, store.AckRequest{ID: "ack-2", RunID: "run-1", State: core.MessageFailed, Error: "boom"}); err != nil {
		t.Fatalf("Ack(failed) error = %v", err)
	}
	nacked, err := repo.Get(ctx, "ack-2")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if nacked.State != core.MessageFailed || nacked.Error != "boom" {
		t.Fatalf("after Ack(failed) = %+v, want failed/boom", nacked)
	}
}

func testMessageNack(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-nack")
	message := queuedMessage("nack-1", project, core.ActorAddress("act-1"), msgBase)
	if err := repo.Create(ctx, message); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	req := actorRecipient(project, "act-1")
	req.RunID = "run-1"

	// Under the attempt ceiling, Nack requeues with attempts retained.
	if _, err := repo.Claim(ctx, req); err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if err := repo.Nack(ctx, "nack-1", "run-1", "not now"); err != nil {
		t.Fatalf("Nack() error = %v", err)
	}
	requeued, err := repo.Get(ctx, "nack-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if requeued.State != core.MessageQueued || requeued.Attempts != 1 || requeued.RunID != nil {
		t.Fatalf("after Nack() = %+v, want queued with attempts retained", requeued)
	}
	if requeued.Error != "not now" {
		t.Fatalf("after Nack() Error = %q, want reason recorded", requeued.Error)
	}

	// After the ceiling, Nack parks it failed. Attempts already counted the
	// claim above, so only the remaining attempts run here.
	for i := 1; i < store.MaxMessageAttempts; i++ {
		if _, err := repo.Claim(ctx, req); err != nil {
			t.Fatalf("Claim() #%d error = %v", i, err)
		}
		if err := repo.Nack(ctx, "nack-1", "run-1", "still no"); err != nil {
			t.Fatalf("Nack() #%d error = %v", i, err)
		}
	}
	parked, err := repo.Get(ctx, "nack-1")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if parked.State != core.MessageFailed {
		t.Fatalf("after max attempts state = %q, want failed", parked.State)
	}
}

func testMessageRequeueExpired(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-exp")
	now := msgBase.Add(time.Hour)
	create := func(message *core.Message) {
		if err := repo.Create(ctx, message); err != nil {
			t.Fatalf("Create(%s) error = %v", message.ID, err)
		}
	}

	past := now.Add(-time.Minute)
	runID := core.RunID("run-1")

	expired := queuedMessage("exp-1", project, core.ActorAddress("act-1"), msgBase)
	expired.State = core.MessageDelivered
	expired.RunID = &runID
	expired.LeaseUntil = &past
	expired.Attempts = 1
	create(expired)

	exhausted := queuedMessage("exp-2", project, core.ActorAddress("act-1"), msgBase.Add(time.Second))
	exhausted.State = core.MessageDelivered
	exhausted.RunID = &runID
	exhausted.LeaseUntil = &past
	exhausted.Attempts = store.MaxMessageAttempts
	create(exhausted)

	// A read message is terminal and untouched.
	read := queuedMessage("exp-3", project, core.ActorAddress("act-1"), msgBase.Add(2*time.Second))
	read.State = core.MessageRead
	read.LeaseUntil = &past
	create(read)

	moved, err := repo.RequeueExpired(ctx, now, store.MaxMessageAttempts)
	if err != nil {
		t.Fatalf("RequeueExpired() error = %v", err)
	}
	if moved != 2 {
		t.Fatalf("RequeueExpired() moved = %d, want 2", moved)
	}

	requeued, err := repo.Get(ctx, "exp-1")
	if err != nil {
		t.Fatalf("Get(exp-1) error = %v", err)
	}
	if requeued.State != core.MessageQueued || requeued.Attempts != 1 || requeued.RunID != nil || requeued.LeaseUntil != nil {
		t.Fatalf("exp-1 after sweep = %+v, want queued with attempts retained", requeued)
	}
	failed, err := repo.Get(ctx, "exp-2")
	if err != nil {
		t.Fatalf("Get(exp-2) error = %v", err)
	}
	if failed.State != core.MessageFailed {
		t.Fatalf("exp-2 after sweep = %q, want failed", failed.State)
	}
	stillRead, err := repo.Get(ctx, "exp-3")
	if err != nil {
		t.Fatalf("Get(exp-3) error = %v", err)
	}
	if stillRead.State != core.MessageRead {
		t.Fatalf("exp-3 after sweep = %q, want read untouched", stillRead.State)
	}
}

func testMessagePrune(t *testing.T, repo store.MessageRepo, ctx context.Context) {
	t.Helper()
	const project = core.ProjectID("prj-prune")
	// Prune is global (no project scope), so these timestamps sit far from the
	// other subtests' messages to keep the count deterministic.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	young := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)

	messages := []*core.Message{
		queuedMessage("p-queued", project, core.ActorAddress("act-1"), old),
		queuedMessage("p-delivered", project, core.ActorAddress("act-1"), old),
		queuedMessage("p-read-old", project, core.ActorAddress("act-1"), old),
		queuedMessage("p-failed-old", project, core.ActorAddress("act-1"), old),
		queuedMessage("p-read-young", project, core.ActorAddress("act-1"), young),
	}
	messages[1].State = core.MessageDelivered
	messages[2].State = core.MessageRead
	messages[3].State = core.MessageFailed
	messages[4].State = core.MessageRead
	for _, message := range messages {
		if err := repo.Create(ctx, message); err != nil {
			t.Fatalf("Create(%s) error = %v", message.ID, err)
		}
	}

	// Nothing pruned without a requested state.
	if moved, err := repo.Prune(ctx, cutoff, nil); err != nil || moved != 0 {
		t.Fatalf("Prune(nil states) = (%d, %v), want (0, nil)", moved, err)
	}

	moved, err := repo.Prune(ctx, cutoff, []core.MessageState{core.MessageRead, core.MessageFailed})
	if err != nil {
		t.Fatalf("Prune() error = %v", err)
	}
	if moved != 2 {
		t.Fatalf("Prune() moved = %d, want 2", moved)
	}
	for _, gone := range []core.MessageID{"p-read-old", "p-failed-old"} {
		if _, err := repo.Get(ctx, gone); !errors.Is(err, core.ErrNotFound) {
			t.Fatalf("Get(%s) after prune error = %v, want ErrNotFound", gone, err)
		}
	}
	for _, kept := range []core.MessageID{"p-queued", "p-delivered", "p-read-young"} {
		if _, err := repo.Get(ctx, kept); err != nil {
			t.Fatalf("Get(%s) after prune error = %v, want kept", kept, err)
		}
	}
}

func testRun(t *testing.T, be store.Backend) {
	t.Helper()
	ctx := context.Background()
	repo := be.Runs()

	created := msgBase
	run := &core.Run{
		ID:         "run-1",
		ProjectID:  "prj-1",
		ActorID:    "act-1",
		Harness:    "opencode",
		Host:       "host-a",
		PID:        42,
		CanInject:  true,
		CreatedAt:  created,
		SeenAt:     created,
		LeaseUntil: created.Add(time.Minute),
	}
	stored, err := repo.Register(ctx, run)
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if stored.ID != "run-1" {
		t.Fatalf("Register().ID = %q, want run-1", stored.ID)
	}

	// The same session key upserts to the same stable id.
	duplicate := &core.Run{
		ID:         "run-dup",
		ProjectID:  "prj-1",
		ActorID:    "act-1",
		Harness:    "opencode",
		Host:       "host-a",
		PID:        42,
		CreatedAt:  created.Add(time.Hour),
		SeenAt:     created.Add(time.Hour),
		LeaseUntil: created.Add(time.Hour + time.Minute),
	}
	again, err := repo.Register(ctx, duplicate)
	if err != nil {
		t.Fatalf("Register() duplicate error = %v", err)
	}
	if again.ID != "run-1" {
		t.Fatalf("Register() duplicate id = %q, want stable run-1", again.ID)
	}
	all, err := repo.List(ctx, store.RunFilter{})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List() len = %d, want 1 after upsert", len(all))
	}

	// Heartbeat renews the lease.
	heartbeatAt := created.Add(5 * time.Minute)
	if err := repo.Heartbeat(ctx, "run-1", heartbeatAt, 2*time.Minute); err != nil {
		t.Fatalf("Heartbeat() error = %v", err)
	}
	beats, err := repo.List(ctx, store.RunFilter{ProjectID: "prj-1"})
	if err != nil {
		t.Fatalf("List() after heartbeat error = %v", err)
	}
	if len(beats) != 1 {
		t.Fatalf("List() after heartbeat len = %d, want 1", len(beats))
	}
	beaten := beats[0]
	if !beaten.SeenAt.Equal(heartbeatAt) || !beaten.LeaseUntil.Equal(heartbeatAt.Add(2*time.Minute)) {
		t.Fatalf("heartbeat run = %+v, want SeenAt/LeaseUntil moved", beaten)
	}

	// A second, stale run.
	stale := &core.Run{
		ID:         "run-stale",
		ProjectID:  "prj-1",
		ActorID:    "act-2",
		Host:       "host-b",
		PID:        7,
		CreatedAt:  created,
		SeenAt:     created,
		LeaseUntil: created.Add(time.Minute),
	}
	if _, err := repo.Register(ctx, stale); err != nil {
		t.Fatalf("Register(stale) error = %v", err)
	}

	now := created.Add(6 * time.Minute)
	live := true
	liveRuns, err := repo.List(ctx, store.RunFilter{ProjectID: "prj-1", Live: &live, Now: now})
	if err != nil {
		t.Fatalf("List(live) error = %v", err)
	}
	if len(liveRuns) != 1 || liveRuns[0].ID != "run-1" {
		t.Fatalf("List(live) = %v, want [run-1]", runIDs(liveRuns))
	}
	staleOnly := false
	staleRuns, err := repo.List(ctx, store.RunFilter{ProjectID: "prj-1", Live: &staleOnly, Now: now})
	if err != nil {
		t.Fatalf("List(stale) error = %v", err)
	}
	if len(staleRuns) != 1 || staleRuns[0].ID != "run-stale" {
		t.Fatalf("List(stale) = %v, want [run-stale]", runIDs(staleRuns))
	}

	byActor := core.ActorID("act-1")
	byActorRuns, err := repo.List(ctx, store.RunFilter{ActorID: &byActor})
	if err != nil {
		t.Fatalf("List(actor) error = %v", err)
	}
	if len(byActorRuns) != 1 || byActorRuns[0].ID != "run-1" {
		t.Fatalf("List(actor) = %v, want [run-1]", runIDs(byActorRuns))
	}

	if err := repo.Delete(ctx, "run-1"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if runs, err := repo.List(ctx, store.RunFilter{}); err != nil {
		t.Fatalf("List() error = %v", err)
	} else if len(runs) != 1 || runs[0].ID != "run-stale" {
		t.Fatalf("List() after delete = %v, want [run-stale]", runIDs(runs))
	}
	if err := repo.Delete(ctx, "run-1"); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Delete(missing) error = %v, want ErrNotFound", err)
	}
	if err := repo.Heartbeat(ctx, "missing", now, time.Minute); !errors.Is(err, core.ErrNotFound) {
		t.Fatalf("Heartbeat(missing) error = %v, want ErrNotFound", err)
	}
}

func messageIDs(messages []*core.Message) []core.MessageID {
	out := make([]core.MessageID, 0, len(messages))
	for _, message := range messages {
		out = append(out, message.ID)
	}
	return out
}

func runIDs(runs []*core.Run) []core.RunID {
	out := make([]core.RunID, 0, len(runs))
	for _, run := range runs {
		out = append(out, run.ID)
	}
	return out
}
