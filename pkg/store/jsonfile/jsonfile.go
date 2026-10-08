// Package jsonfile is a single-document JSON storage backend with atomic
// writes. It is intended for local, single-process use.
package jsonfile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/internal/clone"
)

const defaultPath = ".factotum/factotum.json"

type state struct {
	Projects  []core.Project  `json:"projects,omitempty"`
	Tasks     []core.Ticket   `json:"tasks,omitempty"`
	Actors    []core.Actor    `json:"actors,omitempty"`
	Artifacts []core.Artifact `json:"artifacts,omitempty"`
	Events    []core.Event    `json:"events,omitempty"`
	Messages  []core.Message  `json:"messages,omitempty"`
	Runs      []core.Run      `json:"runs,omitempty"`
	Pipelines []core.Pipeline `json:"pipelines,omitempty"`
}

type Backend struct {
	mu    sync.Mutex
	path  string
	state state
}

func Open(_ context.Context, cfg store.Config) (store.Backend, error) {
	path := cfg.Option("path")
	if path == "" {
		path = defaultPath
	}
	loaded, err := load(path)
	if err != nil {
		return nil, err
	}
	return &Backend{path: path, state: loaded}, nil
}

func load(path string) (state, error) {
	var loaded state
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return loaded, nil
	}
	if err != nil {
		return loaded, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) == 0 {
		return loaded, nil
	}
	if err := json.Unmarshal(data, &loaded); err != nil {
		return loaded, fmt.Errorf("parse %s: %w", path, err)
	}
	return loaded, nil
}

func (b *Backend) persist() error {
	data, err := json.MarshalIndent(b.state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(b.path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".factotum-*.tmp")
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
	if err := os.Rename(tmpName, b.path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("replace state file: %w", err)
	}
	return nil
}

func (b *Backend) Close() error { return nil }

func (b *Backend) Projects() store.ProjectRepo { return &projectRepo{backend: b} }
func (b *Backend) Tickets() store.TicketRepo   { return &taskRepo{backend: b} }
func (b *Backend) Actors() store.ActorRepo     { return &actorRepo{backend: b} }
func (b *Backend) Artifacts() store.ArtifactRepo {
	return &artifactRepo{backend: b}
}
func (b *Backend) Events() store.EventRepo     { return &eventRepo{backend: b} }
func (b *Backend) Messages() store.MessageRepo { return &messageRepo{backend: b} }
func (b *Backend) Runs() store.RunRepo         { return &runRepo{backend: b} }
func (b *Backend) Pipelines() store.PipelineRepo {
	return &pipelineRepo{backend: b}
}

type projectRepo struct{ backend *Backend }

func (r *projectRepo) Create(_ context.Context, project *core.Project) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.projectIndex(project.ID); ok {
		return fmt.Errorf("%w: project %s", core.ErrAlreadyExists, project.ID)
	}
	r.backend.state.Projects = append(r.backend.state.Projects, clone.Project(*project))
	return r.backend.persist()
}

func (r *projectRepo) Get(_ context.Context, id core.ProjectID) (*core.Project, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.projectIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	project := clone.Project(r.backend.state.Projects[index])
	return &project, nil
}

func (r *projectRepo) List(_ context.Context) ([]*core.Project, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Project, 0, len(r.backend.state.Projects))
	for _, project := range r.backend.state.Projects {
		cloned := clone.Project(project)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *projectRepo) Update(_ context.Context, project *core.Project) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.projectIndex(project.ID)
	if !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, project.ID)
	}
	r.backend.state.Projects[index] = clone.Project(*project)
	return r.backend.persist()
}

