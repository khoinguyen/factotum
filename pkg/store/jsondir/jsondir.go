// Package jsondir is a directory-based storage backend that keeps every entity
// in a human-readable markdown file with YAML frontmatter and appends events to
// sharded JSONL files. Mutations touch only the affected file and are written
// atomically (temp file + rename), so the directory is git-friendly and safe to
// edit by hand. List and search are served from an in-memory index, which every
// operation refreshes from disk when the directory changed.
//
// Concurrent ft processes share one directory (an `ft serve` hub beside the CLI,
// or a run beside the agent it launches). Every operation takes the store's
// host-local advisory lock — shared for reads, exclusive for writes — and
// re-reads the directory when it changed; every write marks the directory
// changed, so a long-running handle serves another process's write instead of
// its Open-time snapshot. Promotion still serializes its lookup and insert on
// the same lock, so concurrent promotes of one capture agree on a single task.
// A store on a filesystem shared across hosts is not supported: use sqlite for
// that.
package jsondir

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
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
	"github.com/khoinguyen/factotum/pkg/store/internal/filelock"
)

// DefaultPath is the directory used when no path option is configured.
const DefaultPath = ".factotum/jsondir"

// eventShardSize is how many events share one JSONL file. Shards are numbered
// in append order, so reading them in filename order restores insertion order.
const eventShardSize = 1000

