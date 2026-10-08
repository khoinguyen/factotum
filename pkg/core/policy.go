package core

import "fmt"

type ResolutionPolicy struct {
	TaskStatuses      []TicketStatus
	MilestoneStatuses []TicketStatus
}

func DefaultResolutionPolicy() ResolutionPolicy {
	return ResolutionPolicy{
		TaskStatuses:      []TicketStatus{StatusReadyForReview, StatusDone, StatusCancelled},
		MilestoneStatuses: []TicketStatus{StatusDone},
	}
}

func (p ResolutionPolicy) Resolves(kind TicketKind, status TicketStatus) bool {
	// Non-executable kinds (ideas) are outside the execution graph: they never
	// become ready, never block a dependent, and carry no resolution statuses.
	if !kind.Executable() {
		return true
	}
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