func (r *projectRepo) Delete(_ context.Context, id core.ProjectID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.projectIndex(id)
	if !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	r.backend.state.Projects = append(r.backend.state.Projects[:index], r.backend.state.Projects[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) projectIndex(id core.ProjectID) (int, bool) {
	for i, project := range b.state.Projects {
		if project.ID == id {
			return i, true
		}
	}
	return 0, false
}

type taskRepo struct{ backend *Backend }

func (r *taskRepo) Create(_ context.Context, task *core.Ticket) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.taskIndex(task.ID); ok {
		return fmt.Errorf("%w: task %s", core.ErrAlreadyExists, task.ID)
	}
	r.backend.state.Tasks = append(r.backend.state.Tasks, clone.Ticket(*task))
	return r.backend.persist()
}

func (r *taskRepo) Get(_ context.Context, id core.TicketID) (*core.Ticket, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.taskIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	task := clone.Ticket(r.backend.state.Tasks[index])
	return &task, nil
}

func (r *taskRepo) List(_ context.Context, filter store.TicketFilter) ([]*core.Ticket, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Ticket, 0, len(r.backend.state.Tasks))
	for _, task := range r.backend.state.Tasks {
		if !matchesTask(task, filter) {
			continue
		}
		cloned := clone.Ticket(task)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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

// taskDependsOn reports whether task lists id among its direct dependencies.
func taskDependsOn(task core.Ticket, id core.TicketID) bool {
	for _, dep := range task.Deps {
		if dep == id {
			return true
		}
	}
	return false
}

func (r *taskRepo) Search(_ context.Context, filter store.TicketFilter, query string) ([]store.TicketSearchHit, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.TicketSearchHit, 0)
	for _, task := range r.backend.state.Tasks {
		if !matchesTask(task, filter) {
			continue
		}
		score, matched := store.LexicalTaskScore(&task, terms)
		if !matched {
			continue
		}
		cloned := clone.Ticket(task)
		hits = append(hits, store.TicketSearchHit{Ticket: &cloned, Score: score})
	}
	store.SortTaskSearchHits(hits)
	return hits, nil
}

func (r *taskRepo) Update(_ context.Context, task *core.Ticket) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.taskIndex(task.ID)
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	r.backend.state.Tasks[index] = clone.Ticket(*task)
	return r.backend.persist()
}

func (r *taskRepo) UpdateExpected(_ context.Context, task *core.Ticket, expected time.Time) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.taskIndex(task.ID)
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	if !r.backend.state.Tasks[index].UpdatedAt.Equal(expected) {
		return fmt.Errorf("%w: task %s was modified", core.ErrConflict, task.ID)
	}
	r.backend.state.Tasks[index] = clone.Ticket(*task)
	return r.backend.persist()
}

func (r *taskRepo) Delete(_ context.Context, id core.TicketID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.taskIndex(id)
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	r.backend.state.Tasks = append(r.backend.state.Tasks[:index], r.backend.state.Tasks[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) taskIndex(id core.TicketID) (int, bool) {
	for i, task := range b.state.Tasks {
		if task.ID == id {
			return i, true
		}
	}
	return 0, false
}

type actorRepo struct{ backend *Backend }

func (r *actorRepo) Create(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actorIndex(actor.ID); ok {
		return fmt.Errorf("%w: actor %s", core.ErrAlreadyExists, actor.ID)
	}
	r.backend.state.Actors = append(r.backend.state.Actors, clone.Actor(*actor))
	return r.backend.persist()
}

func (r *actorRepo) Get(_ context.Context, id core.ActorID) (*core.Actor, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.actorIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	actor := clone.Actor(r.backend.state.Actors[index])
	return &actor, nil
}

func (r *actorRepo) FindByName(_ context.Context, name string) (*core.Actor, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	for _, actor := range r.backend.state.Actors {
		if actor.Name == name {
			found := clone.Actor(actor)
			return &found, nil
		}
	}
	return nil, fmt.Errorf("%w: actor named %q", core.ErrNotFound, name)
}

func (r *actorRepo) List(_ context.Context) ([]*core.Actor, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Actor, 0, len(r.backend.state.Actors))
	for _, actor := range r.backend.state.Actors {
		cloned := clone.Actor(actor)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *actorRepo) Update(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.actorIndex(actor.ID)
	if !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, actor.ID)
	}
	r.backend.state.Actors[index] = clone.Actor(*actor)
	return r.backend.persist()
}

