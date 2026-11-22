package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// TestAckRepairsLegacyBlankLinkTitle proves a delivered message stored by an
// older ft, whose link title is only whitespace, can still be acked. This
// adapter revalidates the row on Update, so without the decode-side repair the
// ack fails, the lease sweep requeues the row, and it eventually lands failed.
// The repair drops the blank title, and the ack persists it.
func TestAckRepairsLegacyBlankLinkTitle(t *testing.T) {
	ctx := context.Background()
	backend, _ := openFileBackend(t)
	repo := backend.Messages()
	now := time.Now().UTC()

	message := &core.Message{
		ID:        "msg-legacy",
		ProjectID: "prj-1",
		To:        core.ActorAddress("bob"),
		State:     core.MessageQueued,
		Body:      "see the doc",
		Links:     []core.Link{{Kind: core.LinkDoc, URL: "https://example.invalid/spec", Title: "Design doc"}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := repo.Create(ctx, message); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	actor := core.ActorID("bob")
	claimed, err := repo.Claim(ctx, store.ClaimRequest{
		ProjectID: "prj-1",
		RunID:     "run-1",
		Recipient: store.ClaimRecipient{ActorID: &actor},
	})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed == nil {
		t.Fatal("Claim() = nil, want the queued message")
	}

	// Rewrite the stored title to spaces, the shape an older ft wrote: blank by
	// the current rule, but carrying no refused rune.
	if _, err := backend.(*Backend).db.ExecContext(ctx,
		`UPDATE messages SET data = replace(data, '"Design doc"', '"   "') WHERE id = ?`, string(message.ID)); err != nil {
		t.Fatalf("seed legacy title: %v", err)
	}

	if err := repo.Ack(ctx, store.AckRequest{ID: message.ID, RunID: "run-1", State: core.MessageRead}); err != nil {
		t.Fatalf("Ack() error = %v; a blank legacy title must not wedge the ack", err)
	}
	got, err := repo.Get(ctx, message.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.State != core.MessageRead {
		t.Fatalf("state = %q, want read", got.State)
	}
	if got.Links[0].Title != "" {
		t.Fatalf("title = %q, want the blank title dropped", got.Links[0].Title)
	}
}
