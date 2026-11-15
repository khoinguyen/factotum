//go:build !unix

package jsonfile

import "fmt"

// lockStore refuses where POSIX advisory locks are unavailable. Without them a
// single-document store cannot exclude a second writer, and two writers rewrite
// each other's collections away; failing beats silently losing writes.
func (b *Backend) lockStore(bool) (func(), error) {
	return nil, fmt.Errorf("jsonfile store %s needs POSIX advisory file locking, unavailable on this platform; use --store sqlite for a shared store", b.path)
}
