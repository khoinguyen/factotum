package core

import (
	"fmt"
	"strings"
	"time"
)

type ArtifactKind string

const (
	ArtifactSpec   ArtifactKind = "spec"
	ArtifactDoc    ArtifactKind = "doc"
	ArtifactMemory ArtifactKind = "memory"
	// ArtifactTaskCheck is the derived cache for advisory task checks: one
	// artifact per check run, or a human decision override.
	ArtifactTaskCheck ArtifactKind = "task_check"
)

func (k ArtifactKind) Valid() bool {
	switch k {
	case ArtifactSpec, ArtifactDoc, ArtifactMemory, ArtifactTaskCheck:
		return true
	default:
		return false
	}
}

type Artifact struct {
	ID        ArtifactID
	ProjectID ProjectID
	TaskID    *TaskID
	Kind      ArtifactKind
	Title     string
	Brief     string
	Body      string
	Path      string
	Links     []Link
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (a Artifact) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("%w: artifact id is required", ErrInvalid)
	}
	if a.ProjectID == "" {
		return fmt.Errorf("%w: artifact project id is required", ErrInvalid)
	}
	if !a.Kind.Valid() {
		return fmt.Errorf("%w: unknown artifact kind %q", ErrInvalid, a.Kind)
	}
	if strings.TrimSpace(a.Title) == "" {
		return fmt.Errorf("%w: artifact title is required", ErrInvalid)
	}
	for _, l := range a.Links {
		if err := l.Validate(); err != nil {
			return fmt.Errorf("artifact link: %w", err)
		}
	}
	return nil
}
