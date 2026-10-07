// Package jsondir is a directory-based storage backend that keeps every entity
// in a human-readable markdown file with YAML frontmatter and appends events to
// sharded JSONL files. Mutations touch only the affected file and are written
// atomically (temp file + rename), so the directory is git-friendly and safe to
// edit by hand. List and search are served from an in-memory index built at open.
package jsondir

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/internal/clone"
)

// DefaultPath is the directory used when no path option is configured.
const DefaultPath = ".factotum/jsondir"

// eventShardSize is how many events share one JSONL file. Shards are numbered
// in append order, so reading them in filename order restores insertion order.
const eventShardSize = 1000

type taskRecord struct {
	task  core.Task
	extra map[string]*yaml.Node
}

type projectRecord struct {
	project core.Project
	extra   map[string]*yaml.Node
}

type actorRecord struct {
	actor core.Actor
	extra map[string]*yaml.Node
}

type artifactRecord struct {
	artifact core.Artifact
	extra    map[string]*yaml.Node
}

type Backend struct {
	mu        sync.Mutex
	root      string
	tasks     map[core.TaskID]*taskRecord
	projects  map[core.ProjectID]*projectRecord
	actors    map[core.ActorID]*actorRecord
	artifacts map[core.ArtifactID]*artifactRecord
	events    []core.Event
}

func Open(ctx context.Context, cfg store.Config) (store.Backend, error) {
	root := cfg.Option("path")
	if root == "" {
		root = DefaultPath
	}
	if err := migrateFromOption(ctx, cfg, root); err != nil {
		return nil, err
	}
	return open(ctx, root)
}

// open loads the directory store at root without running any migration. It is
// the shared entry point for Open and the migration writer, so a migration does
// not recurse back into itself.
func open(_ context.Context, root string) (store.Backend, error) {
	b := &Backend{
		root:      root,
		tasks:     make(map[core.TaskID]*taskRecord),
		projects:  make(map[core.ProjectID]*projectRecord),
		actors:    make(map[core.ActorID]*actorRecord),
		artifacts: make(map[core.ArtifactID]*artifactRecord),
	}
	if err := b.load(); err != nil {
		return nil, err
	}
	return b, nil
}

// migrateFromOption runs a one-time jsonfile migration when the store is
// configured with migrate_from=<path>. The migration is idempotent, so a source
// that was already consumed is a silent no-op.
func migrateFromOption(ctx context.Context, cfg store.Config, root string) error {
	source := cfg.Option("migrate_from")
	if source == "" {
		return nil
	}
	backup, err := MigrateFromJSONFile(ctx, source, root)
	if err != nil {
		return err
	}
	if backup != "" && cfg.Noticef != nil {
		cfg.Noticef("migrated jsonfile %s to jsondir %s; original backed up to %s", source, root, backup)
	}
	return nil
}

func (b *Backend) Close() error { return nil }

func (b *Backend) Projects() store.ProjectRepo   { return &projectRepo{backend: b} }
func (b *Backend) Tasks() store.TaskRepo         { return &taskRepo{backend: b} }
func (b *Backend) Actors() store.ActorRepo       { return &actorRepo{backend: b} }
func (b *Backend) Artifacts() store.ArtifactRepo { return &artifactRepo{backend: b} }
func (b *Backend) Events() store.EventRepo       { return &eventRepo{backend: b} }

// --- loading ---

func (b *Backend) load() error {
	if err := b.loadProjects(); err != nil {
		return err
	}
	if err := b.loadTasks(); err != nil {
		return err
	}
	if err := b.loadActors(); err != nil {
		return err
	}
	if err := b.loadArtifacts(); err != nil {
		return err
	}
	return b.loadEvents()
}

