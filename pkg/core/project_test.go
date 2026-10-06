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

func TestProjectReposForTask(t *testing.T) {
	backend := Repository{Name: "backend", URL: "git@example.com:acme/backend.git"}
	web := Repository{Name: "web", Path: "/srv/web"}
	project := Project{ID: "acme", Name: "Acme", Repos: []Repository{backend, web}, Policy: DefaultResolutionPolicy()}

	tests := []struct {
		name    string
		project Project
		task    Task
		want    []string
		wantErr bool
	}{
		{
			name:    "named repo resolves to exactly that repo",
			project: project,
			task:    Task{ID: "t-1", ProjectID: "acme", Repo: "web", Kind: KindTask, Status: StatusTodo, Title: "x"},
			want:    []string{"web"},
		},
		{
			name:    "no repo resolves to every project repo in order",
			project: project,
			task:    Task{ID: "t-1", ProjectID: "acme", Kind: KindTask, Status: StatusTodo, Title: "x"},
			want:    []string{"backend", "web"},
		},
		{
			name:    "unknown repo is an error",
			project: project,
			task:    Task{ID: "t-1", ProjectID: "acme", Repo: "data", Kind: KindTask, Status: StatusTodo, Title: "x"},
			wantErr: true,
		},
		{
			name:    "no repos and no named repo is empty",
			project: Project{ID: "acme", Name: "Acme", Policy: DefaultResolutionPolicy()},
			task:    Task{ID: "t-1", ProjectID: "acme", Kind: KindTask, Status: StatusTodo, Title: "x"},
			want:    nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.project.ReposForTask(tt.task)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ReposForTask() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			var names []string
			for _, repo := range got {
				names = append(names, repo.Name)
			}
			if len(names) != len(tt.want) {
				t.Fatalf("ReposForTask() = %v, want %v", names, tt.want)
			}
			for i := range names {
				if names[i] != tt.want[i] {
					t.Fatalf("ReposForTask() = %v, want %v", names, tt.want)
				}
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
