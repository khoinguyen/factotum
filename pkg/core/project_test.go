package core

import "testing"

func TestProjectValidate(t *testing.T) {
	validRepo := Repository{Name: "backend", URL: "git@example.com:acme/backend.git", Path: "repos/backend", Description: "API service"}

	tests := []struct {
		name    string
		project Project
		wantErr bool
	}{
		{
			"valid with repo",
			Project{ID: "prj-1", Name: "Acme", Repos: []Repository{validRepo}, Policy: DefaultResolutionPolicy()},
			false,
		},
		{
			"valid without repo",
			Project{ID: "prj-1", Name: "Acme", Policy: DefaultResolutionPolicy()},
			false,
		},
		{"missing id", Project{Name: "Acme", Policy: DefaultResolutionPolicy()}, true},
		{"missing name", Project{ID: "prj-1", Policy: DefaultResolutionPolicy()}, true},
		{"missing policy", Project{ID: "prj-1", Name: "Acme"}, true},
		{
			"invalid repo",
			Project{ID: "prj-1", Name: "Acme", Repos: []Repository{{Name: ""}}, Policy: DefaultResolutionPolicy()},
			true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.project.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestRepositoryValidate(t *testing.T) {
	tests := []struct {
		name    string
		repo    Repository
		wantErr bool
	}{
		{"valid", Repository{Name: "backend", URL: "git@example.com:acme/backend.git"}, false},
		{"name only", Repository{Name: "backend"}, false},
		{"missing name", Repository{URL: "git@example.com:acme/backend.git"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.repo.Validate(); (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
