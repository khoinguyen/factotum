package openshell

import (
	"strings"
	"testing"
)

func TestEnvSliceOverrideWins(t *testing.T) {
	t.Setenv("FT_ENV_DUP", "host")
	got := envSlice(map[string]string{"FT_ENV_DUP": "override"})
	count := 0
	for _, kv := range got {
		if strings.HasPrefix(kv, "FT_ENV_DUP=") {
			count++
			if kv != "FT_ENV_DUP=override" {
				t.Fatalf("override lost: %q", kv)
			}
		}
	}
	if count != 1 {
		t.Fatalf("FT_ENV_DUP appears %d times, want exactly once", count)
	}
}

func TestWithSlash(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"stage", "stage/"},
		{"stage/", "stage/"},
		{"/sandbox", "/sandbox/"},
	} {
		if got := withSlash(tc.in); got != tc.want {
			t.Errorf("withSlash(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDisplayPath(t *testing.T) {
	requests := []pathRequest{{display: "out", guest: "/sandbox/out"}}
	tests := []struct{ guest, want string }{
		{"/sandbox/out", "out"},
		{"/sandbox/out/x.txt", "out/x.txt"},
		{"/sandbox/out/sub/y.txt", "out/sub/y.txt"},
		{"/other", "/other"},
	}
	for _, tc := range tests {
		if got := displayPath(requests, tc.guest); got != tc.want {
			t.Errorf("displayPath(%q) = %q, want %q", tc.guest, got, tc.want)
		}
	}
}

func TestProfileCatalogURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"openrouter", "https://raw.githubusercontent.com/NVIDIA/OpenShell/6648bd0c290efbc41ba131ee9831ee45cd431f94/providers/openrouter.yaml"},
		{"openai", "https://raw.githubusercontent.com/NVIDIA/OpenShell/6648bd0c290efbc41ba131ee9831ee45cd431f94/providers/openai.yaml"},
		{"", ""},
		{".", ""},
		{"..", ""},
		{"../evil", ""},
		{"a/b", ""},
		{"OpenRouter", ""},
	}
	for _, tc := range tests {
		if got := profileCatalogURL(tc.in); got != tc.want {
			t.Errorf("profileCatalogURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestProfileCatalogRefIsImmutable pins the watch item from PR #184: the
// provider-profile catalog must be sourced from an immutable commit, never a
// mutable branch. A moved ref would let upstream silently change a profile's
// binaries scope and therefore the sandbox's egress policy.
func TestProfileCatalogRefIsImmutable(t *testing.T) {
	if len(DefaultProfileCatalogRef) != 40 {
		t.Fatalf("DefaultProfileCatalogRef = %q, want a 40-char commit SHA", DefaultProfileCatalogRef)
	}
	for _, r := range DefaultProfileCatalogRef {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f':
		default:
			t.Fatalf("DefaultProfileCatalogRef = %q, want lowercase hex (a commit SHA)", DefaultProfileCatalogRef)
		}
	}
	for _, mutable := range []string{"/main/", "/master/", "/HEAD/"} {
		if strings.Contains(DefaultProfileCatalog, mutable) {
			t.Errorf("DefaultProfileCatalog = %q fetches from the mutable ref %q", DefaultProfileCatalog, mutable)
		}
	}
}

func TestResolvePathRootsAtWorkdir(t *testing.T) {
	env := &environment{workdir: "/sandbox/work"}
	if got := env.resolvePath("a.txt"); got != "/sandbox/work/a.txt" {
		t.Errorf("resolvePath(relative) = %q", got)
	}
	if got := env.resolvePath("/etc/hosts"); got != "/etc/hosts" {
		t.Errorf("resolvePath(absolute) = %q", got)
	}
}

func TestResolveWorkdirRemapsHostPath(t *testing.T) {
	env := &environment{workdir: "/sandbox/checkout", hostWorkdir: "/home/u/checkout"}
	if got := env.resolveWorkdir(""); got != "/sandbox/checkout" {
		t.Errorf("resolveWorkdir(empty) = %q", got)
	}
	if got := env.resolveWorkdir("/home/u/checkout"); got != "/sandbox/checkout" {
		t.Errorf("resolveWorkdir(host) = %q", got)
	}
	if got := env.resolveWorkdir("/sandbox/other"); got != "/sandbox/other" {
		t.Errorf("resolveWorkdir(guest) = %q", got)
	}
}
