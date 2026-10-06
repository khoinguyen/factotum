package cli

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/khoinguyen/factotum/internal/config"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
)

// TestRunBackendsIncludeOpenShell pins that the OpenShell isolation backend is
// a registered, selectable `ft run` backend alongside the dev-only local host
// backend, and that its factory constructs without error.
func TestRunBackendsIncludeOpenShell(t *testing.T) {
	reg := runBackends(func(string) string { return "" })
	names := map[string]bool{}
	for _, name := range reg.Names() {
		names[name] = true
	}
	for _, want := range []string{"local", "openshell"} {
		if !names[want] {
			t.Fatalf("run backends %v missing %q", reg.Names(), want)
		}
	}
	factory, err := reg.MustLookup("openshell")
	if err != nil {
		t.Fatalf("lookup openshell: %v", err)
	}
	backend, err := factory(config.Run{}, io.Discard)
	if err != nil {
		t.Fatalf("build openshell backend: %v", err)
	}
	if backend.Name() != "openshell" {
		t.Fatalf("backend.Name() = %q, want openshell", backend.Name())
	}
}

// TestOpenShellBackendOptionsHonorProjectPolicyAndCredentials pins the CLI
// wiring: the factory translates the run config into OpenShell options that
// carry the project policy override (its allow_hosts) and a host-environment
// credential resolver, instead of the empty Options that made both unreachable.
func TestOpenShellBackendOptionsHonorProjectPolicyAndCredentials(t *testing.T) {
	projectDir := t.TempDir()
	factotumDir := filepath.Join(projectDir, ".factotum")
	if err := os.MkdirAll(factotumDir, 0o755); err != nil {
		t.Fatalf("mkdir .factotum: %v", err)
	}
	policyPath := filepath.Join(factotumDir, "openshell-policy.yaml")
	if err := os.WriteFile(policyPath, []byte("allow_hosts:\n  - models.opencode.ai\n"), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}

	getenv := func(key string) string {
		if key == "OPENROUTER_API_KEY" {
			return "sk-host-secret"
		}
		return ""
	}
	opts := openshellBackendOptions(config.Run{PolicyPath: policyPath}, getenv)
	if opts.OverridePath != policyPath {
		t.Fatalf("OverridePath = %q, want %q", opts.OverridePath, policyPath)
	}
	if opts.Credentials == nil {
		t.Fatal("Credentials resolver is nil; the credential path is unreachable")
	}

	policy, err := openshell.Build(opts)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	projected, err := policy.Isolation()
	if err != nil {
		t.Fatalf("Isolation() error = %v", err)
	}
	if !projected.DefaultDeny {
		t.Fatal("policy is not deny-by-default")
	}
	if !slices.Contains(projected.AllowHosts, "models.opencode.ai") {
		t.Fatalf("policy allow_hosts = %v, want the project override host", projected.AllowHosts)
	}
	if !strings.Contains(string(projected.Raw), "models.opencode.ai") || !strings.Contains(string(projected.Raw), openshell.DefaultHarnessBinary) {
		t.Fatalf("rendered policy is missing the host or its harness-binary scope:\n%s", projected.Raw)
	}

	ctx := context.Background()
	secret, err := opts.Credentials.Resolve(ctx, isolation.Credential{Provider: "openrouter", EnvVar: "OPENROUTER_API_KEY"})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if secret != "sk-host-secret" {
		t.Fatalf("Resolve() = %q, want the host environment value", secret)
	}
	if _, err := opts.Credentials.Resolve(ctx, isolation.Credential{Provider: "openrouter", EnvVar: "MISSING_KEY"}); err == nil {
		t.Fatal("Resolve() of an unset credential = nil error, want an error")
	}
}

// TestRunHarnessOpenCodeDeclaresConfiguredCredential pins that the OpenCode
// harness factory forwards the configured provider and credential variable, so
// the backend receives a credential to attach.
func TestRunHarnessOpenCodeDeclaresConfiguredCredential(t *testing.T) {
	factory, err := runHarnesses().MustLookup("opencode")
	if err != nil {
		t.Fatalf("lookup opencode: %v", err)
	}
	h, err := factory(config.Run{Model: "openrouter/x", Provider: "openrouter", CredentialEnvVar: "OPENROUTER_API_KEY"})
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	spec, err := h.Spec(harness.Request{Workdir: "/w"})
	if err != nil {
		t.Fatalf("Spec() error = %v", err)
	}
	if len(spec.Credentials) != 1 {
		t.Fatalf("Spec.Credentials = %v, want one credential", spec.Credentials)
	}
	if got := spec.Credentials[0]; got.Provider != "openrouter" || got.EnvVar != "OPENROUTER_API_KEY" {
		t.Fatalf("credential = %+v, want openrouter/OPENROUTER_API_KEY", got)
	}

	bare, err := factory(config.Run{Model: "openrouter/x"})
	if err != nil {
		t.Fatalf("build bare harness: %v", err)
	}
	bareSpec, err := bare.Spec(harness.Request{})
	if err != nil {
		t.Fatalf("bare Spec() error = %v", err)
	}
	if len(bareSpec.Credentials) != 0 {
		t.Fatalf("bare Spec.Credentials = %v, want none without a configured provider", bareSpec.Credentials)
	}
}