type taskRecord struct {
	task  core.Ticket
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

type messageRecord struct {
	message core.Message
}

type runRecord struct {
	run core.Run
}

type pipelineRecord struct {
	pipeline core.Pipeline
}

type Backend struct {
	mu        sync.Mutex
	root      string
	stamp     storeStamp
	wrote     bool
	tasks     map[core.TicketID]*taskRecord
	projects  map[core.ProjectID]*projectRecord
	actors    map[core.ActorID]*actorRecord
	artifacts map[core.ArtifactID]*artifactRecord
	events    []core.Event
	messages  map[core.MessageID]*messageRecord
	runs      map[core.RunID]*runRecord
	pipelines map[core.PipelineID]*pipelineRecord
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
	b := newHandle(root)
	release, err := b.lock(false)
	if err != nil {
		return nil, err
	}
	defer release()
	if err := b.reload(); err != nil {
		return nil, err
	}
	return b, nil
}

// newHandle builds an empty handle on root. A reload fills it from disk.
func newHandle(root string) *Backend {
	return &Backend{
		root:      root,
		tasks:     make(map[core.TicketID]*taskRecord),
		projects:  make(map[core.ProjectID]*projectRecord),
		actors:    make(map[core.ActorID]*actorRecord),
		artifacts: make(map[core.ArtifactID]*artifactRecord),
		messages:  make(map[core.MessageID]*messageRecord),
		runs:      make(map[core.RunID]*runRecord),
		pipelines: make(map[core.PipelineID]*pipelineRecord),
	}
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
func (b *Backend) Tickets() store.TicketRepo     { return &taskRepo{backend: b} }
func (b *Backend) Actors() store.ActorRepo       { return &actorRepo{backend: b} }
func (b *Backend) Artifacts() store.ArtifactRepo { return &artifactRepo{backend: b} }
func (b *Backend) Events() store.EventRepo       { return &eventRepo{backend: b} }
func (b *Backend) Messages() store.MessageRepo   { return &messageRepo{backend: b} }
func (b *Backend) Runs() store.RunRepo           { return &runRepo{backend: b} }
func (b *Backend) Pipelines() store.PipelineRepo { return &pipelineRepo{backend: b} }

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
	if err := b.loadMessages(); err != nil {
		return err
	}
	if err := b.loadRuns(); err != nil {
		return err
	}
	if err := b.loadPipelines(); err != nil {
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

func (b *Backend) loadMessages() error {
	return b.readJSON("messages", func(name string, data []byte) error {
		var message core.Message
		if err := json.Unmarshal(data, &message); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := b.messages[message.ID]; ok {
			return fmt.Errorf("%s: duplicate message id %s", name, message.ID)
		}
		if err := checkID("message", string(message.ID)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b.messages[message.ID] = &messageRecord{message: message}
		return nil
	})
}

func (b *Backend) loadRuns() error {
	return b.readJSON("runs", func(name string, data []byte) error {
		var run core.Run
		if err := json.Unmarshal(data, &run); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := b.runs[run.ID]; ok {
			return fmt.Errorf("%s: duplicate run id %s", name, run.ID)
		}
		if err := checkID("run", string(run.ID)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b.runs[run.ID] = &runRecord{run: run}
		return nil
	})
}

func (b *Backend) loadPipelines() error {
	return b.readJSON("pipelines", func(name string, data []byte) error {
		var pipeline core.Pipeline
		if err := json.Unmarshal(data, &pipeline); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		if _, ok := b.pipelines[pipeline.ID]; ok {
			return fmt.Errorf("%s: duplicate pipeline id %s", name, pipeline.ID)
		}
		if err := checkID("pipeline", string(pipeline.ID)); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		b.pipelines[pipeline.ID] = &pipelineRecord{pipeline: pipeline}
		return nil
	})
}

// readJSON invokes visit for every .json file in dir, in name order. A missing
// directory is not an error: the store is simply empty.
func (b *Backend) readJSON(dir string, visit func(name string, data []byte) error) error {
	entries, err := os.ReadDir(filepath.Join(b.root, dir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
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

// --- sharing across processes ---

// loadDirs is every directory a load reads. The change check stats each, so a
// directory added to load() and not to this list would stop being noticed.
var loadDirs = [...]string{"projects", "tasks", "actors", "artifacts", "messages", "runs", "pipelines", "events"}

// storeStamp is the on-disk state the in-memory index was built from. The
// generation is what a writing ft process rewrites under the exclusive lock, so
// a second process is noticed on any filesystem whatever its timestamp
// resolution. The directory times notice a record added, removed, or replaced by
// something else (a hand edit, a git checkout). An in-place edit of an existing
// file changes neither, so a running handle picks it up on its next noticed
// change or on restart.
type storeStamp struct {
	generation string
	dirs       [len(loadDirs)]time.Time
}

// same reports whether other describes the same directory state.
func (s storeStamp) same(other storeStamp) bool {
	if s.generation != other.generation {
		return false
	}
	for i := range s.dirs {
		if !s.dirs[i].Equal(other.dirs[i]) {
			return false
		}
	}
	return true
}

// generationPath is the change stamp beside the store, the way the lock is,
// so the store directory keeps only its records.
func (b *Backend) generationPath() string {
	return filepath.Clean(b.root) + ".generation"
}

// stampDisk reads the change stamp. A missing generation or directory is not an
// error: either the store is empty or no ft process has written it. A generation
// this process may not read (another uid's store, e.g. a container hub) is no
// reason to refuse a read either, the same way the shared lock tolerates a lock
// file it cannot open; the directory times still notice most changes.
func (b *Backend) stampDisk() (storeStamp, error) {
	var stamp storeStamp
	data, err := os.ReadFile(b.generationPath())
	switch {
	case err == nil:
		stamp.generation = strings.TrimSpace(string(data))
	case !errors.Is(err, os.ErrNotExist) && !errors.Is(err, os.ErrPermission):
		return stamp, fmt.Errorf("read %s: %w", b.generationPath(), err)
	}
	for i, dir := range loadDirs {
		info, err := os.Stat(filepath.Join(b.root, dir))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return stamp, fmt.Errorf("stat %s: %w", dir, err)
		}
		stamp.dirs[i] = info.ModTime()
	}
	return stamp, nil
}

// markChanged rewrites the store's generation, which is what tells a handle
// that already loaded the directory to re-read it. It runs once per writing
// operation, before the first file that operation touches and under the
// exclusive lock, so no reader compares the file mid-write and a crash after it
// leaves the store marked changed rather than silently stale. A read-only
// operation never announces, so an idle long-poll cannot make other handles
// re-read the whole directory.
func (b *Backend) markChanged() error {
	if b.wrote {
		return nil
	}
	token := make([]byte, 16)
	if _, err := rand.Read(token); err != nil {
		return fmt.Errorf("generate store generation: %w", err)
	}
	if err := os.WriteFile(b.generationPath(), []byte(hex.EncodeToString(token)+"\n"), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", b.generationPath(), err)
	}
	b.wrote = true
	return nil
}

// writeRecord announces the write and stores data atomically at path.
func (b *Backend) writeRecord(path string, data []byte) error {
	if err := b.markChanged(); err != nil {
		return err
	}
	return writeFileAtomic(path, data)
}

// removeRecord announces the write and removes path.
func (b *Backend) removeRecord(path string) error {
	if err := b.markChanged(); err != nil {
		return err
	}
	return removeFile(path)
}

// reload replaces the in-memory index with the directory on disk. It stamps
// before reading, so a change that lands after the read is noticed by the next
// operation rather than lost. The caller holds mu and the store lock.
func (b *Backend) reload() error {
	stamp, err := b.stampDisk()
	if err != nil {
		return err
	}
	fresh := newHandle(b.root)
	if err := fresh.load(); err != nil {
		return err
	}
	b.tasks = fresh.tasks
	b.projects = fresh.projects
	b.actors = fresh.actors
	b.artifacts = fresh.artifacts
	b.messages = fresh.messages
	b.runs = fresh.runs
	b.pipelines = fresh.pipelines
	b.events = fresh.events
	b.stamp = stamp
	return nil
}

// lock takes this handle's mutex and the store's cross-process advisory lock —
// shared for reads, exclusive for writes — and returns its release.
func (b *Backend) lock(exclusive bool) (func(), error) {
	b.mu.Lock()
	release, err := filelock.Acquire(b.root, exclusive)
	if err != nil {
		b.mu.Unlock()
		return nil, err
	}
	return func() {
		release()
		b.mu.Unlock()
	}, nil
}

// beginRead takes the shared lock and makes the index current, so a handle that
// opened the directory before another ft process wrote serves that write.
func (b *Backend) beginRead() (func(), error) { return b.begin(false) }

// beginWrite takes the exclusive lock and makes the index current, so a write
// applies to the directory as the other processes left it. The write marks the
// store changed for the handles that already loaded it, once per operation and
// only when it stores something.
func (b *Backend) beginWrite() (func(), error) { return b.begin(true) }

func (b *Backend) begin(exclusive bool) (func(), error) {
	release, err := b.lock(exclusive)
	if err != nil {
		return nil, err
	}
	fail := func(err error) (func(), error) {
		release()
		return nil, err
	}
	stamp, err := b.stampDisk()
	if err != nil {
		return fail(err)
	}
	if !stamp.same(b.stamp) {
		if err := b.reload(); err != nil {
			return fail(err)
		}
	}
	if !exclusive {
		return release, nil
	}
	b.wrote = false
	return func() {
		// Record the state this handle just wrote, so its own write does not
		// look foreign to the next operation and force a reload. On error the
		// old stamp stays, and the next operation re-reads.
		if b.wrote {
			if stamp, err := b.stampDisk(); err == nil {
				b.stamp = stamp
			}
		}
		release()
	}, nil
}

// --- paths and writes ---

func (b *Backend) docPath(dir, id string) string {
	return filepath.Join(b.root, dir, id+".md")
}

func (b *Backend) dataPath(dir, id string) string {
	return filepath.Join(b.root, dir, id+".json")
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
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
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
	if err := b.writeRecord(b.docPath("projects", string(project.ID)), data); err != nil {
		return err
	}
	b.projects[project.ID] = &projectRecord{project: clone.Project(*project)}
	return nil
}

func (r *projectRepo) Get(_ context.Context, id core.ProjectID) (*core.Project, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	record, ok := b.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	project := clone.Project(record.project)
	return &project, nil
}

func (r *projectRepo) List(_ context.Context) ([]*core.Project, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
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
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.projects[project.ID]
	if !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, project.ID)
	}
	data, err := encodeProject(*project, record.extra)
	if err != nil {
		return err
	}
	if err := b.writeRecord(b.docPath("projects", string(project.ID)), data); err != nil {
		return err
	}
	b.projects[project.ID] = &projectRecord{project: clone.Project(*project), extra: record.extra}
	return nil
}

func (r *projectRepo) Delete(_ context.Context, id core.ProjectID) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.projects[id]; !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	if err := b.removeRecord(b.docPath("projects", string(id))); err != nil {
		return err
	}
	delete(b.projects, id)
	return nil
}

// --- tasks ---

type taskRepo struct{ backend *Backend }

func (r *taskRepo) Create(_ context.Context, task *core.Ticket) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
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
	if err := b.writeRecord(b.docPath("tasks", string(task.ID)), data); err != nil {
		return err
	}
	b.tasks[task.ID] = &taskRecord{task: clone.Ticket(*task)}
	return nil
}

func (r *taskRepo) Get(_ context.Context, id core.TicketID) (*core.Ticket, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	record, ok := b.tasks[id]
	if !ok {
		return nil, fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	task := clone.Ticket(record.task)
	return &task, nil
}

func (r *taskRepo) List(_ context.Context, filter store.TicketFilter) ([]*core.Ticket, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	out := make([]*core.Ticket, 0, len(b.tasks))
	for _, record := range b.tasks {
		if !matchesTask(record.task, filter) {
			continue
		}
		task := clone.Ticket(record.task)
		out = append(out, &task)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *taskRepo) Search(_ context.Context, filter store.TicketFilter, query string) ([]store.TicketSearchHit, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	terms := store.LexicalTerms(query)
	hits := make([]store.TicketSearchHit, 0)
	for _, record := range b.tasks {
		if !matchesTask(record.task, filter) {
			continue
		}
		score, matched := store.LexicalTaskScore(&record.task, terms)
		if !matched {
			continue
		}
		task := clone.Ticket(record.task)
		hits = append(hits, store.TicketSearchHit{Ticket: &task, Score: score})
	}
	store.SortTaskSearchHits(hits)
	return hits, nil
}

func (r *taskRepo) Update(_ context.Context, task *core.Ticket) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.tasks[task.ID]
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	data, err := encodeTask(*task, record.extra)
	if err != nil {
		return err
	}
	if err := b.writeRecord(b.docPath("tasks", string(task.ID)), data); err != nil {
		return err
	}
	b.tasks[task.ID] = &taskRecord{task: clone.Ticket(*task), extra: record.extra}
	return nil
}

func (r *taskRepo) UpdateExpected(_ context.Context, task *core.Ticket, expected time.Time) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
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
	if err := b.writeRecord(b.docPath("tasks", string(task.ID)), data); err != nil {
		return err
	}
	b.tasks[task.ID] = &taskRecord{task: clone.Ticket(*task), extra: record.extra}
	return nil
}

func (r *taskRepo) Delete(_ context.Context, id core.TicketID) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.tasks[id]; !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	if err := b.removeRecord(b.docPath("tasks", string(id))); err != nil {
		return err
	}
	delete(b.tasks, id)
	return nil
}

// Promoted answers from the tasks on disk: the lookup is the promotion guard's
// read half, and another process may have promoted the capture since this
// handle loaded the directory.
func (r *taskRepo) Promoted(_ context.Context, captureID core.TicketID) (*core.Ticket, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	return b.promoted(captureID), nil
}

// CreatePromoted holds the exclusive lock across the lookup and the insert, so
// one capture is promoted once across processes, not only across goroutines in
// this one.
func (r *taskRepo) CreatePromoted(_ context.Context, captureID core.TicketID, task *core.Ticket) (*core.Ticket, bool, error) {
	if err := store.ValidatePromotion(captureID, task); err != nil {
		return nil, false, err
	}
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return nil, false, err
	}
	defer release()
	if existing := b.promoted(captureID); existing != nil {
		return existing, false, nil
	}
	if _, ok := b.tasks[task.ID]; ok {
		return nil, false, fmt.Errorf("%w: task %s", core.ErrAlreadyExists, task.ID)
	}
	if err := checkID("task", string(task.ID)); err != nil {
		return nil, false, err
	}
	data, err := encodeTask(*task, nil)
	if err != nil {
		return nil, false, err
	}
	if err := b.writeRecord(b.docPath("tasks", string(task.ID)), data); err != nil {
		return nil, false, err
	}
	stored := clone.Ticket(*task)
	b.tasks[task.ID] = &taskRecord{task: stored}
	return &stored, true, nil
}

// promoted returns the capture's promoted task, or nil. The caller holds mu.
func (b *Backend) promoted(captureID core.TicketID) *core.Ticket {
	tasks := make([]*core.Ticket, 0, len(b.tasks))
	for _, record := range b.tasks {
		task := record.task
		tasks = append(tasks, &task)
	}
	if promoted := store.PromotedTask(tasks, captureID); promoted != nil {
		cloned := clone.Ticket(*promoted)
		return &cloned
	}
	return nil
}

func matchesTask(task core.Ticket, filter store.TicketFilter) bool {
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

func taskDependsOn(task core.Ticket, id core.TicketID) bool {
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
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
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
	if err := b.writeRecord(b.docPath("actors", string(actor.ID)), data); err != nil {
		return err
	}
	b.actors[actor.ID] = &actorRecord{actor: clone.Actor(*actor)}
	return nil
}

func (r *actorRepo) Get(_ context.Context, id core.ActorID) (*core.Actor, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	record, ok := b.actors[id]
	if !ok {
		return nil, fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	actor := clone.Actor(record.actor)
	return &actor, nil
}

func (r *actorRepo) FindByName(_ context.Context, name string) (*core.Actor, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
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
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
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
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.actors[actor.ID]
	if !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, actor.ID)
	}
	data, err := encodeActor(*actor, record.extra)
	if err != nil {
		return err
	}
	if err := b.writeRecord(b.docPath("actors", string(actor.ID)), data); err != nil {
		return err
	}
	b.actors[actor.ID] = &actorRecord{actor: clone.Actor(*actor), extra: record.extra}
	return nil
}

func (r *actorRepo) Delete(_ context.Context, id core.ActorID) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.actors[id]; !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	if err := b.removeRecord(b.docPath("actors", string(id))); err != nil {
		return err
	}
	delete(b.actors, id)
	return nil
}

