//go:build unix

package filelock

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// Acquire takes the advisory lock beside path — the sibling file <path>.lock —
// and returns its release. The lock lives on its own file because the store at
// path may be replaced by rename: a lock on the replaced inode would not exclude
// the next writer. flock belongs to the open file description, so two handles in
// one process contend exactly like two processes.
//
// A shared (read) lock never fails for want of a lock file: where one cannot be
// opened, the caller reads unlocked. Writes replace a store atomically and this
// path never writes, so an unlocked read still sees whole data — at worst a
// moment stale. Never build a read-modify-write on a shared lock.
func Acquire(path string, exclusive bool) (func(), error) {
	file, err := openLockFile(path, exclusive)
	if err != nil {
		return nil, err
	}
	if file == nil {
		return func() {}, nil
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(file.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// openLockFile opens the lock file for the lock mode. A writer needs it
// writable and creates it; a reader takes it read-only when it exists, so a
// store another uid wrote (a container hub) or a read-only checkout still
// serves reads. A nil file means this read runs unlocked (see Acquire).
func openLockFile(path string, exclusive bool) (*os.File, error) {
	// Clean first: the lock's identity is its path, so one store spelled with
	// and without a trailing separator must name the same sibling file rather
	// than locking "store/.lock" and "store.lock" separately.
	lockPath := filepath.Clean(path) + ".lock"
	if exclusive {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("create state dir: %w", err)
		}
		file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			return nil, fmt.Errorf("open lock file %s for writing: %w", lockPath, err)
		}
		return file, nil
	}
	file, err := os.Open(lockPath)
	if err == nil {
		return file, nil
	}
	if errors.Is(err, os.ErrPermission) {
		// A lock file this uid cannot read (another uid's umask, e.g. 0600) is
		// no reason to refuse a read.
		return nil, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("open lock file %s: %w", lockPath, err)
	}
	// No lock file yet: creating it lets this read serialize with a writer that
	// starts later. Where that is denied, fall back to an unlocked read.
	file, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err == nil {
		return file, nil
	}
	if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return nil, fmt.Errorf("create lock file %s: %w", lockPath, err)
}
