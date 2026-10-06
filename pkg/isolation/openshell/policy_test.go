package openshell_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
)

// writeOverride writes a project policy override under a temp project dir and
// returns its path.
func writeOverride(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	path := openshell.OverridePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write override: %v", err)
	}
	return path
}

func TestOverridePathIsUnderProjectFactotumDir(t *testing.T) {
	got := openshell.OverridePath("/work/acme/")
	want := filepath.Join("/work/acme", ".factotum", "openshell-policy.yaml")
	if got != want {
		t.Fatalf("OverridePath() = %q, want %q", got, want)
	}
}

// rawMap decodes a rendered policy into a generic map so tests can assert that
// a section or key is entirely absent.
func rawMap(t *testing.T, p openshell.Policy) map[string]any {
	t.Helper()
	raw, err := p.YAML()
	if err != nil {
		t.Fatalf("YAML() error = %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal rendered policy: %v", err)
	}
	return doc
}

func TestDefaultPolicyDeniesAllEgress(t *testing.T) {
	p, err := openshell.Build(openshell.Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(p.NetworkPolicies) != 0 {
		t.Fatalf("NetworkPolicies = %v, want none (deny-by-default)", p.NetworkPolicies)
	}
	if _, ok := rawMap(t, p)["network_policies"]; ok {
		t.Fatalf("rendered policy carries a network_policies section, want it omitted for deny-all")
	}

	pol, err := p.Isolation()
	if err != nil {
		t.Fatalf("Isolation() error = %v", err)
	}
	if !pol.DefaultDeny {
		t.Errorf("DefaultDeny = false, want true")
	}
	if len(pol.AllowHosts) != 0 {
		t.Errorf("AllowHosts = %v, want none", pol.AllowHosts)
	}
}

func TestDefaultPolicyLocksFilesystemAndNonRoot(t *testing.T) {
	p, err := openshell.Build(openshell.Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if !p.FilesystemPolicy.IncludeWorkdir {
		t.Errorf("IncludeWorkdir = false, want true")
	}
	for _, want := range []string{"/usr", "/lib", "/bin", "/etc", "/proc"} {
		if !contains(p.FilesystemPolicy.ReadOnly, want) {
			t.Errorf("ReadOnly %v missing %q", p.FilesystemPolicy.ReadOnly, want)
		}
	}
	for _, want := range []string{"/tmp", "/dev/null"} {
		if !contains(p.FilesystemPolicy.ReadWrite, want) {
			t.Errorf("ReadWrite %v missing %q", p.FilesystemPolicy.ReadWrite, want)
		}
	}

	if p.Process.RunAsUser != openshell.DefaultRunAsUser {
		t.Errorf("RunAsUser = %q, want %q", p.Process.RunAsUser, openshell.DefaultRunAsUser)
	}
	if p.Process.RunAsGroup != openshell.DefaultRunAsGroup {
		t.Errorf("RunAsGroup = %q, want %q", p.Process.RunAsGroup, openshell.DefaultRunAsGroup)
	}
}

func TestDefaultPolicyDeniesOpenCodePhoneHome(t *testing.T) {
	p, err := openshell.Build(openshell.Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	raw, err := p.YAML()
	if err != nil {
		t.Fatalf("YAML() error = %v", err)
	}
	for _, host := range []string{openshell.OpenCodeModelsHost, openshell.OpenCodeRegistryHost} {
		if strings.Contains(string(raw), host) {
			t.Errorf("default policy allows phone-home host %q, want it denied", host)
		}
	}
}

func TestProjectOverrideAllowsExplicitHosts(t *testing.T) {
	override := writeOverride(t, `
allow_hosts:
  - models.opencode.ai
  - registry.npmjs.org
`)
	p, err := openshell.Build(openshell.Options{OverridePath: override})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	if len(p.NetworkPolicies) != 2 {
		t.Fatalf("NetworkPolicies = %d rules, want 2", len(p.NetworkPolicies))
	}
	got := map[string]bool{}
	for _, rule := range p.NetworkPolicies {
		if len(rule.Endpoints) != 1 {
			t.Fatalf("rule %q endpoints = %v, want exactly one", rule.Name, rule.Endpoints)
		}
		ep := rule.Endpoints[0]
		got[ep.Host] = true
		if ep.Port != 443 {
			t.Errorf("rule %q port = %d, want 443", rule.Name, ep.Port)
		}
		if ep.Enforcement != "enforce" {
			t.Errorf("rule %q enforcement = %q, want enforce", rule.Name, ep.Enforcement)
		}
		if len(rule.Binaries) != 1 || rule.Binaries[0].Path != openshell.DefaultHarnessBinary {
			t.Errorf("rule %q binaries = %v, want scoped to %q", rule.Name, rule.Binaries, openshell.DefaultHarnessBinary)
		}
	}
	for _, host := range []string{openshell.OpenCodeModelsHost, openshell.OpenCodeRegistryHost} {
		if !got[host] {
			t.Errorf("override did not allow %q", host)
		}
	}

	pol, err := p.Isolation()
	if err != nil {
		t.Fatalf("Isolation() error = %v", err)
	}
	want := []string{openshell.OpenCodeModelsHost, openshell.OpenCodeRegistryHost}
	sort.Strings(want)
	if !reflect.DeepEqual(pol.AllowHosts, want) {
		t.Errorf("AllowHosts = %v, want %v", pol.AllowHosts, want)
	}
}

func TestAllowHostsMergeDedupeAndSort(t *testing.T) {
	override := writeOverride(t, `
allow_hosts:
  - git.example.com
  - models.opencode.ai
`)
	p, err := openshell.Build(openshell.Options{
		OverridePath: override,
		AllowHosts:   []string{"models.opencode.ai", "api.example.com"},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	pol, err := p.Isolation()
	if err != nil {
		t.Fatalf("Isolation() error = %v", err)
	}
	want := []string{"api.example.com", "git.example.com", "models.opencode.ai"}
	if !reflect.DeepEqual(pol.AllowHosts, want) {
		t.Errorf("AllowHosts = %v, want %v", pol.AllowHosts, want)
	}
}

func TestBuildNeverRunsAsRoot(t *testing.T) {
	tests := []struct {
		name string
		opts openshell.Options
	}{
		{"uid zero", openshell.Options{RunAsUser: "0"}},
		{"root name", openshell.Options{RunAsUser: "root"}},
		{"empty user falls back but cannot be blanked", openshell.Options{RunAsUser: "  "}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, err := openshell.Build(tc.opts)
			if err != nil {
				return // rejecting root outright is also acceptable
			}
			if isRoot(p.Process.RunAsUser) {
				t.Fatalf("RunAsUser = %q, want non-root", p.Process.RunAsUser)
			}
		})
	}
}

func TestNonRootOverrideHonored(t *testing.T) {
	p, err := openshell.Build(openshell.Options{RunAsUser: "501", RunAsGroup: "20"})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if p.Process.RunAsUser != "501" || p.Process.RunAsGroup != "20" {
		t.Fatalf("Process = %+v, want 501/20", p.Process)
	}
}

func TestOverrideRejectsWeakening(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{"root identity", "process:\n  run_as_user: \"0\"\n"},
		{"filesystem change", "filesystem_policy:\n  read_write:\n    - /\n"},
		{"unknown key", "allow_everything: true\n"},
		{"network rules", "network_policies:\n  allow_all: {}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := writeOverride(t, tc.body)
			if _, err := openshell.Build(openshell.Options{OverridePath: path}); err == nil {
				t.Fatalf("Build() accepted a weakening override %q, want an error", tc.body)
			}
		})
	}
}

func TestMissingOverrideIsNoOverride(t *testing.T) {
	p, err := openshell.Build(openshell.Options{OverridePath: filepath.Join(t.TempDir(), "absent.yaml")})
	if err != nil {
		t.Fatalf("Build() with a missing override error = %v, want nil", err)
	}
	if len(p.NetworkPolicies) != 0 {
		t.Fatalf("NetworkPolicies = %v, want deny-all", p.NetworkPolicies)
	}
}

func TestInvalidHostRejected(t *testing.T) {
	for _, host := range []string{"http://evil.example.com", "evil.example.com/path", "user@evil.example.com", "evil.example.com:8443", "  "} {
		path := writeOverride(t, "allow_hosts:\n  - \""+host+"\"\n")
		if _, err := openshell.Build(openshell.Options{OverridePath: path}); err == nil {
			t.Errorf("Build() accepted invalid host %q, want an error", host)
		}
	}
}

func TestPolicyCarriesNoSecrets(t *testing.T) {
	override := writeOverride(t, "allow_hosts:\n  - models.opencode.ai\n")
	p, err := openshell.Build(openshell.Options{OverridePath: override})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	raw, err := p.YAML()
	if err != nil {
		t.Fatalf("YAML() error = %v", err)
	}

	doc := rawMap(t, p)
	forbidKeys(t, doc, map[string]bool{
		"env": true, "envs": true, "environment": true, "credentials": true,
		"secret": true, "secrets": true, "api_key": true, "apikey": true,
		"token": true, "password": true,
	})

	lower := strings.ToLower(string(raw))
	for _, needle := range []string{"api_key", "apikey", "password", "secret", "token"} {
		if strings.Contains(lower, needle) {
			t.Errorf("rendered policy contains %q; secrets must be provider-injected, never in the policy", needle)
		}
	}

	pol, err := p.Isolation()
	if err != nil {
		t.Fatalf("Isolation() error = %v", err)
	}
	if strings.Contains(string(pol.Raw), "resolve:env") {
		t.Errorf("Isolation().Raw contains a credential placeholder; credentials are provider references, not policy")
	}
}

func TestApprovalStaysManual(t *testing.T) {
	got := openshell.ApprovalArgs()
	want := []string{"--approval-mode", "manual"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ApprovalArgs() = %v, want %v", got, want)
	}
	for _, arg := range got {
		if arg == "auto" {
			t.Fatalf("ApprovalArgs() enables auto-approval: %v", got)
		}
	}
}

func TestBuildIsDeterministic(t *testing.T) {
	override := writeOverride(t, "allow_hosts:\n  - b.example.com\n  - a.example.com\n")
	firstPolicy, err := openshell.Build(openshell.Options{OverridePath: override})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	first, err := firstPolicy.YAML()
	if err != nil {
		t.Fatalf("YAML() error = %v", err)
	}
	secondPolicy, err := openshell.Build(openshell.Options{OverridePath: override})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	second, err := secondPolicy.YAML()
	if err != nil {
		t.Fatalf("YAML() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("rendered policy is not deterministic:\n%s\n---\n%s", first, second)
	}
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func isRoot(user string) bool {
	switch strings.TrimSpace(user) {
	case "", "0", "root":
		return true
	}
	return false
}

// forbidKeys fails if any forbidden key appears anywhere in a decoded document.
func forbidKeys(t *testing.T, doc map[string]any, forbidden map[string]bool) {
	t.Helper()
	for key, value := range doc {
		if forbidden[strings.ToLower(key)] {
			t.Errorf("rendered policy contains forbidden key %q", key)
		}
		switch child := value.(type) {
		case map[string]any:
			forbidKeys(t, child, forbidden)
		case []any:
			for _, item := range child {
				if nested, ok := item.(map[string]any); ok {
					forbidKeys(t, nested, forbidden)
				}
			}
		}
	}
}
