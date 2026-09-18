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