// --- artifacts ---

type artifactRepo struct{ backend *Backend }

func (r *artifactRepo) Create(_ context.Context, artifact *core.Artifact) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
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
	if err := b.writeRecord(b.docPath("artifacts", string(artifact.ID)), data); err != nil {
		return err
	}
	b.artifacts[artifact.ID] = &artifactRecord{artifact: clone.Artifact(*artifact)}
	return nil
}

func (r *artifactRepo) Get(_ context.Context, id core.ArtifactID) (*core.Artifact, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	record, ok := b.artifacts[id]
	if !ok {
		return nil, fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	artifact := clone.Artifact(record.artifact)
	return &artifact, nil
}

func (r *artifactRepo) List(_ context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
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
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
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
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.artifacts[artifact.ID]
	if !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	data, err := encodeArtifact(*artifact, record.extra)
	if err != nil {
		return err
	}
	if err := b.writeRecord(b.docPath("artifacts", string(artifact.ID)), data); err != nil {
		return err
	}
	b.artifacts[artifact.ID] = &artifactRecord{artifact: clone.Artifact(*artifact), extra: record.extra}
	return nil
}

func (r *artifactRepo) Delete(_ context.Context, id core.ArtifactID) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.artifacts[id]; !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	if err := b.removeRecord(b.docPath("artifacts", string(id))); err != nil {
		return err
	}
	delete(b.artifacts, id)
	return nil
}

