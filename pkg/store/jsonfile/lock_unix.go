//go:build unix

package jsonfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// lockStore takes the store's cross-process advisory lock and returns its
// release. The lock lives on a sibling file because persist replaces the
// document by rename: a lock on the replaced inode would not exclude the next
// writer. flock belongs to the open file description, so two handles in one
// process contend exactly like two processes.
func (b *Backend) lockStore(exclusive bool) (func(), error) {
	file, err := b.openLockFile(exclusive)
	if err != nil {
		return nil, err
	}
	if file == nil {
		// No lock was available for this read. Writes replace the document by
		// rename and this path never writes, so an unlocked read still sees a
		// whole document — at worst a moment stale. Never build a
		// read-modify-write on this path.
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
		return nil, fmt.Errorf("lock %s: %w", b.path, err)
	}
	return func() {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = file.Close()
	}, nil
}

// openLockFile opens the lock file for the lock mode. A writer needs it
// writable and creates it; a reader takes it read-only when it exists, so a
// store another uid wrote (a container hub) or a read-only checkout still
// serves reads. A nil file means this read runs unlocked (see lockStore).
func (b *Backend) openLockFile(exclusive bool) (*os.File, error) {
	lockPath := b.path + ".lock"
	if exclusive {
		if err := os.MkdirAll(filepath.Dir(b.path), 0o755); err != nil {
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
