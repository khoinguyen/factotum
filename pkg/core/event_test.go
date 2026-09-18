package core

import "testing"

func TestEventKindValid(t *testing.T) {
	tests := []struct {
		kind EventKind
		want bool
	}{
		{EventProjectCreated, true},
		{EventTaskStatusChanged, true},
		{EventArtifactCreated, true},
		{"task.exploded", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.kind.Valid(); got != tt.want {
			t.Errorf("EventKind(%q).Valid() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestEventValidate(t *testing.T) {
	taskID := TaskID("t-1")
	actorID := ActorID("act-1")
	tests := []struct {
		name    string
		event   Event
		wantErr bool
	}{
		{
			"valid",
			Event{ID: "ev-1", ProjectID: "prj-1", Kind: EventTaskStatusChanged, Summary: "t-1 todo -> done"},
			false,
		},
		{
			"valid with task and actor",
			Event{ID: "ev-2", ProjectID: "prj-1", TaskID: &taskID, Kind: EventTaskAssigned, By: &actorID},
			false,
		},
		{"missing id", Event{ProjectID: "prj-1", Kind: EventTaskCreated}, true},
		{"global actor event", Event{ID: "ev-1", Kind: EventActorCreated}, false},
		{"unknown kind", Event{ID: "ev-1", ProjectID: "prj-1", Kind: "task.exploded"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.event.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
