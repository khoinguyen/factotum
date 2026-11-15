//go:build unix

package jsonfile

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

// TestReadOnlyStoreIsReadable pins that a read needs no write access: a store
// another uid wrote (a container hub leaving root-owned files) or a read-only
// checkout must still serve reads, whether or not a lock file is already there.
func TestReadOnlyStoreIsReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes through directory permissions")
	}

	cases := []struct {
		name     string
		keepLock bool
	}{
		{name: "no lock file", keepLock: false},
		{name: "read-only lock file", keepLock: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
			path := filepath.Join(dir, "factotum.json")
			cfg := store.Config{Backend: "jsonfile", Options: map[string]string{"path": path}}

			writer := openAt(t, cfg)
			if err := writer.Projects().Create(ctx, &core.Project{ID: "prj-1", Name: "Acme"}); err != nil {
				t.Fatalf("Projects().Create() error = %v", err)
			}
			if !tc.keepLock {
				if err := os.Remove(path + ".lock"); err != nil {
					t.Fatalf("remove lock file: %v", err)
				}
			} else if err := os.Chmod(path+".lock", 0o444); err != nil {
				t.Fatalf("chmod lock file: %v", err)
			}
			if err := os.Chmod(path, 0o444); err != nil {
				t.Fatalf("chmod store: %v", err)
			}
			if err := os.Chmod(dir, 0o555); err != nil {
				t.Fatalf("chmod dir: %v", err)
			}

			reader := openAt(t, cfg)
			project, err := reader.Projects().Get(ctx, "prj-1")
			if err != nil {
				t.Fatalf("read-only Get() error = %v", err)
			}
			if project.Name != "Acme" {
				t.Fatalf("read-only Get() Name = %q, want Acme", project.Name)
			}

			err = reader.Projects().Create(ctx, &core.Project{ID: "prj-2", Name: "Beta"})
			if err == nil {
				t.Fatal("a write to a read-only store succeeded")
			}
			if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "lock file") {
				t.Fatalf("write error = %v, want it to name the unwritable lock file", err)
			}
		})
	}
}
