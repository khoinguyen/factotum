package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/store"
)

type ProjectService struct {
	backend store.Backend
	clock   Clock
	ids     IDGen
}

func NewProjectService(backend store.Backend, clock Clock, ids IDGen) *ProjectService {
	return &ProjectService{backend: backend, clock: clock, ids: ids}
}

func (s *ProjectService) Create(ctx context.Context, name, description string, repos []core.Repository) (*core.Project, error) {
	id := core.ProjectID(Slug(name))
	if id == "" {
		id = core.ProjectID(s.ids.NewID("prj"))
	}
	return s.CreateWithID(ctx, id, name, description, repos)
}

// CreateWithID creates a project with an explicit id, unlike Create which slugs
// the name into one. It is for a caller that already knows the id (for example a
// project named in the machine config) and must not have it reshaped.
func (s *ProjectService) CreateWithID(ctx context.Context, id core.ProjectID, name, description string, repos []core.Repository) (*core.Project, error) {
	now := s.clock.Now()
	project := &core.Project{
		ID:          id,
		Name:        name,
		Description: description,
		Repos:       repos,
		Policy:      core.DefaultResolutionPolicy(),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := project.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Projects().Create(ctx, project); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		Kind:      core.EventProjectCreated,
		Summary:   fmt.Sprintf("created project %s", project.Name),
	}); err != nil {
		return nil, err
	}
	return project, nil
}

func (s *ProjectService) Get(ctx context.Context, id core.ProjectID) (*core.Project, error) {
	return s.backend.Projects().Get(ctx, id)
}

func (s *ProjectService) List(ctx context.Context) ([]*core.Project, error) {
	return s.backend.Projects().List(ctx)
}

type ProjectUpdate struct {
	Name        *string
	Description *string
	Repos       []core.Repository
}

func (s *ProjectService) Update(ctx context.Context, id core.ProjectID, patch ProjectUpdate) (*core.Project, error) {
	project, err := s.backend.Projects().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if patch.Name != nil {
		project.Name = *patch.Name
	}
	if patch.Description != nil {
		project.Description = *patch.Description
	}
	if patch.Repos != nil {
		project.Repos = patch.Repos
	}
	project.UpdatedAt = s.clock.Now()
	if err := project.Validate(); err != nil {
		return nil, err
	}
	if err := s.backend.Projects().Update(ctx, project); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		Kind:      core.EventProjectUpdated,
		Summary:   fmt.Sprintf("updated project %s", project.Name),
	}); err != nil {
		return nil, err
	}
	return project, nil
}

// Slug converts a name into an ID: lowercased, with runs of non-alphanumeric
// characters collapsed to single dashes and trimmed. Returns an empty string
// when the name has no alphanumeric characters.
func Slug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func (s *ProjectService) AddRepo(ctx context.Context, id core.ProjectID, repo core.Repository) (*core.Project, error) {
	if err := repo.Validate(); err != nil {
		return nil, err
	}
	project, err := s.backend.Projects().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, existing := range project.Repos {
		if existing.Name == repo.Name {
			return nil, fmt.Errorf("%w: repository %s", core.ErrAlreadyExists, repo.Name)
		}
	}
	project.Repos = append(project.Repos, repo)
	project.UpdatedAt = s.clock.Now()
	if err := s.backend.Projects().Update(ctx, project); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		Kind:      core.EventProjectRepoAdded,
		Summary:   fmt.Sprintf("added repository %s to %s", repo.Name, project.Name),
	}); err != nil {
		return nil, err
	}
	return project, nil
}

type RepositoryUpdate struct {
	URL         *string
	Path        *string
	Description *string
}

func (s *ProjectService) UpdateRepo(ctx context.Context, id core.ProjectID, name string, patch RepositoryUpdate) (*core.Project, error) {
	project, err := s.backend.Projects().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	index := -1
	for i, repo := range project.Repos {
		if repo.Name == name {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("%w: repository %s", core.ErrNotFound, name)
	}
	if patch.URL != nil {
		project.Repos[index].URL = *patch.URL
	}
	if patch.Path != nil {
		project.Repos[index].Path = *patch.Path
	}
	if patch.Description != nil {
		project.Repos[index].Description = *patch.Description
	}
	if err := project.Repos[index].Validate(); err != nil {
		return nil, err
	}
	project.UpdatedAt = s.clock.Now()
	if err := s.backend.Projects().Update(ctx, project); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		Kind:      core.EventProjectRepoUpdated,
		Summary:   fmt.Sprintf("updated repository %s in %s", name, project.Name),
	}); err != nil {
		return nil, err
	}
	return project, nil
}

func (s *ProjectService) RemoveRepo(ctx context.Context, id core.ProjectID, name string) (*core.Project, error) {
	project, err := s.backend.Projects().Get(ctx, id)
	if err != nil {
		return nil, err
	}
	index := -1
	for i, repo := range project.Repos {
		if repo.Name == name {
			index = i
			break
		}
	}
	if index < 0 {
		return nil, fmt.Errorf("%w: repository %s", core.ErrNotFound, name)
	}
	project.Repos = append(project.Repos[:index], project.Repos[index+1:]...)
	project.UpdatedAt = s.clock.Now()
	if err := s.backend.Projects().Update(ctx, project); err != nil {
		return nil, err
	}
	if err := appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: project.ID,
		Kind:      core.EventProjectRepoRemoved,
		Summary:   fmt.Sprintf("removed repository %s from %s", name, project.Name),
	}); err != nil {
		return nil, err
	}
	return project, nil
}

func (s *ProjectService) Delete(ctx context.Context, id core.ProjectID) error {
	project, err := s.backend.Projects().Get(ctx, id)
	if err != nil {
		return err
	}
	if err := s.backend.Projects().Delete(ctx, id); err != nil {
		return err
	}
	return appendEvent(ctx, s.backend, s.clock, s.ids, &core.Event{
		ProjectID: id,
		Kind:      core.EventProjectDeleted,
		Summary:   fmt.Sprintf("deleted project %s", project.Name),
	})
}
