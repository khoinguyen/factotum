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
	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	file, err := os.OpenFile(b.path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
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
