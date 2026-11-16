package jsonfile

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/internal/filelock"
	"github.com/khoinguyen/factotum/pkg/store/internal/promotetest"
)

// openPromotionStore is the opener the promotion children and their parent
// share: the child re-executes this test binary and must open a real store.
func openPromotionStore(ctx context.Context, path string) (store.Backend, error) {
	return Open(ctx, store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}})
}

// readPromotionStore holds the store as a reader at path — the shared lock a
// read takes, on the document its handle is opened with. The harness holds it
// across the children's arrival, which queues every promotion (a writer) at the
// store's begin(), and lets a promotion that took the shared lock run.
func readPromotionStore(path string) (func(), error) { return filelock.Acquire(path, false) }

// TestMain runs a promotion child when the cross-process tests re-execute this
// binary, rather than running any test.
func TestMain(m *testing.M) {
	if ran, code := promotetest.Child(openPromotionStore); ran {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// TestPromotionAcrossProcessesKeepsEveryTask promotes a different capture from
// each of 20 processes at once. Before the promotion path took the store's file
// lock, every child persisted the document it loaded at open, so only the last
// writer's task survived (QA measured 7, 8 and 10 of 20).
func TestPromotionAcrossProcessesKeepsEveryTask(t *testing.T) {
	promotetest.DistinctCaptures(t, openPromotionStore, filepath.Join(t.TempDir(), "factotum.json"))
}

// TestPromotionAcrossProcessesAgreesOnOneTask promotes one capture from 12
// processes at once and requires the guard to agree on a single task, so no
// caller is handed an id the store does not hold.
func TestPromotionAcrossProcessesAgreesOnOneTask(t *testing.T) {
	promotetest.SameCapture(t, openPromotionStore, readPromotionStore, filepath.Join(t.TempDir(), "factotum.json"))
}

// TestPromotionAcrossProcessesKeepsAnotherWritersWrite writes a message from
// another process while the promotion handles have already loaded their
// snapshots. The promotion must apply to the document on disk, not persist the
// snapshot it loaded at open (QA saw the message erased, db 1 -> 0).
func TestPromotionAcrossProcessesKeepsAnotherWritersWrite(t *testing.T) {
	promotetest.KeepsAnotherWritersWrite(t, openPromotionStore, filepath.Join(t.TempDir(), "factotum.json"))
}
