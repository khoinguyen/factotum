// Package memory is the in-memory storage backend. It is the reference
// implementation of the store ports and passes the conformance suite.
package memory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/internal/clone"
)

type Backend struct {
	mu        sync.RWMutex
	projects  map[core.ProjectID]core.Project
	tasks     map[core.TicketID]core.Ticket
	actors    map[core.ActorID]core.Actor
	artifacts map[core.ArtifactID]core.Artifact
	events    []core.Event
	messages  map[core.MessageID]core.Message
	runs      map[core.RunID]core.Run
	pipelines map[core.PipelineID]core.Pipeline
}

func New() *Backend {
	return &Backend{
		projects:  make(map[core.ProjectID]core.Project),
		tasks:     make(map[core.TicketID]core.Ticket),
		actors:    make(map[core.ActorID]core.Actor),
		artifacts: make(map[core.ArtifactID]core.Artifact),
		messages:  make(map[core.MessageID]core.Message),
		runs:      make(map[core.RunID]core.Run),
		pipelines: make(map[core.PipelineID]core.Pipeline),
	}
}

func Open(_ context.Context, _ store.Config) (store.Backend, error) {
	return New(), nil
}

func (b *Backend) Close() error                { return nil }
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
	if _, ok := r.backend.projects[project.ID]; ok {
		return fmt.Errorf("%w: project %s", core.ErrAlreadyExists, project.ID)
	}
	r.backend.projects[project.ID] = clone.Project(*project)
	return nil
}

func (r *projectRepo) Get(_ context.Context, id core.ProjectID) (*core.Project, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	project, ok := r.backend.projects[id]
	if !ok {
		return nil, fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	cloned := clone.Project(project)
	return &cloned, nil
}

func (r *projectRepo) List(_ context.Context) ([]*core.Project, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.ProjectID, 0, len(r.backend.projects))
	for id := range r.backend.projects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Project, 0, len(ids))
	for _, id := range ids {
		cloned := clone.Project(r.backend.projects[id])
		out = append(out, &cloned)
	}
	return out, nil
}

func (r *projectRepo) Update(_ context.Context, project *core.Project) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.projects[project.ID]; !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, project.ID)
	}
	r.backend.projects[project.ID] = clone.Project(*project)
	return nil
}

func (r *projectRepo) Delete(_ context.Context, id core.ProjectID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.projects[id]; !ok {
		return fmt.Errorf("%w: project %s", core.ErrNotFound, id)
	}
	delete(r.backend.projects, id)
	return nil
}

type taskRepo struct{ backend *Backend }

func (r *taskRepo) Create(_ context.Context, task *core.Ticket) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.tasks[task.ID]; ok {
		return fmt.Errorf("%w: task %s", core.ErrAlreadyExists, task.ID)
	}
	r.backend.tasks[task.ID] = clone.Ticket(*task)
	return nil
}

func (r *taskRepo) Get(_ context.Context, id core.TicketID) (*core.Ticket, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	task, ok := r.backend.tasks[id]
	if !ok {
		return nil, fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	cloned := clone.Ticket(task)
	return &cloned, nil
}

func (r *taskRepo) List(_ context.Context, filter store.TicketFilter) ([]*core.Ticket, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.TicketID, 0, len(r.backend.tasks))
	for id, task := range r.backend.tasks {
		if !matchesTask(task, filter) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Ticket, 0, len(ids))
	for _, id := range ids {
		cloned := clone.Ticket(r.backend.tasks[id])
		out = append(out, &cloned)
	}
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
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.TicketSearchHit, 0)
	for _, task := range r.backend.tasks {
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
	if _, ok := r.backend.tasks[task.ID]; !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	r.backend.tasks[task.ID] = clone.Ticket(*task)
	return nil
}

func (r *taskRepo) UpdateExpected(_ context.Context, task *core.Ticket, expected time.Time) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	current, ok := r.backend.tasks[task.ID]
	if !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, task.ID)
	}
	if !current.UpdatedAt.Equal(expected) {
		return fmt.Errorf("%w: task %s was modified", core.ErrConflict, task.ID)
	}
	r.backend.tasks[task.ID] = clone.Ticket(*task)
	return nil
}

func (r *taskRepo) Delete(_ context.Context, id core.TicketID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.tasks[id]; !ok {
		return fmt.Errorf("%w: task %s", core.ErrNotFound, id)
	}
	delete(r.backend.tasks, id)
	return nil
}

type actorRepo struct{ backend *Backend }

func (r *actorRepo) Create(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actors[actor.ID]; ok {
		return fmt.Errorf("%w: actor %s", core.ErrAlreadyExists, actor.ID)
	}
	r.backend.actors[actor.ID] = *actor
	return nil
}

