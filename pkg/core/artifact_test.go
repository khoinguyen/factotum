package core

import "testing"

func TestArtifactKindValid(t *testing.T) {
	tests := []struct {
		kind ArtifactKind
		want bool
	}{
		{ArtifactSpec, true},
		{ArtifactDoc, true},
		{ArtifactMemory, true},
		{"note", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := tt.kind.Valid(); got != tt.want {
			t.Errorf("ArtifactKind(%q).Valid() = %v, want %v", tt.kind, got, tt.want)
		}
	}
}

func TestArtifactValidate(t *testing.T) {
	taskID := TaskID("t-1")
	tests := []struct {
		name     string
		artifact Artifact
		wantErr  bool
	}{
		{"valid spec", Artifact{ID: "art-1", ProjectID: "prj-1", Kind: ArtifactSpec, Title: "Product spec"}, false},
		{
			"valid memory with task",
			Artifact{ID: "art-2", ProjectID: "prj-1", TaskID: &taskID, Kind: ArtifactMemory, Title: "recall", Body: "remember this"},
			false,
		},
		{"missing id", Artifact{ProjectID: "prj-1", Kind: ArtifactSpec, Title: "x"}, true},
		{"missing project", Artifact{ID: "art-1", Kind: ArtifactSpec, Title: "x"}, true},
		{"unknown kind", Artifact{ID: "art-1", ProjectID: "prj-1", Kind: "note", Title: "x"}, true},
		{"missing title", Artifact{ID: "art-1", ProjectID: "prj-1", Kind: ArtifactSpec}, true},
		{
			"invalid link",
			Artifact{ID: "art-1", ProjectID: "prj-1", Kind: ArtifactDoc, Title: "x", Links: []Link{{Kind: LinkDoc}}},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.artifact.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
