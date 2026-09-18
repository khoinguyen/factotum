package core

import "fmt"

type ResolutionPolicy struct {
	TaskStatuses      []TaskStatus
	MilestoneStatuses []TaskStatus
}

func DefaultResolutionPolicy() ResolutionPolicy {
	return ResolutionPolicy{
		TaskStatuses:      []TaskStatus{StatusReadyForReview, StatusDone, StatusCancelled},
		MilestoneStatuses: []TaskStatus{StatusDone},
	}
}

func (p ResolutionPolicy) Resolves(kind TaskKind, status TaskStatus) bool {
	allowed := p.TaskStatuses
	if kind == KindMilestone {
		allowed = p.MilestoneStatuses
	}
	for _, s := range allowed {
		if s == status {
			return true
		}
	}
	return false
}

func (p ResolutionPolicy) Validate() error {
	if len(p.TaskStatuses) == 0 {
		return fmt.Errorf("%w: resolution policy needs at least one task status", ErrInvalid)
	}
	if len(p.MilestoneStatuses) == 0 {
		return fmt.Errorf("%w: resolution policy needs at least one milestone status", ErrInvalid)
	}
	for _, s := range p.TaskStatuses {
		if !s.Valid() {
			return fmt.Errorf("%w: unknown task status %q in resolution policy", ErrInvalid, s)
		}
	}
	for _, s := range p.MilestoneStatuses {
		if !s.Valid() {
			return fmt.Errorf("%w: unknown milestone status %q in resolution policy", ErrInvalid, s)
		}
	}
	return nil
}