// --- events ---

type eventRepo struct{ backend *Backend }

func (r *eventRepo) Append(_ context.Context, event *core.Event) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if err := b.appendEvent(event); err != nil {
		return err
	}
	b.events = append(b.events, clone.Event(*event))
	return nil
}

func (b *Backend) appendEvent(event *core.Event) error {
	if err := b.markChanged(); err != nil {
		return err
	}
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
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	out := make([]*core.Event, 0)
	for i := len(b.events) - 1; i >= 0; i-- {
		event := b.events[i]
		if filter.ProjectID != "" && event.ProjectID != filter.ProjectID {
			continue
		}
		if filter.TicketID != nil && (event.TicketID == nil || *event.TicketID != *filter.TicketID) {
			continue
		}
		if len(filter.Kinds) > 0 && !containsKind(filter.Kinds, event.Kind) {
			continue
		}
		if filter.Session != "" && event.Session != filter.Session {
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

// --- messages ---

type messageRepo struct{ backend *Backend }

func (r *messageRepo) Create(_ context.Context, message *core.Message) error {
	if err := message.Validate(0); err != nil {
		return err
	}
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.messages[message.ID]; ok {
		return fmt.Errorf("%w: message %s", core.ErrAlreadyExists, message.ID)
	}
	if err := checkID("message", string(message.ID)); err != nil {
		return err
	}
	if err := b.writeMessage(*message); err != nil {
		return err
	}
	b.messages[message.ID] = &messageRecord{message: clone.Message(*message)}
	return nil
}

func (r *messageRepo) Get(_ context.Context, id core.MessageID) (*core.Message, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	record, ok := b.messages[id]
	if !ok {
		return nil, fmt.Errorf("%w: message %s", core.ErrNotFound, id)
	}
	message := clone.Message(record.message)
	return &message, nil
}

func (r *messageRepo) List(_ context.Context, filter store.MessageFilter) ([]*core.Message, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	out := make([]*core.Message, 0, len(b.messages))
	for _, record := range b.messages {
		if !store.MatchesMessageFilter(&record.message, filter) {
			continue
		}
		message := clone.Message(record.message)
		out = append(out, &message)
	}
	sort.Slice(out, func(i, j int) bool { return store.CompareMessages(out[i], out[j]) < 0 })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (r *messageRepo) Update(_ context.Context, message *core.Message) error {
	if err := message.Validate(0); err != nil {
		return err
	}
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.messages[message.ID]; !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, message.ID)
	}
	if err := b.writeMessage(*message); err != nil {
		return err
	}
	b.messages[message.ID] = &messageRecord{message: clone.Message(*message)}
	return nil
}

func (r *messageRepo) Claim(ctx context.Context, req store.ClaimRequest) (*core.Message, error) {
	return store.AwaitClaim(ctx, req.Wait, func() (*core.Message, error) { return r.claimOnce(req) })
}

func (r *messageRepo) claimOnce(req store.ClaimRequest) (*core.Message, error) {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return nil, err
	}
	defer release()
	var (
		best   core.Message
		bestID core.MessageID
		found  bool
	)
	for id, record := range b.messages {
		message := record.message
		if !store.Claimable(&message, req) {
			continue
		}
		if !found || store.CompareMessages(&message, &best) < 0 {
			best = message
			bestID = id
			found = true
		}
	}
	if !found {
		return nil, nil
	}
	now := time.Now().UTC()
	lease := req.Lease
	if lease <= 0 {
		lease = store.DefaultMessageLease
	}
	until := now.Add(lease)
	runID := req.RunID
	best.State = core.MessageDelivered
	best.RunID = &runID
	best.LeaseUntil = &until
	best.DeliveredAt = &now
	best.Attempts++
	best.UpdatedAt = now
	if err := b.writeMessage(best); err != nil {
		return nil, err
	}
	b.messages[bestID] = &messageRecord{message: best}
	cloned := clone.Message(best)
	return &cloned, nil
}

func (r *messageRepo) Ack(_ context.Context, req store.AckRequest) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.messages[req.ID]
	if !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, req.ID)
	}
	message := record.message
	now := time.Now().UTC()
	switch req.State {
	case core.MessageRead:
		if message.State == core.MessageRead {
			return nil
		}
		if err := checkAckOwner(message, req.RunID); err != nil {
			return err
		}
		message.State = core.MessageRead
		message.ReadAt = &now
	case core.MessageFailed:
		if message.State == core.MessageFailed {
			return nil
		}
		if err := checkAckOwner(message, req.RunID); err != nil {
			return err
		}
		message.State = core.MessageFailed
		message.Error = req.Error
	default:
		return fmt.Errorf("%w: ack state %q must be read or failed", core.ErrInvalid, req.State)
	}
	message.UpdatedAt = now
	if err := b.writeMessage(message); err != nil {
		return err
	}
	b.messages[req.ID] = &messageRecord{message: message}
	return nil
}

