package app

import (
	"context"
	"fmt"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

type ArtifactService struct {
	backend store.Backend
	clock   Clock
	ids     IDGen
}

func NewArtifactService(backend store.Backend, clock Clock, ids IDGen) *ArtifactService {
	return &ArtifactService{backend: backend, clock: clock, ids: ids}
}

type ArtifactInput struct {
	ProjectID core.ProjectID
	TaskID    *core.TaskID
	Kind      core.ArtifactKind
	Title     string
	Brief     string
	Body      string
	Path      string
	Links     []core.Link
}

func (s *ArtifactService) Add(ctx context.Context, in ArtifactInput) (*core.Artifact, error) {
	project, err := s.backend.Projects().Get(ctx, in.ProjectID)
	if err != nil {
		return nil, fmt.Errorf("artifact project: %w", err)
	}
	if in.TaskID != nil {
		if _, err := s.backend.Tasks().Get(ctx, *in.TaskID); err != nil {
			return nil, fmt.Errorf("artifact task: %w", err)
		}
	}
	now := s.clock.Now()
	artifact := &core.Artifact{
		ID:        core.ArtifactID(s.ids.NewID("art")),
		ProjectID: in.ProjectID,
		TaskID:    in.TaskID,
		Kind:      in.Kind,
		Title:     in.Title,
		Brief:     in.Brief,
		Body:      in.Body,
		Path:      in.Path,
		Links:     in.Links,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := artifact.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Artifacts().Create(ctx, artifact); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		TaskID:    artifact.TaskID,
		Kind:      core.EventArtifactCreated,
		Summary:   fmt.Sprintf("added %s artifact %q", artifact.Kind, artifact.Title),
	}); err != nil {
		return nil, err
	}
	return artifact, nil
}

func (s *ArtifactService) Get(ctx context.Context, id core.ArtifactID) (*core.Artifact, error) {
	return s.backend.Artifacts().Get(ctx, id)
}

func (s *ArtifactService) List(ctx context.Context, filter store.ArtifactFilter) ([]*core.Artifact, error) {
	return s.backend.Artifacts().List(ctx, filter)
}

// ArtifactPatch is a partial update to an artifact. A nil field is left
// unchanged; ClearTask detaches the artifact from its task.
type ArtifactPatch struct {
	Title     *string
	Brief     *string
	Body      *string
	TaskID    *core.TaskID
	ClearTask bool
}

func (s *ArtifactService) Update(ctx context.Context, id core.ArtifactID, patch ArtifactPatch) (*core.Artifact, error) {
	artifact, err := s.backend.Artifacts().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if patch.Title != nil {
		artifact.Title = *patch.Title
	}
	if patch.Brief != nil {
		artifact.Brief = *patch.Brief
	}
	if patch.Body != nil {
		artifact.Body = *patch.Body
	}
	if patch.ClearTask {
		artifact.TaskID = nil
	} else if patch.TaskID != nil {
		if _, err := s.backend.Tasks().Get(ctx, *patch.TaskID); err != nil {
			return nil, fmt.Errorf("artifact task: %w", err)
		}
		taskID := *patch.TaskID
		artifact.TaskID = &taskID
	}
	artifact.UpdatedAt = s.clock.Now()
	if err := artifact.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Artifacts().Update(ctx, artifact); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: artifact.ProjectID,
		TaskID:    artifact.TaskID,
		Kind:      core.EventArtifactUpdated,
		Summary:   fmt.Sprintf("updated %s artifact %q", artifact.Kind, artifact.Title),
	}); err != nil {
		return nil, err
	}
	return artifact, nil
}

// Search returns the artifacts in filter scope whose title or body matches the
// query, ranked by relevance by the storage backend. An empty query returns
// everything in scope. The order is deterministic.
func (s *ArtifactService) Search(ctx context.Context, filter store.ArtifactFilter, query string) ([]*core.Artifact, error) {
	hits, err := s.backend.Artifacts().Search(ctx, filter, query)
	if err != nil {
		return nil, err
	}
	out := make([]*core.Artifact, 0, len(hits))
	for _, hit := range hits {
		out = append(out, hit.Artifact)
	}
	return out, nil
}

func (s *ArtifactService) Delete(ctx context.Context, id core.ArtifactID) error {
	artifact, err := s.backend.Artifacts().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.backend.Artifacts().Delete(ctx, id); err != nil {
		return err
	}
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: artifact.ProjectID,
		TaskID:    artifact.TaskID,
		Kind:      core.EventArtifactDeleted,
		Summary:   fmt.Sprintf("deleted artifact %q", artifact.Title),
	})
}
