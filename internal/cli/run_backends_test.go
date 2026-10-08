package cli

import (
	"bytes"
	"context"
	"errors"
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
	"github.com/khoinguyen/factotum/pkg/harness/opencode"
	"github.com/khoinguyen/factotum/pkg/harness/pi"
	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/docker"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
	"github.com/khoinguyen/factotum/pkg/isolation/local"
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

// TestRunBackendsLocalForwardsAllowHost pins that the host-scoped opt-in reaches
// the local backend factory, so a persisted run.allow_host actually lets the
// unsandboxed backend run without --allow-host.
func TestRunBackendsLocalForwardsAllowHost(t *testing.T) {
	reg := runBackends(func(string) string { return "" })
	factory, err := reg.MustLookup(local.Name)
	if err != nil {
		t.Fatalf("lookup %q: %v", local.Name, err)
	}
	spec := isolation.Spec{Workdir: t.TempDir()}

	optedIn, err := factory(config.Run{AllowHost: true}, io.Discard)
	if err != nil {
		t.Fatalf("build local backend: %v", err)
	}
	if _, err := optedIn.Prepare(context.Background(), spec); err != nil {
		t.Fatalf("opted-in Prepare() error = %v, want nil", err)
	}

	notOptedIn, err := factory(config.Run{AllowHost: false}, io.Discard)
	if err != nil {
		t.Fatalf("build local backend: %v", err)
	}
	if _, err := notOptedIn.Prepare(context.Background(), spec); !errors.Is(err, local.ErrNotOptedIn) {
		t.Fatalf("Prepare() error = %v, want ErrNotOptedIn", err)
	}
}

// TestRunBackendsIncludeDocker pins that the docker isolation backend is a
// registered, selectable `ft run` backend and that its factory constructs
// without error.
func TestRunBackendsIncludeDocker(t *testing.T) {
	reg := runBackends(func(string) string { return "" })
	factory, err := reg.MustLookup(docker.Name)
	if err != nil {
		t.Fatalf("lookup %q: %v", docker.Name, err)
	}
	backend, err := factory(config.Run{}, io.Discard)
	if err != nil {
		t.Fatalf("build docker backend: %v", err)
	}
	if backend.Name() != docker.Name {
		t.Fatalf("backend.Name() = %q, want %q", backend.Name(), docker.Name)
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

// TestRunHarnessesIncludePi pins that pi is a registered, selectable `ft run`
// harness and that its factory forwards the configured model and credential, so
// `ft run --harness pi` resolves the same way OpenCode does.
func TestRunHarnessesIncludePi(t *testing.T) {
	reg := runHarnesses()
	names := map[string]bool{}
	for _, name := range reg.Names() {
		names[name] = true
	}
	for _, want := range []string{opencode.Name, pi.Name} {
		if !names[want] {
			t.Fatalf("run harnesses %v missing %q", reg.Names(), want)
		}
	}

	factory, err := reg.MustLookup(pi.Name)
	if err != nil {
		t.Fatalf("lookup %q: %v", pi.Name, err)
	}
	if h, err := factory(config.Run{Model: "openai/gpt-5"}); err != nil || h.Name() != pi.Name {
		t.Fatalf("build pi harness = (%v, %v), want name %q", h, err, pi.Name)
	}

	h, err := factory(config.Run{Model: "openrouter/x", Provider: "openrouter", CredentialEnvVar: "OPENROUTER_API_KEY"})
	if err != nil {
		t.Fatalf("build pi harness: %v", err)
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
		sandbox:   "capture",
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
		sandbox:   "capture",
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
// OpenShell backend without a project policy surfaces a clear message instead
// of silently denying all egress. The model provider endpoint is the one
// exception: ft attaches it from run.provider, so the warning names it when a
// provider is configured and otherwise points at the config that makes the
// model reachable.
func TestOpenShellFactoryWarnsWhenProjectPolicyMissing(t *testing.T) {
	factory, err := runBackends(func(string) string { return "" }).MustLookup("openshell")
	if err != nil {
		t.Fatalf("lookup openshell: %v", err)
	}
	policyPath := filepath.Join(t.TempDir(), "openshell-policy.yaml")

	t.Run("provider configured", func(t *testing.T) {
		var errBuf bytes.Buffer
		if _, err := factory(config.Run{PolicyPath: policyPath, Provider: "openrouter", CredentialEnvVar: "OPENROUTER_API_KEY"}, &errBuf); err != nil {
			t.Fatalf("build openshell backend: %v", err)
		}
		got := errBuf.String()
		if !strings.Contains(got, policyPath) {
			t.Fatalf("warning = %q, want it to name %q", got, policyPath)
		}
		if !strings.Contains(strings.ToLower(got), "deny-all") {
			t.Fatalf("warning = %q, want it to say egress is deny-all", got)
		}
		if !strings.Contains(got, "openrouter") {
			t.Fatalf("warning = %q, want it to name the configured provider", got)
		}
	})

	t.Run("no provider configured", func(t *testing.T) {
		var errBuf bytes.Buffer
		if _, err := factory(config.Run{PolicyPath: policyPath}, &errBuf); err != nil {
			t.Fatalf("build openshell backend: %v", err)
		}
		got := errBuf.String()
		if !strings.Contains(got, "run.provider") || !strings.Contains(got, "credential_env") {
			t.Fatalf("warning = %q, want it to point at run.provider/credential_env", got)
		}
	})
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