// checkAckOwner requires a delivered message owned by runID.
func checkAckOwner(message core.Message, runID core.RunID) error {
	if message.State != core.MessageDelivered {
		return fmt.Errorf("%w: message %s is %s, not delivered", core.ErrInvalid, message.ID, message.State)
	}
	if message.RunID == nil || *message.RunID != runID {
		return fmt.Errorf("%w: message %s is not owned by run %s", core.ErrInvalid, message.ID, runID)
	}
	return nil
}

func (r *messageRepo) Nack(_ context.Context, id core.MessageID, runID core.RunID, reason string) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.messages[id]
	if !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, id)
	}
	message := record.message
	if err := checkAckOwner(message, runID); err != nil {
		return err
	}
	now := time.Now().UTC()
	message.Error = reason
	message.UpdatedAt = now
	if message.Attempts >= store.MaxMessageAttempts {
		message.State = core.MessageFailed
	} else {
		message.State = core.MessageQueued
		message.RunID = nil
		message.LeaseUntil = nil
	}
	if err := b.writeMessage(message); err != nil {
		return err
	}
	b.messages[id] = &messageRecord{message: message}
	return nil
}

func (r *messageRepo) RequeueExpired(_ context.Context, now time.Time, maxAttempts int) (int, error) {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return 0, err
	}
	defer release()
	moved := 0
	for id, record := range b.messages {
		message := record.message
		if message.State != core.MessageDelivered || message.LeaseUntil == nil || message.LeaseUntil.After(now) {
			continue
		}
		if message.Attempts >= maxAttempts {
			message.State = core.MessageFailed
			message.Error = "lease expired"
		} else {
			message.State = core.MessageQueued
			message.RunID = nil
			message.LeaseUntil = nil
		}
		message.UpdatedAt = now
		if err := b.writeMessage(message); err != nil {
			return moved, err
		}
		b.messages[id] = &messageRecord{message: message}
		moved++
	}
	return moved, nil
}

