// Package registry provides a small typed registry used by every plugin point:
// storage backends, rankers, renderers, and CLI commands.
package registry

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	ErrDuplicate = errors.New("duplicate registration")
	ErrEmptyName = errors.New("empty registration name")
	ErrNotFound  = errors.New("not registered")
)

type Registry[T any] struct {
	mu    sync.RWMutex
	items map[string]T
}

func New[T any]() *Registry[T] {
	return &Registry[T]{items: make(map[string]T)}
}

func (r *Registry[T]) Register(name string, item T) error {
	if name == "" {
		return ErrEmptyName
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.items == nil {
		r.items = make(map[string]T)
	}
	if _, ok := r.items[name]; ok {
		return fmt.Errorf("%w: %s", ErrDuplicate, name)
	}
	r.items[name] = item
	return nil
}

func (r *Registry[T]) Lookup(name string) (T, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.items[name]
	return item, ok
}

func (r *Registry[T]) MustLookup(name string) (T, error) {
	item, ok := r.Lookup(name)
	if !ok {
		var zero T
		return zero, fmt.Errorf("%w: %s", ErrNotFound, name)
	}
	return item, nil
}

func (r *Registry[T]) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.items))
	for name := range r.items {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
