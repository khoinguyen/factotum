package app

import (
	"context"
	"fmt"
	"strings"

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

// Search returns project artifacts whose title or body contains the query,
// case-insensitively. An empty query returns everything.
func (s *ArtifactService) Search(ctx context.Context, projectID core.ProjectID, query string) ([]*core.Artifact, error) {
	artifacts, err := s.backend.Artifacts().List(ctx, store.ArtifactFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	if query == "" {
		return artifacts, nil
	}
	needle := strings.ToLower(query)
	out := make([]*core.Artifact, 0)
	for _, artifact := range artifacts {
		if strings.Contains(strings.ToLower(artifact.Title), needle) ||
			strings.Contains(strings.ToLower(artifact.Body), needle) {
			out = append(out, artifact)
		}
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