func (r *messageRepo) Prune(_ context.Context, before time.Time, states []core.MessageState) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return 0, err
	}
	defer release()
	removed := 0
	for id, record := range b.messages {
		message := record.message
		if !message.State.Terminal() || !message.UpdatedAt.Before(before) {
			continue
		}
		prunable := false
		for _, state := range states {
			if message.State == state {
				prunable = true
				break
			}
		}
		if !prunable {
			continue
		}
		if err := b.removeRecord(b.dataPath("messages", string(id))); err != nil {
			return removed, err
		}
		delete(b.messages, id)
		removed++
	}
	return removed, nil
}

func (b *Backend) writeMessage(message core.Message) error {
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode message %s: %w", message.ID, err)
	}
	return b.writeRecord(b.dataPath("messages", string(message.ID)), data)
}

// --- runs ---

type runRepo struct{ backend *Backend }

func (r *runRepo) Register(_ context.Context, run *core.Run) (*core.Run, error) {
	if err := run.Validate(); err != nil {
		return nil, err
	}
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return nil, err
	}
	defer release()
	for id, record := range b.runs {
		existing := record.run
		if existing.ProjectID == run.ProjectID && existing.ActorID == run.ActorID && existing.Host == run.Host && existing.PID == run.PID {
			existing.TaskID = run.TaskID
			existing.Session = run.Session
			existing.Harness = run.Harness
			existing.CanInject = run.CanInject
			existing.SeenAt = run.SeenAt
			existing.LeaseUntil = run.LeaseUntil
			if err := b.writeRun(existing); err != nil {
				return nil, err
			}
			b.runs[id] = &runRecord{run: existing}
			cloned := clone.Run(existing)
			return &cloned, nil
		}
	}
	if err := checkID("run", string(run.ID)); err != nil {
		return nil, err
	}
	if err := b.writeRun(*run); err != nil {
		return nil, err
	}
	b.runs[run.ID] = &runRecord{run: clone.Run(*run)}
	cloned := clone.Run(*run)
	return &cloned, nil
}