func (r *actorRepo) Get(_ context.Context, id core.ActorID) (*core.Actor, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	actor, ok := r.backend.actors[id]
	if !ok {
		return nil, fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	return &actor, nil
}

func (r *actorRepo) FindByName(_ context.Context, name string) (*core.Actor, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	for _, actor := range r.backend.actors {
		if actor.Name == name {
			found := actor
			return &found, nil
		}
	}
	return nil, fmt.Errorf("%w: actor named %q", core.ErrNotFound, name)
}

func (r *actorRepo) List(_ context.Context) ([]*core.Actor, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.ActorID, 0, len(r.backend.actors))
	for id := range r.backend.actors {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Actor, 0, len(ids))
	for _, id := range ids {
		actor := r.backend.actors[id]
		out = append(out, &actor)
	}
	return out, nil
}

func (r *actorRepo) Update(_ context.Context, actor *core.Actor) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actors[actor.ID]; !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, actor.ID)
	}
	r.backend.actors[actor.ID] = *actor
	return nil
}

func (r *actorRepo) Delete(_ context.Context, id core.ActorID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.actors[id]; !ok {
		return fmt.Errorf("%w: actor %s", core.ErrNotFound, id)
	}
	delete(r.backend.actors, id)
	return nil
}

type artifactRepo struct{ backend *Backend }

func (r *artifactRepo) Create(_ context.Context, artifact *core.Artifact) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.artifacts[artifact.ID]; ok {
		return fmt.Errorf("%w: artifact %s", core.ErrAlreadyExists, artifact.ID)
	}
	r.backend.artifacts[artifact.ID] = clone.Artifact(*artifact)
	return nil
}

func (r *artifactRepo) Get(_ context.Context, id core.ArtifactID) (*core.Artifact, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	artifact, ok := r.backend.artifacts[id]
	if !ok {
		return nil, fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	cloned := clone.Artifact(artifact)
	return &cloned, nil
}

func (r *artifactRepo) List(_ context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	ids := make([]core.ArtifactID, 0, len(r.backend.artifacts))
	for id, artifact := range r.backend.artifacts {
		if !store.MatchesArtifactFilter(&artifact, filter) {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]*core.Artifact, 0, len(ids))
	for _, id := range ids {
		cloned := clone.Artifact(r.backend.artifacts[id])
		out = append(out, &cloned)
	}
	return out, nil
}

func (r *artifactRepo) Search(_ context.Context, filter store.ArtifactFilter, query string) ([]store.SearchHit, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	terms := store.LexicalTerms(query)
	hits := make([]store.SearchHit, 0)
	for _, artifact := range r.backend.artifacts {
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
	if _, ok := r.backend.artifacts[artifact.ID]; !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, artifact.ID)
	}
	r.backend.artifacts[artifact.ID] = clone.Artifact(*artifact)
	return nil
}

func (r *artifactRepo) Delete(_ context.Context, id core.ArtifactID) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.artifacts[id]; !ok {
		return fmt.Errorf("%w: artifact %s", core.ErrNotFound, id)
	}
	delete(r.backend.artifacts, id)
	return nil
}

type eventRepo struct{ backend *Backend }

func (r *eventRepo) Append(_ context.Context, event *core.Event) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	r.backend.events = append(r.backend.events, clone.Event(*event))
	return nil
}

func (r *eventRepo) List(_ context.Context, filter store.EventFilter) ([]*core.Event, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	out := make([]*core.Event, 0)
	for i := len(r.backend.events) - 1; i >= 0; i-- {
		event := r.backend.events[i]
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
	if _, ok := r.backend.messages[message.ID]; ok {
		return fmt.Errorf("%w: message %s", core.ErrAlreadyExists, message.ID)
	}
	r.backend.messages[message.ID] = clone.Message(*message)
	return nil
}

func (r *messageRepo) Get(_ context.Context, id core.MessageID) (*core.Message, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	message, ok := r.backend.messages[id]
	if !ok {
		return nil, fmt.Errorf("%w: message %s", core.ErrNotFound, id)
	}
	cloned := clone.Message(message)
	return &cloned, nil
}

func (r *messageRepo) List(_ context.Context, filter store.MessageFilter) ([]*core.Message, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	out := make([]*core.Message, 0, len(r.backend.messages))
	for _, message := range r.backend.messages {
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
	if _, ok := r.backend.messages[message.ID]; !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, message.ID)
	}
	r.backend.messages[message.ID] = clone.Message(*message)
	return nil
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
	for id, message := range r.backend.messages {
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
	r.backend.messages[bestID] = best
	cloned := clone.Message(best)
	return &cloned, nil
}

func (r *messageRepo) Ack(_ context.Context, req store.AckRequest) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	message, ok := r.backend.messages[req.ID]
	if !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, req.ID)
	}
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
	r.backend.messages[req.ID] = message
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
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	message, ok := r.backend.messages[id]
	if !ok {
		return fmt.Errorf("%w: message %s", core.ErrNotFound, id)
	}
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
	r.backend.messages[id] = message
	return nil
}