func (r *actorRepo) Delete(_ context.Context, id core.ActorID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.actorIndex(id)
	if !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	r.backend.state.Actors = append(r.backend.state.Actors[:index], r.backend.state.Actors[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) actorIndex(id core.ActorID) (int, bool) {
	for i, actor := range b.state.Actors {
		if actor.ID == id {
			return i, true
		}
	}
	return 0, false
}

type artifactRepo struct{ backend *Backend }

func (r *artifactRepo) Create(_ context.Context, artifact *core.Artifact) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.artifactIndex(artifact.ID); ok {
		return fmt.Errorf("%w: artifact %s", core.ErrAlreadyExists, artifact.ID)
	}
	r.backend.state.Artifacts = append(r.backend.state.Artifacts, clone.Artifact(*artifact))
	return r.backend.persist()
}

func (r *artifactRepo) Get(_ context.Context, id core.ArtifactID) (*core.Artifact, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.artifactIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	artifact := clone.Artifact(r.backend.state.Artifacts[index])
	return &artifact, nil
}

func (r *artifactRepo) List(_ context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Artifact, 0, len(r.backend.state.Artifacts))
	for _, artifact := range r.backend.state.Artifacts {
		if !store.MatchesArtifactFilter(&artifact, filter) {
			continue
		}
		cloned := clone.Artifact(artifact)
		out = append(out, &cloned)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *artifactRepo) Search(_ context.Context, filter store.ArtifactFilter, query string) ([]store.SearchHit, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.SearchHit, 0)
	for _, artifact := range r.backend.state.Artifacts {
		if !store.MatchesArtifactFilter(&artifact, filter) {
			continue
		}
		score, matched := store.LexicalScore(&artifact, terms)
		if !matched {
			continue
		}
		cloned := clone.Artifact(artifact)
		hits = append(hits, store.SearchHit{Artifact: &cloned, Score: score})
	}
	store.SortSearchHits(hits)
	return hits, nil
}

func (r *artifactRepo) Update(_ context.Context, artifact *core.Artifact) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.artifactIndex(artifact.ID)
	if !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	r.backend.state.Artifacts[index] = clone.Artifact(*artifact)
	return r.backend.persist()
}

func (r *artifactRepo) Delete(_ context.Context, id core.ArtifactID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.artifactIndex(id)
	if !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	r.backend.state.Artifacts = append(r.backend.state.Artifacts[:index], r.backend.state.Artifacts[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) artifactIndex(id core.ArtifactID) (int, bool) {
	for i, artifact := range b.state.Artifacts {
		if artifact.ID == id {
			return i, true
		}
	}
	return 0, false
}

type eventRepo struct{ backend *Backend }

func (r *eventRepo) Append(_ context.Context, event *core.Event) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	r.backend.state.Events = append(r.backend.state.Events, clone.Event(*event))
	return r.backend.persist()
}

func (r *eventRepo) List(_ context.Context, filter store.EventFilter) ([]*core.Event, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Event, 0)
	for i := len(r.backend.state.Events) - 1; i >= 0; i-- {
		event := r.backend.state.Events[i]
		if filter.ProjectID != "" && event.ProjectID != filter.ProjectID {
			continue
		}
		if filter.TicketID != nil && (event.TicketID == nil || *event.TicketID != *filter.TicketID) {
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

type messageRepo struct{ backend *Backend }

func (r *messageRepo) Create(_ context.Context, message *core.Message) error {
	if err := message.Validate(0); err != nil {
		return err
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.messageIndex(message.ID); ok {
		return fmt.Errorf("%w: message %s", core.ErrAlreadyExists, message.ID)
	}
	r.backend.state.Messages = append(r.backend.state.Messages, clone.Message(*message))
	return r.backend.persist()
}

func (r *messageRepo) Get(_ context.Context, id core.MessageID) (*core.Message, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.messageIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: message %s", core.ErrNotFound, id)
	}
	cloned := clone.Message(r.backend.state.Messages[index])
	return &cloned, nil
}

func (r *messageRepo) List(_ context.Context, filter store.MessageFilter) ([]*core.Message, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Message, 0, len(r.backend.state.Messages))
	for _, message := range r.backend.state.Messages {
		if !store.MatchesMessageFilter(&message, filter) {
			continue
		}
		cloned := clone.Message(message)
		out = append(out, &cloned)
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
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.messageIndex(message.ID)
	if !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, message.ID)
	}
	r.backend.state.Messages[index] = clone.Message(*message)
	return r.backend.persist()
}

func (r *messageRepo) Claim(ctx context.Context, req store.ClaimRequest) (*core.Message, error) {
	return store.AwaitClaim(ctx, req.Wait, func() (*core.Message, error) { return r.claimOnce(req) })
}

func (r *messageRepo) claimOnce(req store.ClaimRequest) (*core.Message, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	var (
		best   core.Message
		bestID core.MessageID
		found  bool
	)
	for _, message := range r.backend.state.Messages {
		if !store.Claimable(&message, req) {
			continue
		}
		if !found || store.CompareMessages(&message, &best) < 0 {
			best = message
			bestID = message.ID
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
	index, _ := r.backend.messageIndex(bestID)
	r.backend.state.Messages[index] = best
	if err := r.backend.persist(); err != nil {
		return nil, err
	}
	cloned := clone.Message(best)
	return &cloned, nil
}

func (r *messageRepo) Ack(_ context.Context, req store.AckRequest) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.messageIndex(req.ID)
	if !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, req.ID)
	}
	message := r.backend.state.Messages[index]
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
	r.backend.state.Messages[index] = message
	return r.backend.persist()
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
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.messageIndex(id)
	if !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, id)
	}
	message := r.backend.state.Messages[index]
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
	r.backend.state.Messages[index] = message
	return r.backend.persist()
}

