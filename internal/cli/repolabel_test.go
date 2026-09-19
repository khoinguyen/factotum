package cli

import "testing"

func TestShortRepo(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{"https github", "https://github.com/khoinguyen/factotum", "github:khoinguyen/factotum"},
		{"https github .git", "https://github.com/khoinguyen/factotum.git", "github:khoinguyen/factotum"},
		{"http github", "http://github.com/khoinguyen/factotum", "github:khoinguyen/factotum"},
		{"scp github", "git@github.com:khoinguyen/factotum.git", "github:khoinguyen/factotum"},
		{"ssh github", "ssh://git@github.com/khoinguyen/factotum.git", "github:khoinguyen/factotum"},
		{"git protocol", "git://github.com/khoinguyen/factotum.git", "github:khoinguyen/factotum"},
		{"gitlab", "https://gitlab.com/org/repo.git", "gitlab:org/repo"},
		{"bitbucket", "https://bitbucket.org/org/repo.git", "bitbucket:org/repo"},
		{"other host url", "https://git.example.com/org/repo.git", "git.example.com:org/repo"},
		{"other host scp", "git@git.example.com:org/repo.git", "git.example.com:org/repo"},
		{"nested path", "https://github.com/org/team/repo.git", "github:org/team/repo"},
		{"already short github", "github:khoinguyen/factotum", "github:khoinguyen/factotum"},
		{"already short gitlab", "gitlab:org/repo", "gitlab:org/repo"},
		{"org/repo left as-is", "org/repo", "org/repo"},
		{"plain name", "backend", "backend"},
		{"absolute path", "/Users/me/repos/backend", "Local"},
		{"relative path", "./repos/backend", "Local"},
		{"home path", "~/repos/backend", "Local"},
		{"empty", "", "-"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shortRepo(tt.value); got != tt.want {
				t.Fatalf("shortRepo(%q) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestRepoURL(t *testing.T) {
	tests := []struct {
		value string
		want  string
	}{
		{"https://github.com/khoinguyen/factotum", "https://github.com/khoinguyen/factotum"},
		{"git@github.com:khoinguyen/factotum.git", "https://github.com/khoinguyen/factotum"},
		{"github:khoinguyen/factotum", "https://github.com/khoinguyen/factotum"},
		{"gitlab:org/repo", "https://gitlab.com/org/repo"},
		{"org/repo", ""},
		{"backend", ""},
		{"/Users/me/repos/backend", ""},
		{"", ""},
	}
	for _, tt := range tests {
		if got := repoURL(tt.value); got != tt.want {
			t.Errorf("repoURL(%q) = %q, want %q", tt.value, got, tt.want)
		}
	}
}

func TestRepoCell(t *testing.T) {
	tests := []struct {
		remote, path, want string
	}{
		{"https://github.com/o/r.git", "", "github:o/r"},
		{"", "repos/backend", "Local"},
		{"", "", "-"},
		{"https://github.com/o/r.git", "repos/backend", "github:o/r"},
	}
	for _, tt := range tests {
		if got := repoCell(tt.remote, tt.path); got != tt.want {
			t.Errorf("repoCell(%q, %q) = %q, want %q", tt.remote, tt.path, got, tt.want)
		}
	}
}
