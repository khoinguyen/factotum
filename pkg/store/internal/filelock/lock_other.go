//go:build !unix

package filelock

import "fmt"

// Acquire refuses where POSIX advisory locks are unavailable. Without them a
// file-backed store cannot exclude a second writer, and two writers can rewrite
// each other's records away; failing beats silently losing writes.
func Acquire(path string, _ bool) (func(), error) {
	return nil, fmt.Errorf("store %s needs POSIX advisory file locking, unavailable on this platform; use --store sqlite for a shared store", path)
}
