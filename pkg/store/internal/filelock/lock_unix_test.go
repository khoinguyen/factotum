//go:build unix

package filelock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestAcquireNamesTheLockFromACleanedPath pins the lock's identity: the same
// store spelled with or without a trailing separator must name the same sibling
// lock file. A raw concatenation put "store/.lock" in the store directory for
// one spelling and "store.lock" beside it for the other, which quietly gave two
// components two different locks.
func TestAcquireNamesTheLockFromACleanedPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store")

	release, err := Acquire(path+string(os.PathSeparator), true)
	if err != nil {
		t.Fatalf("Acquire(%q) error = %v", path+string(os.PathSeparator), err)
	}
	defer release()

	if _, err := os.Stat(path + ".lock"); err != nil {
		t.Fatalf("Stat(%s) error = %v; the lock must be the store's sibling", path+".lock", err)
	}
	if _, err := os.Stat(filepath.Join(path, ".lock")); err == nil {
		t.Fatalf("%s exists; a trailing separator must not move the lock inside the store", filepath.Join(path, ".lock"))
	}
}

// TestAcquireContendsAcrossSpellings proves the cleaned name is also the lock a
// second spelling waits on, not merely the file it creates.
func TestAcquireContendsAcrossSpellings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store")
	release, err := Acquire(path, true)
	if err != nil {
		t.Fatalf("Acquire(%q) error = %v", path, err)
	}
	released := false
	defer func() {
		if !released {
			release()
		}
	}()

	done := make(chan error, 1)
	go func() {
		other, err := Acquire(path+string(os.PathSeparator), true)
		if err != nil {
			done <- err
			return
		}
		other()
		done <- nil
	}()

	select {
	case err := <-done:
		t.Fatalf("a trailing-separator spelling did not wait for the lock (err = %v)", err)
	case <-time.After(100 * time.Millisecond):
	}

	released = true
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Acquire(%q) error = %v", path+string(os.PathSeparator), err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second spelling never acquired the lock after it was released")
	}
}
