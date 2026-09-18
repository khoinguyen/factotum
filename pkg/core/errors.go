// Package core contains the pure domain model: entities, statuses, resolution
// policy, and validation. It must not import infrastructure packages.
package core

import "errors"

var (
	ErrInvalid       = errors.New("invalid")
	ErrNotFound      = errors.New("not found")
	ErrAlreadyExists = errors.New("already exists")
	ErrConflict      = errors.New("conflict")
	ErrCycle         = errors.New("dependency cycle")
)
