package core

import (
	"fmt"
	"strings"
	"time"
)

type Repository struct {
	Name        string
	URL         string
	Path        string
	Description string
}

func (r Repository) Validate() error {
	if strings.TrimSpace(r.Name) == "" {
		return fmt.Errorf("%w: repository name is required", ErrInvalid)
	}
	return nil
}

type Project struct {
	ID          ProjectID
	Name        string
	Description string
	Repos       []Repository
	Policy      ResolutionPolicy
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ReposForTask returns the repositories a task touches. A task names the repo
// it works in with Ticket.Repo, resolved against the project's repo definitions;
// naming a repo the project does not define is an error. A task that names no
// repo spans the project and touches every one of its repos, in definition
// order.
func (p Project) ReposForTask(t Ticket) ([]Repository, error) {
	if t.Repo == "" {
		return append([]Repository(nil), p.Repos...), nil
	}
	for _, repo := range p.Repos {
		if repo.Name == t.Repo {
			return []Repository{repo}, nil
		}
	}
	return nil, fmt.Errorf("%w: repository %q is not part of project %s", ErrInvalid, t.Repo, p.ID)
}

func (p Project) Validate() error {
	if p.ID == "" {
		return fmt.Errorf("%w: project id is required", ErrInvalid)
	}
	if strings.TrimSpace(p.Name) == "" {
		return fmt.Errorf("%w: project name is required", ErrInvalid)
	}
	for _, r := range p.Repos {
		if err := r.Validate(); err != nil {
			return fmt.Errorf("repository: %w", err)
		}
	}
	if err := p.Policy.Validate(); err != nil {
		return fmt.Errorf("policy: %w", err)
	}
	return nil
}
