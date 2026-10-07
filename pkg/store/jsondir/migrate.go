package jsondir

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
)

// nowFunc is overridable in tests so backup names are deterministic.
var nowFunc = time.Now

// MigrateFromJSONFile converts the single-document jsonfile at source into the
// jsondir layout rooted at root, so a user can switch backends without losing
// state. It reads the legacy document through the jsonfile backend and writes
// each entity as a jsondir document, so the migration tracks the store ports
// rather than the JSON shape.
//
// It is idempotent: when the source is already gone and root holds the migrated
// data it is a no-op. It refuses to write into a target directory that already
// holds data, with a clear error, rather than clobber it. On success the
// original document is moved next to itself as <source>.bak-<UTC timestamp>
// (the backup path is returned) and the source is removed, which is what makes
// a re-run a no-op.
func MigrateFromJSONFile(ctx context.Context, source, root string) (string, error) {
	if source == "" || root == "" {
		return "", fmt.Errorf("%w: migration needs a source document and a target directory", core.ErrInvalid)
	}
	sourceExists, err := fileExists(source)
	if err != nil {
		return "", err
	}
	targetExists, err := dirHasEntries(root)
	if err != nil {
		return "", err
	}
	if !sourceExists {
		if targetExists {
			return "", nil
		}
		return "", fmt.Errorf("%w: no jsonfile document at %s", core.ErrNotFound, source)
	}
	if targetExists {
		return "", fmt.Errorf("%w: target directory %s already exists", core.ErrAlreadyExists, root)
	}

	legacy, err := jsonfile.Open(ctx, store.Config{Backend: "jsonfile", Options: map[string]string{"path": source}})
	if err != nil {
		return "", fmt.Errorf("open jsonfile %s: %w", source, err)
	}
	defer func() { _ = legacy.Close() }()

	target, err := open(ctx, root)
	if err != nil {
		return "", err
	}
	defer func() { _ = target.Close() }()

	if err := copyState(ctx, legacy, target); err != nil {
		return "", err
	}

	backup := fmt.Sprintf("%s.bak-%s", source, nowFunc().UTC().Format("20060102T150405Z"))
	if err := os.Rename(source, backup); err != nil {
		return "", fmt.Errorf("back up %s: %w", source, err)
	}
	return backup, nil
}

// copyState recreates every entity of from in to through the store ports.
func copyState(ctx context.Context, from, to store.Backend) error {
	projects, err := from.Projects().List(ctx)
	if err != nil {
		return fmt.Errorf("list projects: %w", err)
	}
	for _, project := range projects {
		if err := to.Projects().Create(ctx, project); err != nil {
			return fmt.Errorf("copy project %s: %w", project.ID, err)
		}
	}

	actors, err := from.Actors().List(ctx)
	if err != nil {
		return fmt.Errorf("list actors: %w", err)
	}
	for _, actor := range actors {
		if err := to.Actors().Create(ctx, actor); err != nil {
			return fmt.Errorf("copy actor %s: %w", actor.ID, err)
		}
	}

	tasks, err := from.Tasks().List(ctx, store.TaskFilter{})
	if err != nil {
		return fmt.Errorf("list tasks: %w", err)
	}
	for _, task := range tasks {
		if err := to.Tasks().Create(ctx, task); err != nil {
			return fmt.Errorf("copy task %s: %w", task.ID, err)
		}
	}

	artifacts, err := from.Artifacts().List(ctx, store.ArtifactFilter{})
	if err != nil {
		return fmt.Errorf("list artifacts: %w", err)
	}
	for _, artifact := range artifacts {
		if err := to.Artifacts().Create(ctx, artifact); err != nil {
			return fmt.Errorf("copy artifact %s: %w", artifact.ID, err)
		}
	}

	// EventRepo.List returns newest-first; re-append oldest-first so the
	// sharded log keeps insertion order.
	events, err := from.Events().List(ctx, store.EventFilter{})
	if err != nil {
		return fmt.Errorf("list events: %w", err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if err := to.Events().Append(ctx, events[i]); err != nil {
			return fmt.Errorf("copy event %s: %w", events[i].ID, err)
		}
	}
	return nil
}

func fileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat %s: %w", path, err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("%w: %s is a directory, want a jsonfile document", core.ErrInvalid, path)
	}
	return true, nil
}

func dirHasEntries(path string) (bool, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return len(entries) > 0, nil
}
