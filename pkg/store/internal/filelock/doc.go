// Package filelock is the host-local advisory lock the file backends share. It
// serializes a read-modify-write across processes on one store, which an
// in-process mutex cannot do.
package filelock
