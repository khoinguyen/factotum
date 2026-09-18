package core

import "testing"

func TestDefaultResolutionPolicyTask(t *testing.T) {
	p := DefaultResolutionPolicy()
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{StatusTodo, false},
		{StatusInProgress, false},
		{StatusBlocked, false},
		{StatusReadyForReview, true},
		{StatusDone, true},
		{StatusCancelled, true},
	}
	for _, tt := range tests {
		if got := p.Resolves(KindTask, tt.status); got != tt.want {
			t.Errorf("Resolves(task, %q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestDefaultResolutionPolicyMilestone(t *testing.T) {
	p := DefaultResolutionPolicy()
	tests := []struct {
		status TaskStatus
		want   bool
	}{
		{StatusTodo, false},
		{StatusInProgress, false},
		{StatusBlocked, false},
		{StatusReadyForReview, false},
		{StatusDone, true},
		{StatusCancelled, false},
	}
	for _, tt := range tests {
		if got := p.Resolves(KindMilestone, tt.status); got != tt.want {
			t.Errorf("Resolves(milestone, %q) = %v, want %v", tt.status, got, tt.want)
		}
	}
}

func TestResolutionPolicyValidate(t *testing.T) {
	tests := []struct {
		name    string
		policy  ResolutionPolicy
		wantErr bool
	}{
		{"default", DefaultResolutionPolicy(), false},
		{"empty", ResolutionPolicy{}, true},
		{"task statuses only", ResolutionPolicy{TaskStatuses: []TaskStatus{StatusDone}}, true},
		{"milestone statuses only", ResolutionPolicy{MilestoneStatuses: []TaskStatus{StatusDone}}, true},
		{
			"unknown task status",
			ResolutionPolicy{TaskStatuses: []TaskStatus{"nope"}, MilestoneStatuses: []TaskStatus{StatusDone}},
			true,
		},
		{
			"unknown milestone status",
			ResolutionPolicy{TaskStatuses: []TaskStatus{StatusDone}, MilestoneStatuses: []TaskStatus{"nope"}},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.policy.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
