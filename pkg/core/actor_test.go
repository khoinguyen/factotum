package core

import "testing"

func TestActorKindValid(t *testing.T) {
	tests := []struct {
		kind ActorKind
		want bool
	}{
		{ActorHuman, true},
		{ActorAgent, true},
		{"robot", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.kind.Valid(); got != tt.want {
			t.Errorf("ActorKind(%q).Valid() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestActorValidate(t *testing.T) {
	tests := []struct {
		name    string
		actor   Actor
		wantErr bool
	}{
		{"valid human", Actor{ID: "act-1", Kind: ActorHuman, Name: "Khoi"}, false},
		{"valid agent", Actor{ID: "act-2", Kind: ActorAgent, Name: "claude"}, false},
		{"missing id", Actor{Kind: ActorHuman, Name: "Khoi"}, true},
		{"missing name", Actor{ID: "act-1", Kind: ActorHuman}, true},
		{"blank name", Actor{ID: "act-1", Kind: ActorHuman, Name: "   "}, true},
		{"unknown kind", Actor{ID: "act-1", Kind: "robot", Name: "Khoi"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.actor.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