// TestPrepareRunResolvesProjectPolicyPath pins that a run resolves the committed
// project policy override from the loaded project config path, so the OpenShell
// factory gets a real path rather than nothing.
func TestPrepareRunResolvesProjectPolicyPath(t *testing.T) {
	projectDir := t.TempDir()
	factotumDir := filepath.Join(projectDir, ".factotum")
	if err := os.MkdirAll(factotumDir, 0o755); err != nil {
		t.Fatalf("mkdir .factotum: %v", err)
	}

	var got config.Run
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, func(string) string { return "" })
	deps.ProjectConfigPath = filepath.Join(factotumDir, "config.toml")
	if err := deps.RunBackends.Register("capture", func(cfg config.Run, _ io.Writer) (isolation.IsolationBackend, error) {
		got = cfg
		return isofake.New("capture"), nil
	}); err != nil {
		t.Fatalf("register backend: %v", err)
	}
	if err := deps.RunHarnesses.Register("fake", func(config.Run) (harness.Harness, error) {
		return harnessfake.New("fake"), nil
	}); err != nil {
		t.Fatalf("register harness: %v", err)
	}

	if _, err := deps.prepareRun(&cobra.Command{Use: "run"}, runOptions{
		backend:   "capture",
		harness:   "fake",
		workspace: t.TempDir(),
	}); err != nil {
		t.Fatalf("prepareRun() error = %v", err)
	}
	want := filepath.Join(factotumDir, "openshell-policy.yaml")
	if got.PolicyPath != want {
		t.Fatalf("resolved PolicyPath = %q, want %q", got.PolicyPath, want)
	}
}

// TestPrepareRunResolvesPolicyFromCustomConfigLocation pins that a custom
// -c/--config resolves the policy next to that config file instead of assuming
// <root>/.factotum/config.toml and silently denying all.
func TestPrepareRunResolvesPolicyFromCustomConfigLocation(t *testing.T) {
	configDir := t.TempDir()
	var got config.Run
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, func(string) string { return "" })
	deps.ProjectConfigPath = filepath.Join(configDir, "ft.toml")
	if err := deps.RunBackends.Register("capture", func(cfg config.Run, _ io.Writer) (isolation.IsolationBackend, error) {
		got = cfg
		return isofake.New("capture"), nil
	}); err != nil {
		t.Fatalf("register backend: %v", err)
	}
	if err := deps.RunHarnesses.Register("fake", func(config.Run) (harness.Harness, error) {
		return harnessfake.New("fake"), nil
	}); err != nil {
		t.Fatalf("register harness: %v", err)
	}

	if _, err := deps.prepareRun(&cobra.Command{Use: "run"}, runOptions{
		backend:   "capture",
		harness:   "fake",
		workspace: t.TempDir(),
	}); err != nil {
		t.Fatalf("prepareRun() error = %v", err)
	}
	want := filepath.Join(configDir, "openshell-policy.yaml")
	if got.PolicyPath != want {
		t.Fatalf("resolved PolicyPath = %q, want %q", got.PolicyPath, want)
	}
}

// TestOpenShellFactoryWarnsWhenProjectPolicyMissing pins that selecting the
// OpenShell backend without a project policy surfaces a clear deny-all message
// instead of silently denying all egress.
func TestOpenShellFactoryWarnsWhenProjectPolicyMissing(t *testing.T) {
	factory, err := runBackends(func(string) string { return "" }).MustLookup("openshell")
	if err != nil {
		t.Fatalf("lookup openshell: %v", err)
	}
	policyPath := filepath.Join(t.TempDir(), "openshell-policy.yaml")
	var errBuf bytes.Buffer
	if _, err := factory(config.Run{PolicyPath: policyPath}, &errBuf); err != nil {
		t.Fatalf("build openshell backend: %v", err)
	}
	got := errBuf.String()
	if !strings.Contains(got, policyPath) {
		t.Fatalf("warning = %q, want it to name %q", got, policyPath)
	}
	if !strings.Contains(strings.ToLower(got), "deny-all") {
		t.Fatalf("warning = %q, want it to say egress is deny-all", got)
	}
}

// TestOpenShellFactorySilentWhenProjectPolicyExists pins that a present policy
// override produces no warning.
func TestOpenShellFactorySilentWhenProjectPolicyExists(t *testing.T) {
	policyPath := filepath.Join(t.TempDir(), "openshell-policy.yaml")
	if err := os.WriteFile(policyPath, []byte("allow_hosts:\n  - models.opencode.ai\n"), 0o644); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	factory, err := runBackends(func(string) string { return "" }).MustLookup("openshell")
	if err != nil {
		t.Fatalf("lookup openshell: %v", err)
	}
	var errBuf bytes.Buffer
	if _, err := factory(config.Run{PolicyPath: policyPath}, &errBuf); err != nil {
		t.Fatalf("build openshell backend: %v", err)
	}
	if errBuf.Len() != 0 {
		t.Fatalf("unexpected warning: %q", errBuf.String())
	}
}