func (r *messageRepo) RequeueExpired(_ context.Context, now time.Time, maxAttempts int) (int, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	moved := 0
	for i, message := range r.backend.state.Messages {
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
		r.backend.state.Messages[i] = message
		moved++
	}
	if moved > 0 {
		if err := r.backend.persist(); err != nil {
			return 0, err
		}
	}
	return moved, nil
}

func (r *messageRepo) Prune(_ context.Context, before time.Time, states []core.MessageState) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	kept := r.backend.state.Messages[:0]
	removed := 0
	for _, message := range r.backend.state.Messages {
		if !message.State.Terminal() || !message.UpdatedAt.Before(before) || !containsMessageState(states, message.State) {
			kept = append(kept, message)
			continue
		}
		removed++
	}
	r.backend.state.Messages = kept
	if removed > 0 {
		if err := r.backend.persist(); err != nil {
			return 0, err
		}
	}
	return removed, nil
}

func containsMessageState(states []core.MessageState, state core.MessageState) bool {
	for _, candidate := range states {
		if candidate == state {
			return true
		}
	}
	return false
}

func (b *Backend) messageIndex(id core.MessageID) (int, bool) {
	for i, message := range b.state.Messages {
		if message.ID == id {
			return i, true
		}
	}
	return 0, false
}

type runRepo struct{ backend *Backend }