func (r *messageRepo) RequeueExpired(_ context.Context, now time.Time, maxAttempts int) (int, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	moved := 0
	for id, message := range r.backend.messages {
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
		r.backend.messages[id] = message
		moved++
	}
	return moved, nil
}

func (r *messageRepo) Prune(_ context.Context, before time.Time, states []core.MessageState) (int, error) {
	if len(states) == 0 {
		return 0, nil
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	removed := 0
	for id, message := range r.backend.messages {
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
		delete(r.backend.messages, id)
		removed++
	}
	return removed, nil
}

type runRepo struct{ backend *Backend }

func (r *runRepo) Register(_ context.Context, run *core.Run) (*core.Run, error) {
	if err := run.Validate(); err != nil {
		return nil, err
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	for id, existing := range r.backend.runs {
		if existing.ProjectID == run.ProjectID && existing.ActorID == run.ActorID && existing.Host == run.Host && existing.PID == run.PID {
			existing.TaskID = run.TaskID
			existing.Harness = run.Harness
			existing.CanInject = run.CanInject
			existing.SeenAt = run.SeenAt
			existing.LeaseUntil = run.LeaseUntil
			r.backend.runs[id] = existing
			cloned := clone.Run(existing)
			return &cloned, nil
		}
	}
	r.backend.runs[run.ID] = clone.Run(*run)
	cloned := clone.Run(*run)
	return &cloned, nil
}

func (r *runRepo) Heartbeat(_ context.Context, id core.RunID, now time.Time, lease time.Duration) error {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	run, ok := r.backend.runs[id]
	if !ok {
		return fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	run.SeenAt = now
	run.LeaseUntil = now.Add(lease)
	r.backend.runs[id] = run
	return nil
}

func (r *runRepo) List(_ context.Context, filter store.RunFilter) ([]*core.Run, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	now := filter.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	out := make([]*core.Run, 0, len(r.backend.runs))
	for _, run := range r.backend.runs {
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
	if _, ok := r.backend.runs[id]; !ok {
		return fmt.Errorf("%w: run %s", core.ErrNotFound, id)
	}
	delete(r.backend.runs, id)
	return nil
}

type pipelineRepo struct{ backend *Backend }

func (r *pipelineRepo) Create(_ context.Context, pipeline *core.Pipeline) error {
	if err := pipeline.Validate(); err != nil {
		return err
	}
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	if _, ok := r.backend.pipelines[pipeline.ID]; ok {
		return fmt.Errorf("%w: pipeline %s", core.ErrAlreadyExists, pipeline.ID)
	}
	for _, existing := range r.backend.pipelines {
		if existing.ProjectID == pipeline.ProjectID && existing.CaptureID == pipeline.CaptureID {
			return fmt.Errorf("%w: pipeline for capture %s in project %s", core.ErrAlreadyExists, pipeline.CaptureID, pipeline.ProjectID)
		}
	}
	r.backend.pipelines[pipeline.ID] = clone.Pipeline(*pipeline)
	return nil
}

func (r *pipelineRepo) Get(_ context.Context, id core.PipelineID) (*core.Pipeline, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	pipeline, ok := r.backend.pipelines[id]
	if !ok {
		return nil, fmt.Errorf("%w: pipeline %s", core.ErrNotFound, id)
	}
	cloned := clone.Pipeline(pipeline)
	return &cloned, nil
}

func (r *pipelineRepo) List(_ context.Context, filter store.PipelineFilter) ([]*core.Pipeline, error) {
	r.backend.mu.RLock()
	defer r.backend.mu.RUnlock()
	out := make([]*core.Pipeline, 0, len(r.backend.pipelines))
	for _, pipeline := range r.backend.pipelines {
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
	current, ok := r.backend.pipelines[pipeline.ID]
	if !ok {
		return fmt.Errorf("%w: pipeline %s", core.ErrNotFound, pipeline.ID)
	}
	if current.State.Terminal() {
		return fmt.Errorf("%w: pipeline %s is %s and immutable", core.ErrConflict, pipeline.ID, current.State)
	}
	if current.ProjectID != pipeline.ProjectID || current.CaptureID != pipeline.CaptureID {
		return fmt.Errorf("%w: pipeline %s identity is immutable", core.ErrConflict, pipeline.ID)
	}
	r.backend.pipelines[pipeline.ID] = clone.Pipeline(*pipeline)
	return nil
}

func (r *pipelineRepo) Claim(_ context.Context, req store.PipelineClaimRequest) (*core.Pipeline, error) {
	r.backend.mu.Lock()
	defer r.backend.mu.Unlock()
	var (
		best   core.Pipeline
		bestID core.PipelineID
		found  bool
	)
	for id, pipeline := range r.backend.pipelines {
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
	r.backend.pipelines[bestID] = best
	cloned := clone.Pipeline(best)
	return &cloned, nil
}