func (b *Backend) loadTasks() error {
	return b.readDocs("tasks", func(name string, data []byte) error {
		task, extra, err := decodeTask(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := b.tasks[task.ID]; ok {
			return fmt.Errorf("%s: duplicate task id %s", name, task.ID)
		}
		if err := checkID("task", string(task.ID)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b.tasks[task.ID] = &taskRecord{task: task, extra: extra}
		return nil
	})
}

func (b *Backend) loadProjects() error {
	return b.readDocs("projects", func(name string, data []byte) error {
		project, extra, err := decodeProject(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := b.projects[project.ID]; ok {
			return fmt.Errorf("%s: duplicate project id %s", name, project.ID)
		}
		if err := checkID("project", string(project.ID)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b.projects[project.ID] = &projectRecord{project: project, extra: extra}
		return nil
	})
}

func (b *Backend) loadActors() error {
	return b.readDocs("actors", func(name string, data []byte) error {
		actor, extra, err := decodeActor(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := b.actors[actor.ID]; ok {
			return fmt.Errorf("%s: duplicate actor id %s", name, actor.ID)
		}
		if err := checkID("actor", string(actor.ID)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b.actors[actor.ID] = &actorRecord{actor: actor, extra: extra}
		return nil
	})
}

func (b *Backend) loadArtifacts() error {
	return b.readDocs("artifacts", func(name string, data []byte) error {
		artifact, extra, err := decodeArtifact(data)
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := b.artifacts[artifact.ID]; ok {
			return fmt.Errorf("%s: duplicate artifact id %s", name, artifact.ID)
		}
		if err := checkID("artifact", string(artifact.ID)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b.artifacts[artifact.ID] = &artifactRecord{artifact: artifact, extra: extra}
		return nil
	})
}

// readDocs invokes visit for every .md file in dir, in name order. A missing
// directory is not an error: the store is simply empty.
func (b *Backend) readDocs(dir string, visit func(name string, data []byte) error) error {
	entries, err := os.ReadDir(filepath.Join(b.root, dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(b.root, dir, entry.Name()))
		if err != nil {
			return fmt.Errorf("read %s/%s: %w", dir, entry.Name(), err)
		}
		if err := visit(filepath.Join(dir, entry.Name()), data); err != nil {
			return err
		}
	}
	return nil
}

func (b *Backend) loadEvents() error {
	dir := filepath.Join(b.root, "events")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read events: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open events/%s: %w", entry.Name(), err)
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
		line := 0
		for scanner.Scan() {
			line++
			text := strings.TrimSpace(scanner.Text())
			if text == "" {
				continue
			}
			var event core.Event
			if err := json.Unmarshal([]byte(text), &event); err != nil {
				_ = file.Close()
				return fmt.Errorf("events/%s line %d: %w", entry.Name(), line, err)
			}
			b.events = append(b.events, event)
		}
		if err := scanner.Err(); err != nil {
			_ = file.Close()
			return fmt.Errorf("read events/%s: %w", entry.Name(), err)
		}
		_ = file.Close()
	}
	return nil
}

// --- paths and writes ---

func (b *Backend) docPath(dir, id string) string {
	return filepath.Join(b.root, dir, id+".md")
}

// checkID rejects ids that would escape the layout when used as a filename.
func checkID(kind, id string) error {
	if id == "" {
		return fmt.Errorf("%s id is empty", kind)
	}
	if strings.ContainsAny(id, `/\`) || id == "." || id == ".." {
		return fmt.Errorf("%s id %q contains a path separator", kind, id)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".jsondir-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// --- projects ---

type projectRepo struct{ backend *Backend }

func (r *projectRepo) Create(_ context.Context, project *core.Project) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.projects[project.ID]; ok {
		return fmt.Errorf("%w: project %s", core.ErrAlreadyExists, project.ID)
	}
	if err := checkID("project", string(project.ID)); err != nil {
		return err
	}
	data, err := encodeProject(*project, nil)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("projects", string(project.ID)), data); err != nil {
		return err
	}
	b.projects[project.ID] = &projectRecord{project: clone.Project(*project)}
	return nil
}

func (r *projectRepo) Get(_ context.Context, id core.ProjectID) (*core.Project, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	project := clone.Project(record.project)
	return &project, nil
}

func (r *projectRepo) List(_ context.Context) ([]*core.Project, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*core.Project, 0, len(b.projects))
	for _, record := range b.projects {
		project := clone.Project(record.project)
		out = append(out, &project)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *projectRepo) Update(_ context.Context, project *core.Project) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.projects[project.ID]
	if !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, project.ID)
	}
	data, err := encodeProject(*project, record.extra)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("projects", string(project.ID)), data); err != nil {
		return err
	}
	b.projects[project.ID] = &projectRecord{project: clone.Project(*project), extra: record.extra}
	return nil
}

func (r *projectRepo) Delete(_ context.Context, id core.ProjectID) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.projects[id]; !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	if err := removeFile(b.docPath("projects", string(id))); err != nil {
		return err
	}
	delete(b.projects, id)
	return nil
}

// --- tasks ---

type taskRepo struct{ backend *Backend }

func (r *taskRepo) Create(_ context.Context, task *core.Task) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.tasks[task.ID]; ok {
		return fmt.Errorf("%w: task %s", core.ErrAlreadyExists, task.ID)
	}
	if err := checkID("task", string(task.ID)); err != nil {
		return err
	}
	data, err := encodeTask(*task, nil)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("tasks", string(task.ID)), data); err != nil {
		return err
	}
	b.tasks[task.ID] = &taskRecord{task: clone.Task(*task)}
	return nil
}

func (r *taskRepo) Get(_ context.Context, id core.TaskID) (*core.Task, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.tasks[id]
	if !ok {
		return nil, fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	task := clone.Task(record.task)
	return &task, nil
}

func (r *taskRepo) List(_ context.Context, filter store.TaskFilter) ([]*core.Task, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*core.Task, 0, len(b.tasks))
	for _, record := range b.tasks {
		if !matchesTask(record.task, filter) {
			continue
		}
		task := clone.Task(record.task)
		out = append(out, &task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *taskRepo) Search(_ context.Context, filter store.TaskFilter, query string) ([]store.TaskSearchHit, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.TaskSearchHit, 0)
	for _, record := range b.tasks {
		if !matchesTask(record.task, filter) {
			continue
		}
		score, matched := store.LexicalTaskScore(&record.task, terms)
		if !matched {
			continue
		}
		task := clone.Task(record.task)
		hits = append(hits, store.TaskSearchHit{Task: &task, Score: score})
	}
	store.SortTaskSearchHits(hits)
	return hits, nil
}

func (r *taskRepo) Update(_ context.Context, task *core.Task) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.tasks[task.ID]
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	data, err := encodeTask(*task, record.extra)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("tasks", string(task.ID)), data); err != nil {
		return err
	}
	b.tasks[task.ID] = &taskRecord{task: clone.Task(*task), extra: record.extra}
	return nil
}

func (r *taskRepo) UpdateExpected(_ context.Context, task *core.Task, expected time.Time) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.tasks[task.ID]
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	if !record.task.UpdatedAt.Equal(expected) {
		return fmt.Errorf("%w: task %s was modified", core.ErrConflict, task.ID)
	}
	data, err := encodeTask(*task, record.extra)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("tasks", string(task.ID)), data); err != nil {
		return err
	}
	b.tasks[task.ID] = &taskRecord{task: clone.Task(*task), extra: record.extra}
	return nil
}

func (r *taskRepo) Delete(_ context.Context, id core.TaskID) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.tasks[id]; !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	if err := removeFile(b.docPath("tasks", string(id))); err != nil {
		return err
	}
	delete(b.tasks, id)
	return nil
}

func matchesTask(task core.Task, filter store.TaskFilter) bool {
	if filter.ProjectID != "" && task.ProjectID != filter.ProjectID {
		return false
	}
	if filter.Repo != nil && task.Repo != *filter.Repo {
		return false
	}
	if len(filter.Statuses) > 0 {
		found := false
		for _, status := range filter.Statuses {
			if task.Status == status {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if filter.Kind != nil && task.Kind != *filter.Kind {
		return false
	}
	if filter.DependsOn != nil && !taskDependsOn(task, *filter.DependsOn) {
		return false
	}
	if !store.MatchGroomed(task, filter.Groomed) {
		return false
	}
	return store.MatchLabels(task, filter.Labels)
}

func taskDependsOn(task core.Task, id core.TaskID) bool {
	for _, dep := range task.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

// --- actors ---

type actorRepo struct{ backend *Backend }

func (r *actorRepo) Create(_ context.Context, actor *core.Actor) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.actors[actor.ID]; ok {
		return fmt.Errorf("%w: actor %s", core.ErrAlreadyExists, actor.ID)
	}
	if err := checkID("actor", string(actor.ID)); err != nil {
		return err
	}
	data, err := encodeActor(*actor, nil)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("actors", string(actor.ID)), data); err != nil {
		return err
	}
	b.actors[actor.ID] = &actorRecord{actor: clone.Actor(*actor)}
	return nil
}

func (r *actorRepo) Get(_ context.Context, id core.ActorID) (*core.Actor, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.actors[id]
	if !ok {
		return nil, fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	actor := clone.Actor(record.actor)
	return &actor, nil
}

func (r *actorRepo) FindByName(_ context.Context, name string) (*core.Actor, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, record := range b.actors {
		if record.actor.Name == name {
			actor := clone.Actor(record.actor)
			return &actor, nil
		}
	}
	return nil, fmt.Errorf("%w: actor named %q", core.ErrNotFound, name)
}

func (r *actorRepo) List(_ context.Context) ([]*core.Actor, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*core.Actor, 0, len(b.actors))
	for _, record := range b.actors {
		actor := clone.Actor(record.actor)
		out = append(out, &actor)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *actorRepo) Update(_ context.Context, actor *core.Actor) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.actors[actor.ID]
	if !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, actor.ID)
	}
	data, err := encodeActor(*actor, record.extra)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("actors", string(actor.ID)), data); err != nil {
		return err
	}
	b.actors[actor.ID] = &actorRecord{actor: clone.Actor(*actor), extra: record.extra}
	return nil
}

func (r *actorRepo) Delete(_ context.Context, id core.ActorID) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.actors[id]; !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	if err := removeFile(b.docPath("actors", string(id))); err != nil {
		return err
	}
	delete(b.actors, id)
	return nil
}

// --- artifacts ---

type artifactRepo struct{ backend *Backend }

func (r *artifactRepo) Create(_ context.Context, artifact *core.Artifact) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.artifacts[artifact.ID]; ok {
		return fmt.Errorf("%w: artifact %s", core.ErrAlreadyExists, artifact.ID)
	}
	if err := checkID("artifact", string(artifact.ID)); err != nil {
		return err
	}
	data, err := encodeArtifact(*artifact, nil)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("artifacts", string(artifact.ID)), data); err != nil {
		return err
	}
	b.artifacts[artifact.ID] = &artifactRecord{artifact: clone.Artifact(*artifact)}
	return nil
}

func (r *artifactRepo) Get(_ context.Context, id core.ArtifactID) (*core.Artifact, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.artifacts[id]
	if !ok {
		return nil, fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	artifact := clone.Artifact(record.artifact)
	return &artifact, nil
}

func (r *artifactRepo) List(_ context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*core.Artifact, 0, len(b.artifacts))
	for _, record := range b.artifacts {
		if !store.MatchesArtifactFilter(&record.artifact, filter) {
			continue
		}
		artifact := clone.Artifact(record.artifact)
		out = append(out, &artifact)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *artifactRepo) Search(_ context.Context, filter store.ArtifactFilter, query string) ([]store.SearchHit, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.SearchHit, 0)
	for _, record := range b.artifacts {
		if !store.MatchesArtifactFilter(&record.artifact, filter) {
			continue
		}
		score, matched := store.LexicalScore(&record.artifact, terms)
		if !matched {
			continue
		}
		artifact := clone.Artifact(record.artifact)
		hits = append(hits, store.SearchHit{Artifact: &artifact, Score: score})
	}
	store.SortSearchHits(hits)
	return hits, nil
}

func (r *artifactRepo) Update(_ context.Context, artifact *core.Artifact) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	record, ok := b.artifacts[artifact.ID]
	if !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	data, err := encodeArtifact(*artifact, record.extra)
	if err != nil {
		return err
	}
	if err := writeFileAtomic(b.docPath("artifacts", string(artifact.ID)), data); err != nil {
		return err
	}
	b.artifacts[artifact.ID] = &artifactRecord{artifact: clone.Artifact(*artifact), extra: record.extra}
	return nil
}

func (r *artifactRepo) Delete(_ context.Context, id core.ArtifactID) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.artifacts[id]; !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	if err := removeFile(b.docPath("artifacts", string(id))); err != nil {
		return err
	}
	delete(b.artifacts, id)
	return nil
}

// --- events ---

type eventRepo struct{ backend *Backend }

func (r *eventRepo) Append(_ context.Context, event *core.Event) error {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.appendEvent(event); err != nil {
		return err
	}
	b.events = append(b.events, clone.Event(*event))
	return nil
}

func (b *Backend) appendEvent(event *core.Event) error {
	shard := len(b.events) / eventShardSize
	dir := filepath.Join(b.root, "events")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create events dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%06d.jsonl", shard))
	data, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	data = append(data, '\n')
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open event shard: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("append event: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close event shard: %w", err)
	}
	return nil
}

func (r *eventRepo) List(_ context.Context, filter store.EventFilter) ([]*core.Event, error) {
	b := r.backend
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*core.Event, 0)
	for i := len(b.events) - 1; i >= 0; i-- {
		event := b.events[i]
		if filter.ProjectID != "" && event.ProjectID != filter.ProjectID {
			continue
		}
		if filter.TaskID != nil && (event.TaskID == nil || *event.TaskID != *filter.TaskID) {
			continue
		}
		if len(filter.Kinds) > 0 && !containsKind(filter.Kinds, event.Kind) {
			continue
		}
		if filter.Since != nil && event.CreatedAt.Before(*filter.Since) {
			continue
		}
		cloned := clone.Event(event)
		out = append(out, &cloned)
		if filter.Limit > 0 && len(out) >= filter.Limit {
			break
		}
	}
	return out, nil
}

func containsKind(kinds []core.EventKind, kind core.EventKind) bool {
	for _, k := range kinds {
		if k == kind {
			return true
		}
	}
	return false
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