func (r *runRepo) Heartbeat(_ context.Context, id core.RunID, now time.Time, lease time.Duration) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.runs[id]
	if !ok {
		return fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	run := record.run
	run.SeenAt = now
	run.LeaseUntil = now.Add(lease)
	if err := b.writeRun(run); err != nil {
		return err
	}
	b.runs[id] = &runRecord{run: run}
	return nil
}

func (r *runRepo) List(_ context.Context, filter store.RunFilter) ([]*core.Run, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	now := filter.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := make([]*core.Run, 0, len(b.runs))
	for _, record := range b.runs {
		run := record.run
		if !store.MatchRunFilter(&run, filter, now) {
			continue
		}
		cloned := clone.Run(run)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (r *runRepo) Delete(_ context.Context, id core.RunID) error {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.runs[id]; !ok {
		return fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	if err := b.removeRecord(b.dataPath("runs", string(id))); err != nil {
		return err
	}
	delete(b.runs, id)
	return nil
}

func (b *Backend) writeRun(run core.Run) error {
	data, err := json.Marshal(run)
	if err != nil {
		return fmt.Errorf("encode run %s: %w", run.ID, err)
	}
	return b.writeRecord(b.dataPath("runs", string(run.ID)), data)
}

// --- pipelines ---

type pipelineRepo struct{ backend *Backend }

func (r *pipelineRepo) Create(_ context.Context, pipeline *core.Pipeline) error {
	if err := pipeline.Validate(); err != nil {
		return err
	}
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	if _, ok := b.pipelines[pipeline.ID]; ok {
		return fmt.Errorf("%w: pipeline %s", core.ErrAlreadyExists, pipeline.ID)
	}
	for _, record := range b.pipelines {
		if record.pipeline.ProjectID == pipeline.ProjectID && record.pipeline.CaptureID == pipeline.CaptureID {
			return fmt.Errorf("%w: pipeline for capture %s in project %s", core.ErrAlreadyExists, pipeline.CaptureID, pipeline.ProjectID)
		}
	}
	if err := checkID("pipeline", string(pipeline.ID)); err != nil {
		return err
	}
	if err := b.writePipeline(*pipeline); err != nil {
		return err
	}
	b.pipelines[pipeline.ID] = &pipelineRecord{pipeline: clone.Pipeline(*pipeline)}
	return nil
}

func (r *pipelineRepo) Get(_ context.Context, id core.PipelineID) (*core.Pipeline, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	record, ok := b.pipelines[id]
	if !ok {
		return nil, fmt.Errorf("%w: pipeline %s", core.ErrNotFound, id)
	}
	cloned := clone.Pipeline(record.pipeline)
	return &cloned, nil
}

func (r *pipelineRepo) List(_ context.Context, filter store.PipelineFilter) ([]*core.Pipeline, error) {
	b := r.backend
	release, err := b.beginRead()
	if err != nil {
		return nil, err
	}
	defer release()
	out := make([]*core.Pipeline, 0, len(b.pipelines))
	for _, record := range b.pipelines {
		if !store.MatchesPipelineFilter(&record.pipeline, filter) {
			continue
		}
		cloned := clone.Pipeline(record.pipeline)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return store.ComparePipelines(out[i], out[j]) < 0 })
	if filter.Limit > 0 && len(out) > filter.Limit {
		out = out[:filter.Limit]
	}
	return out, nil
}

func (r *pipelineRepo) Update(_ context.Context, pipeline *core.Pipeline) error {
	if err := pipeline.Validate(); err != nil {
		return err
	}
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return err
	}
	defer release()
	record, ok := b.pipelines[pipeline.ID]
	if !ok {
		return fmt.Errorf("%w: pipeline %s", core.ErrNotFound, pipeline.ID)
	}
	if record.pipeline.State.Terminal() {
		return fmt.Errorf("%w: pipeline %s is %s and immutable", core.ErrConflict, pipeline.ID, record.pipeline.State)
	}
	if record.pipeline.ProjectID != pipeline.ProjectID || record.pipeline.CaptureID != pipeline.CaptureID {
		return fmt.Errorf("%w: pipeline %s identity is immutable", core.ErrConflict, pipeline.ID)
	}
	if err := b.writePipeline(*pipeline); err != nil {
		return err
	}
	b.pipelines[pipeline.ID] = &pipelineRecord{pipeline: clone.Pipeline(*pipeline)}
	return nil
}

func (r *pipelineRepo) Claim(_ context.Context, req store.PipelineClaimRequest) (*core.Pipeline, error) {
	b := r.backend
	release, err := b.beginWrite()
	if err != nil {
		return nil, err
	}
	defer release()
	if req.ID != nil {
		record, ok := b.pipelines[*req.ID]
		if !ok {
			return nil, fmt.Errorf("%w: pipeline %s", core.ErrNotFound, *req.ID)
		}
		pipeline := record.pipeline
		state, err := store.ClaimByID(&pipeline, req)
		if err != nil {
			return nil, err
		}
		if state != pipeline.State {
			pipeline.State = state
			pipeline.UpdatedAt = time.Now().UTC()
			if err := b.writePipeline(pipeline); err != nil {
				return nil, err
			}
			b.pipelines[*req.ID] = &pipelineRecord{pipeline: pipeline}
		}
		cloned := clone.Pipeline(pipeline)
		return &cloned, nil
	}
	var (
		best   core.Pipeline
		bestID core.PipelineID
		found  bool
	)
	for id, record := range b.pipelines {
		pipeline := record.pipeline
		if pipeline.State != core.PipelineQueued {
			continue
		}
		if req.ProjectID != "" && pipeline.ProjectID != req.ProjectID {
			continue
		}
		if !found || store.ComparePipelines(&pipeline, &best) < 0 {
			best = pipeline
			bestID = id
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("%w: no queued pipeline for project %s", core.ErrNotFound, req.ProjectID)
	}
	best.State = core.PipelineGrooming
	best.UpdatedAt = time.Now().UTC()
	if err := b.writePipeline(best); err != nil {
		return nil, err
	}
	b.pipelines[bestID] = &pipelineRecord{pipeline: best}
	cloned := clone.Pipeline(best)
	return &cloned, nil
}

func (b *Backend) writePipeline(pipeline core.Pipeline) error {
	data, err := json.Marshal(pipeline)
	if err != nil {
		return fmt.Errorf("encode pipeline %s: %w", pipeline.ID, err)
	}
	return b.writeRecord(b.dataPath("pipelines", string(pipeline.ID)), data)
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", path, err)
	}
	return nil
}
