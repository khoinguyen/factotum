package jsondir

import (
	"context"
	"os"
	"testing"

	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/internal/promotetest"
)

// openPromotionStore is the opener the promotion children and their parent
// share: the child re-executes this test binary and must open a real store.
func openPromotionStore(ctx context.Context, path string) (store.Backend, error) {
	return Open(ctx, store.Config{Backend: "jsondir", Options: map[string]string{"path": path}})
}

// TestMain runs a promotion child when the cross-process tests re-execute this
// binary, rather than running any test.
func TestMain(m *testing.M) {
	if ran, code := promotetest.Child(openPromotionStore); ran {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

// TestPromotionAcrossProcessesKeepsEveryTask promotes a different capture from
// each of 20 processes at once. Only AgreesOnOneTask is jsondir's signal for
// this fix: a per-file store already keeps distinct promotions, so this one
// guards against regression rather than proving the guard.
func TestPromotionAcrossProcessesKeepsEveryTask(t *testing.T) {
	promotetest.DistinctCaptures(t, openPromotionStore, t.TempDir())
}

// TestPromotionAcrossProcessesAgreesOnOneTask promotes one capture from 12
// processes at once. Before the promotion path took a cross-process lock, each
// child saw no promotion in its own index and wrote its own task, leaving
// duplicate tasks and origin edges (the residual #350 left on jsondir).
func TestPromotionAcrossProcessesAgreesOnOneTask(t *testing.T) {
	promotetest.SameCapture(t, openPromotionStore, t.TempDir())
}

// TestPromotionAcrossProcessesKeepsAnotherWritersWrite writes a message from
// another process while the promotion handles have loaded their snapshots; the
// promotion writes only its own task file, so the message must survive. Like
// KeepsEveryTask this guards jsondir against regression rather than proving the
// cross-process guard.
func TestPromotionAcrossProcessesKeepsAnotherWritersWrite(t *testing.T) {
	promotetest.KeepsAnotherWritersWrite(t, openPromotionStore, t.TempDir())
}
