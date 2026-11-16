package cli

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// TestInvalidFlagTouchesNoStore pins that a flag value a command rejects is
// resolved before the root opens the caller's store. An invocation that can
// never run must not create, migrate, or lock a database on its way to failing:
// the sqlite backend creates and migrates its file at open, and jsonfile leaves
// its lock beside the document on the first write.
func TestInvalidFlagTouchesNoStore(t *testing.T) {
	backends := []struct {
		name string
		args []string
	}{
		{"jsonfile", nil},
		{"sqlite", []string{"--store", "sqlite"}},
	}
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"run sandbox", []string{"run", "--sandbox", "nope"}, `unknown isolation backend "nope"`},
		{"run harness", []string{"run", "--harness", "nope"}, `unknown harness "nope"`},
		{"groom sandbox", []string{"groom", "--sandbox", "nope"}, `unknown isolation backend "nope"`},
		{"groom harness", []string{"groom", "--harness", "nope"}, `unknown harness "nope"`},
		{"qa verdict", []string{"qa", "record", "cap-nope", "--verdict", "maybe", "-b", "x"}, `unknown verdict "maybe"`},
		{"groom review verdict", []string{"groom", "review", "groom-nope", "--verdict", "maybe", "-b", "x"}, `unknown architecture-review verdict "maybe"`},
		{"graph format", []string{"graph", "render", "-p", "nope", "--format", "nope"}, `unknown format "nope"`},
		{"task rank", []string{"task", "next", "-p", "nope", "--rank", "nope"}, `unknown ranker "nope"`},
	}
	for _, backend := range backends {
		for _, tc := range cases {
			t.Run(backend.name+"/"+tc.name, func(t *testing.T) {
				r := newRunner(t)
				args := append(append([]string{}, backend.args...), tc.args...)
				err := r.runErr(args...)
				if err == nil || !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error = %v, want the flag's usage error naming %q", err, tc.want)
				}
				assertNoPath(t, r.path)
				assertNoPath(t, r.path+".lock")
				assertNoPath(t, r.path+"-wal")
				assertNoPath(t, r.path+"-shm")
			})
		}
	}
}

func assertNoPath(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("%s exists after a rejected flag (stat error = %v); the store was opened", path, err)
	}
}

// TestInvalidFlagDoesNotOpenVectorIndex pins that the flag check also prevents
// the SQLite vector side index Attach opens beside the store. With an embedding
// model configured, a store-backed command normally creates it (that creation is
// pinned by memory_embed_integration_test).
func TestInvalidFlagDoesNotOpenVectorIndex(t *testing.T) {
	r := newRunner(t)
	r.embedModel = "test-model"
	r.embedder = &keywordEmbedder{}

	err := r.runErr("groom", "--sandbox", "nope")
	if err == nil || !strings.Contains(err.Error(), `unknown isolation backend "nope"`) {
		t.Fatalf("error = %v, want the unknown-backend usage error", err)
	}
	assertNoPath(t, r.path)
	assertNoPath(t, r.path+".vectors.db")
}