func (r *runRepo) Register(_ context.Context, run *core.Run) (*core.Run, error) {
	if err := run.Validate(); err != nil {
		return nil, err
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	for i, existing := range r.backend.state.Runs {
		if existing.ProjectID == run.ProjectID && existing.ActorID == run.ActorID && existing.Host == run.Host && existing.PID == run.PID {
			existing.TaskID = run.TaskID
			existing.Harness = run.Harness
			existing.CanInject = run.CanInject
			existing.SeenAt = run.SeenAt
			existing.LeaseUntil = run.LeaseUntil
			r.backend.state.Runs[i] = existing
			if err := r.backend.persist(); err != nil {
				return nil, err
			}
			cloned := clone.Run(existing)
			return &cloned, nil
		}
	}
	r.backend.state.Runs = append(r.backend.state.Runs, clone.Run(*run))
	if err := r.backend.persist(); err != nil {
		return nil, err
	}
	cloned := clone.Run(*run)
	return &cloned, nil
}

func (r *runRepo) Heartbeat(_ context.Context, id core.RunID, now time.Time, lease time.Duration) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.runIndex(id)
	if !ok {
		return fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	run := r.backend.state.Runs[index]
	run.SeenAt = now
	run.LeaseUntil = now.Add(lease)
	r.backend.state.Runs[index] = run
	return r.backend.persist()
}

func (r *runRepo) List(_ context.Context, filter store.RunFilter) ([]*core.Run, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	now := filter.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := make([]*core.Run, 0, len(r.backend.state.Runs))
	for _, run := range r.backend.state.Runs {
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
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.runIndex(id)
	if !ok {
		return fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	r.backend.state.Runs = append(r.backend.state.Runs[:index], r.backend.state.Runs[index+1:]...)
	return r.backend.persist()
}

func (b *Backend) runIndex(id core.RunID) (int, bool) {
	for i, run := range b.state.Runs {
		if run.ID == id {
			return i, true
		}
	}
	return 0, false
}

type pipelineRepo struct{ backend *Backend }

func (r *pipelineRepo) Create(_ context.Context, pipeline *core.Pipeline) error {
	if err := pipeline.Validate(); err != nil {
		return err
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.pipelineIndex(pipeline.ID); ok {
		return fmt.Errorf("%w: pipeline %s", core.ErrAlreadyExists, pipeline.ID)
	}
	for _, existing := range r.backend.state.Pipelines {
		if existing.ProjectID == pipeline.ProjectID && existing.CaptureID == pipeline.CaptureID {
			return fmt.Errorf("%w: pipeline for capture %s in project %s", core.ErrAlreadyExists, pipeline.CaptureID, pipeline.ProjectID)
		}
	}
	r.backend.state.Pipelines = append(r.backend.state.Pipelines, clone.Pipeline(*pipeline))
	return r.backend.persist()
}

func (r *pipelineRepo) Get(_ context.Context, id core.PipelineID) (*core.Pipeline, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.pipelineIndex(id)
	if !ok {
		return nil, fmt.Errorf("%w: pipeline %s", core.ErrNotFound, id)
	}
	cloned := clone.Pipeline(r.backend.state.Pipelines[index])
	return &cloned, nil
}

func (r *pipelineRepo) List(_ context.Context, filter store.PipelineFilter) ([]*core.Pipeline, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	out := make([]*core.Pipeline, 0, len(r.backend.state.Pipelines))
	for _, pipeline := range r.backend.state.Pipelines {
		if !store.MatchesPipelineFilter(&pipeline, filter) {
			continue
		}
		cloned := clone.Pipeline(pipeline)
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
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	index, ok := r.backend.pipelineIndex(pipeline.ID)
	if !ok {
		return fmt.Errorf("%w: pipeline %s", core.ErrNotFound, pipeline.ID)
	}
	if r.backend.state.Pipelines[index].State.Terminal() {
		return fmt.Errorf("%w: pipeline %s is %s and immutable", core.ErrConflict, pipeline.ID, r.backend.state.Pipelines[index].State)
	}
	if r.backend.state.Pipelines[index].ProjectID != pipeline.ProjectID || r.backend.state.Pipelines[index].CaptureID != pipeline.CaptureID {
		return fmt.Errorf("%w: pipeline %s identity is immutable", core.ErrConflict, pipeline.ID)
	}
	r.backend.state.Pipelines[index] = clone.Pipeline(*pipeline)
	return r.backend.persist()
}

func (r *pipelineRepo) Claim(_ context.Context, req store.PipelineClaimRequest) (*core.Pipeline, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	var (
		best  core.Pipeline
		bestI = -1
	)
	for i, pipeline := range r.backend.state.Pipelines {
		if pipeline.State != core.PipelineQueued {
			continue
		}
		if req.ProjectID != "" && pipeline.ProjectID != req.ProjectID {
			continue
		}
		if bestI < 0 || store.ComparePipelines(&pipeline, &best) < 0 {
			best = pipeline
			bestI = i
		}
	}
	if bestI < 0 {
		return nil, fmt.Errorf("%w: no queued pipeline for project %s", core.ErrNotFound, req.ProjectID)
	}
	best.State = core.PipelineGrooming
	best.UpdatedAt = time.Now().UTC()
	r.backend.state.Pipelines[bestI] = best
	if err := r.backend.persist(); err != nil {
		return nil, err
	}
	cloned := clone.Pipeline(best)
	return &cloned, nil
}

func (b *Backend) pipelineIndex(id core.PipelineID) (int, bool) {
	for i, pipeline := range b.state.Pipelines {
		if pipeline.ID == id {
			return i, true
		}
	}
	return 0, false
}
