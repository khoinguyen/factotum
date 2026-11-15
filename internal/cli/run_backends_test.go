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
	if h, err := factory(config.Run{Sandbox: local.Name, Model: "openai/gpt-5"}); err != nil || h.Name() != pi.Name {
		t.Fatalf("build pi harness = (%v, %v), want name %q", h, err, pi.Name)
	}

	h, err := factory(config.Run{Sandbox: openshell.Name, Image: "base-image", Model: "openrouter/x", Provider: "openrouter", CredentialEnvVar: "OPENROUTER_API_KEY"})
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

// TestRunHarnessLocalOmitsConfiguredCredential pins that a configured
// run.provider/run.credential_env is declared only to an isolating backend. The
// local backend runs the host binary in place, where it authenticates from the
// host's own configuration, and has no credential resolver to attach one: a
// declared credential made every local run fail Prepare with "no credential
// resolver configured" (t-2dqrwegfcc).
func TestRunHarnessLocalOmitsConfiguredCredential(t *testing.T) {
	run := config.Run{
		Model:            "openrouter/x",
		Image:            "base-image",
		Provider:         "openrouter",
		CredentialEnvVar: "OPENROUTER_API_KEY",
	}
	tests := []struct {
		sandbox   string
		wantCreds int
	}{
		{sandbox: local.Name, wantCreds: 0},
		{sandbox: openshell.Name, wantCreds: 1},
		{sandbox: docker.Name, wantCreds: 1},
	}
	for _, harnessName := range []string{opencode.Name, opencode.NameV2, pi.Name} {
		factory, err := runHarnesses().MustLookup(harnessName)
		if err != nil {
			t.Fatalf("lookup %q: %v", harnessName, err)
		}
		for _, tc := range tests {
			t.Run(harnessName+"/"+tc.sandbox, func(t *testing.T) {
				cfg := run
				cfg.Sandbox = tc.sandbox
				h, err := factory(cfg)
				if err != nil {
					t.Fatalf("build harness: %v", err)
				}
				spec, err := h.Spec(harness.Request{Workdir: "/w"})
				if err != nil {
					t.Fatalf("Spec() error = %v", err)
				}
				if len(spec.Credentials) != tc.wantCreds {
					t.Fatalf("Spec.Credentials = %v, want %d", spec.Credentials, tc.wantCreds)
				}
				if tc.wantCreds == 1 {
					if got := spec.Credentials[0]; got.Provider != "openrouter" || got.EnvVar != "OPENROUTER_API_KEY" {
						t.Fatalf("credential = %+v, want openrouter/OPENROUTER_API_KEY", got)
					}
				}
			})
		}
	}
}

// TestLocalRunPreparesConfiguredCredentialHarness pins the bug end to end at the
// wiring level: the spec a harness builds for a credential-configured local run
// must prepare on the local backend rather than fail with "no credential
// resolver configured" (t-2dqrwegfcc).
func TestLocalRunPreparesConfiguredCredentialHarness(t *testing.T) {
	cfg := config.Run{
		Sandbox:          local.Name,
		Model:            "openrouter/x",
		Provider:         "openrouter",
		CredentialEnvVar: "OPENROUTER_API_KEY",
	}
	backend := local.New(local.Options{AllowHost: true, Warn: io.Discard})
	for _, name := range []string{opencode.Name, opencode.NameV2, pi.Name} {
		t.Run(name, func(t *testing.T) {
			factory, err := runHarnesses().MustLookup(name)
			if err != nil {
				t.Fatalf("lookup %q: %v", name, err)
			}
			h, err := factory(cfg)
			if err != nil {
				t.Fatalf("build harness: %v", err)
			}
			spec, err := h.Spec(harness.Request{Workdir: t.TempDir()})
			if err != nil {
				t.Fatalf("Spec() error = %v", err)
			}
			handle, err := backend.Prepare(context.Background(), spec)
			if err != nil {
				t.Fatalf("local Prepare() error = %v, want nil (the credential is ignored on the host)", err)
			}
			if err := backend.Delete(context.Background(), handle); err != nil {
				t.Fatalf("Delete() error = %v", err)
			}
		})
	}
}

// TestRunHarnessesIncludeOpenCodeV2 pins that the OpenCode V2 harness is a
// registered, selectable `ft run` harness under its own key, so
// `ft run --harness opencode2` resolves the V2 variant (distinct binary/image)
// without shadowing the V1 `opencode` harness.
func TestRunHarnessesIncludeOpenCodeV2(t *testing.T) {
	reg := runHarnesses()
	factory, err := reg.MustLookup(opencode.NameV2)
	if err != nil {
		t.Fatalf("lookup %q: %v", opencode.NameV2, err)
	}
	h, err := factory(config.Run{Model: "opencode-go/deepseek-v4.1-flash"})
	if err != nil {
		t.Fatalf("build harness: %v", err)
	}
	if h.Name() != opencode.NameV2 {
		t.Fatalf("harness Name() = %q, want %q", h.Name(), opencode.NameV2)
	}
	cmd, err := h.Command(harness.Request{Prompt: "hi"})
	if err != nil {
		t.Fatalf("Command() error = %v", err)
	}
	if cmd.Argv[0] != "opencode2" {
		t.Fatalf("argv[0] = %q, want the V2 binary opencode2", cmd.Argv[0])
	}
	// The registry must hand a run the private-server argv: without it the run
	// attaches to V2's shared service, whose env has no FACTOTUM_*, and the
	// receiver ft staged never registers.
	if len(cmd.Argv) < 3 || cmd.Argv[1] != "run" || cmd.Argv[2] != "--standalone" {
		t.Fatalf("argv = %q, want run --standalone (private server)", cmd.Argv)
	}
}

// TestRunHarnessOpenCodeDetectsInPlaceV2 pins the in-place upgrade: `brew
// install opencode` moves V1 to V2 without renaming the binary, so the local
// backend must detect the installed `opencode`'s major version and drive V2
// argv (no top-level --model, private server) under the `opencode` key, while an
// isolating backend runs the pinned image and is never probed.
func TestRunHarnessOpenCodeDetectsInPlaceV2(t *testing.T) {
	writeOpencode := func(t *testing.T, banner string) string {
		t.Helper()
		dir := t.TempDir()
		bin := filepath.Join(dir, "opencode")
		script := "#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then echo '" + banner + "'; exit 0; fi\nexit 0\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
		return dir
	}

	t.Run("local detects V2 and keeps the in-place binary name", func(t *testing.T) {
		t.Setenv("PATH", writeOpencode(t, "opencode v2.0.25"))
		factory, err := runHarnesses().MustLookup(opencode.Name)
		if err != nil {
			t.Fatalf("lookup opencode: %v", err)
		}
		h, err := factory(config.Run{Sandbox: local.Name})
		if err != nil {
			t.Fatalf("build harness: %v", err)
		}
		interactive, err := h.Command(harness.Request{Prompt: "groom", Interactive: true, Model: "provider/model"})
		if err != nil {
			t.Fatalf("Command() error = %v", err)
		}
		wantInteractive := []string{"opencode", "--standalone", "--prompt", "groom"}
		if strings.Join(interactive.Argv, "\x00") != strings.Join(wantInteractive, "\x00") {
			t.Fatalf("interactive argv = %q, want %q", interactive.Argv, wantInteractive)
		}
		headless, err := h.Command(harness.Request{Prompt: "go", Model: "provider/model"})
		if err != nil {
			t.Fatalf("Command() error = %v", err)
		}
		wantHeadless := []string{"opencode", "run", "--standalone", "--model", "provider/model", "--", "go"}
		if strings.Join(headless.Argv, "\x00") != strings.Join(wantHeadless, "\x00") {
			t.Fatalf("headless argv = %q, want %q", headless.Argv, wantHeadless)
		}
	})

	t.Run("local keeps V1 when the binary is V1", func(t *testing.T) {
		t.Setenv("PATH", writeOpencode(t, "1.19.0"))
		factory, err := runHarnesses().MustLookup(opencode.Name)
		if err != nil {
			t.Fatalf("lookup opencode: %v", err)
		}
		h, err := factory(config.Run{Sandbox: local.Name})
		if err != nil {
			t.Fatalf("build harness: %v", err)
		}
		cmd, err := h.Command(harness.Request{Prompt: "groom", Interactive: true, Model: "provider/model"})
		if err != nil {
			t.Fatalf("Command() error = %v", err)
		}
		want := []string{"opencode", "--model", "provider/model", "--prompt", "groom"}
		if strings.Join(cmd.Argv, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("interactive argv = %q, want %q", cmd.Argv, want)
		}
	})

	t.Run("isolating backend is not probed", func(t *testing.T) {
		t.Setenv("PATH", writeOpencode(t, "opencode v2.0.25"))
		factory, err := runHarnesses().MustLookup(opencode.Name)
		if err != nil {
			t.Fatalf("lookup opencode: %v", err)
		}
		h, err := factory(config.Run{Sandbox: "docker"})
		if err != nil {
			t.Fatalf("build harness: %v", err)
		}
		cmd, err := h.Command(harness.Request{Prompt: "groom", Interactive: true, Model: "provider/model"})
		if err != nil {
			t.Fatalf("Command() error = %v", err)
		}
		want := []string{"opencode", "--model", "provider/model", "--prompt", "groom"}
		if strings.Join(cmd.Argv, "\x00") != strings.Join(want, "\x00") {
			t.Fatalf("interactive argv = %q, want the pinned V1 shape %q", cmd.Argv, want)
		}
	})
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
// exception: ft attaches it from run.provider, so the warning names the host
// that provider's profile authorizes when a provider is configured and
// otherwise points at the config that makes the model reachable.
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
		if !strings.Contains(got, "openrouter.ai") {
			t.Fatalf("warning = %q, want it to name the provider host openrouter.ai", got)
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
		if !strings.Contains(got, policyPath) {
			t.Fatalf("warning = %q, want it to name %q", got, policyPath)
		}
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

// TestRunHarnessPiNeedsAnImageToIsolate pins the pi image rule: pi ships as an
// npm package, not a published image, so an isolating backend must be told which
// base image carries it (run.image, --image, or FACTOTUM_RUN_IMAGE), while a
// local run drives the host binary and needs none. An isolating run that names
// no image fails at selection with a message naming the knob, instead of
// launching a backend default image that has no pi on PATH.
func TestRunHarnessPiNeedsAnImageToIsolate(t *testing.T) {
	factory, err := runHarnesses().MustLookup(pi.Name)
	if err != nil {
		t.Fatalf("lookup %q: %v", pi.Name, err)
	}

	t.Run("local runs the host binary", func(t *testing.T) {
		h, err := factory(config.Run{Sandbox: local.Name})
		if err != nil {
			t.Fatalf("build pi harness on local = %v, want no image required", err)
		}
		spec, err := h.Spec(harness.Request{Workdir: "/w"})
		if err != nil {
			t.Fatalf("Spec() error = %v", err)
		}
		if spec.Image.Ref != "" {
			t.Fatalf("local Spec.Image.Ref = %q, want empty (host binary)", spec.Image.Ref)
		}
	})

	t.Run("isolating without an image is rejected", func(t *testing.T) {
		for _, sandbox := range []string{openshell.Name, docker.Name} {
			// A user does not stand in for an image: the gate is about pi
			// being on PATH inside the image, not about identity.
			for _, cfg := range []config.Run{
				{Sandbox: sandbox},
				{Sandbox: sandbox, User: "501:20"},
			} {
				_, err := factory(cfg)
				if err == nil {
					t.Fatalf("build pi harness on %s (user %q) = nil error, want a missing-image error", sandbox, cfg.User)
				}
				for _, want := range []string{"pi", "run.image", "--image"} {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("error %q does not name %q", err, want)
					}
				}
			}
		}
	})

	t.Run("isolating carries the configured image", func(t *testing.T) {
		h, err := factory(config.Run{Sandbox: openshell.Name, Image: "ghcr.io/acme/pi:1"})
		if err != nil {
			t.Fatalf("build pi harness with an image: %v", err)
		}
		spec, err := h.Spec(harness.Request{Workdir: "/w"})
		if err != nil {
			t.Fatalf("Spec() error = %v", err)
		}
		if want := "ghcr.io/acme/pi:1"; spec.Image.Ref != want {
			t.Fatalf("Spec.Image.Ref = %q, want %q", spec.Image.Ref, want)
		}
	})
}

// TestRunHarnessesForwardConfiguredImage pins that a configured base image
// reaches every harness, not only pi: run.image overrides the harness's own
// default, and OpenCode stops forcing the shipped image's uid so a
// caller-supplied image keeps its own identity.
func TestRunHarnessesForwardConfiguredImage(t *testing.T) {
	const image = "ghcr.io/acme/agent:1"
	for _, name := range []string{opencode.Name, opencode.NameV2, pi.Name} {
		t.Run(name, func(t *testing.T) {
			factory, err := runHarnesses().MustLookup(name)
			if err != nil {
				t.Fatalf("lookup %q: %v", name, err)
			}
			h, err := factory(config.Run{Sandbox: docker.Name, Image: image})
			if err != nil {
				t.Fatalf("build harness: %v", err)
			}
			spec, err := h.Spec(harness.Request{})
			if err != nil {
				t.Fatalf("Spec() error = %v", err)
			}
			if spec.Image.Ref != image {
				t.Fatalf("Spec.Image.Ref = %q, want the configured %q", spec.Image.Ref, image)
			}
			if spec.Image.User != "" {
				t.Fatalf("Spec.Image.User = %q, want empty so the supplied image's own user applies", spec.Image.User)
			}
		})
	}
}

// TestPrepareRunForwardsImageFlag pins that --image reaches the harness factory
// as run.image, so a per-run base image needs no config edit.
func TestPrepareRunForwardsImageFlag(t *testing.T) {
	var got config.Run
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, func(string) string { return "" })
	if err := deps.RunBackends.Register("capture", func(cfg config.Run, _ io.Writer) (isolation.IsolationBackend, error) {
		return isofake.New("capture"), nil
	}); err != nil {
		t.Fatalf("register backend: %v", err)
	}
	if err := deps.RunHarnesses.Register("fake", func(cfg config.Run) (harness.Harness, error) {
		got = cfg
		return harnessfake.New("fake"), nil
	}); err != nil {
		t.Fatalf("register harness: %v", err)
	}

	if _, err := deps.prepareRun(&cobra.Command{Use: "run"}, runOptions{
		sandbox:   "capture",
		harness:   "fake",
		image:     "ghcr.io/acme/pi:1",
		workspace: t.TempDir(),
	}); err != nil {
		t.Fatalf("prepareRun() error = %v", err)
	}
	if want := "ghcr.io/acme/pi:1"; got.Image != want {
		t.Fatalf("harness factory got Image = %q, want %q", got.Image, want)
	}
}

// TestRunHarnessesForwardConfiguredUser pins that a configured container
// identity reaches every harness's spec, so an isolating backend runs the
// workload as that uid:gid instead of whatever the image's own USER says.
func TestRunHarnessesForwardConfiguredUser(t *testing.T) {
	const user = "501:20"
	for _, name := range []string{opencode.Name, opencode.NameV2, pi.Name} {
		t.Run(name, func(t *testing.T) {
			factory, err := runHarnesses().MustLookup(name)
			if err != nil {
				t.Fatalf("lookup %q: %v", name, err)
			}
			h, err := factory(config.Run{Sandbox: docker.Name, Image: "ghcr.io/acme/agent:1", User: user})
			if err != nil {
				t.Fatalf("build harness: %v", err)
			}
			spec, err := h.Spec(harness.Request{})
			if err != nil {
				t.Fatalf("Spec() error = %v", err)
			}
			if spec.Image.User != user {
				t.Fatalf("Spec.Image.User = %q, want the configured %q", spec.Image.User, user)
			}
		})
	}
}

// dockerArgvRunner records the docker CLI invocations a backend makes and
// reports the container as running, so a run's argv can be asserted without a
// daemon. Start is unused by Prepare and returns an error if a test reaches it.
type dockerArgvRunner struct {
	runs [][]string
}

func (r *dockerArgvRunner) Run(_ context.Context, args []string, _ map[string]string) ([]byte, error) {
	r.runs = append(r.runs, append([]string(nil), args...))
	if len(args) > 0 && args[0] == "inspect" {
		return []byte("true\n"), nil
	}
	return nil, nil
}

func (r *dockerArgvRunner) Start(context.Context, []string, docker.StartInput) (docker.Process, error) {
	return nil, errors.New("dockerArgvRunner.Start is not used by Prepare")
}

// dockerRun returns the recorded `docker run` invocation, failing when the
// backend never made one.
func (r *dockerArgvRunner) dockerRun(t *testing.T) []string {
	t.Helper()
	for _, args := range r.runs {
		if len(args) > 0 && args[0] == "run" {
			return args
		}
	}
	t.Fatalf("no docker run invocation recorded; calls = %v", r.runs)
	return nil
}

// TestRunHarnessCustomImageKeepsImageUserInDockerArgv pins the exact
// combination the identity default hinges on: OpenCode (V1 and V2) with a
// caller-supplied image and no configured user. The harness must hand the
// backend an empty identity, so the docker run argv carries no --user and stays
// identical to the argv from before the identity knob existed — the image's own
// USER applies. Emitting the shipped 1000:1000 here would silently override it.
func TestRunHarnessCustomImageKeepsImageUserInDockerArgv(t *testing.T) {
	const image = "ghcr.io/acme/agent:1"
	for _, name := range []string{opencode.Name, opencode.NameV2} {
		t.Run(name, func(t *testing.T) {
			factory, err := runHarnesses().MustLookup(name)
			if err != nil {
				t.Fatalf("lookup %q: %v", name, err)
			}
			h, err := factory(config.Run{Sandbox: docker.Name, Image: image})
			if err != nil {
				t.Fatalf("build harness: %v", err)
			}
			workdir := t.TempDir()
			spec, err := h.Spec(harness.Request{Workdir: workdir})
			if err != nil {
				t.Fatalf("Spec() error = %v", err)
			}

			runner := &dockerArgvRunner{}
			backend := docker.New(docker.Options{
				Runner:  runner,
				NewName: func() string { return "fttest" },
			})
			if _, err := backend.Prepare(context.Background(), spec); err != nil {
				t.Fatalf("Prepare() error = %v", err)
			}

			got := runner.dockerRun(t)
			if slices.Contains(got, "--user") {
				t.Fatalf("docker run argv = %q, want no --user so the image's own USER applies", got)
			}
			want := []string{
				"run",
				"--detach",
				"--name", "fttest",
				"--workdir", workdir,
				"--mount", "type=bind,source=" + workdir + ",target=" + workdir,
				"--entrypoint", "sh", image, "-c", docker.KeepAlive,
			}
			if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
				t.Fatalf("docker run argv = %q, want %q", got, want)
			}
		})
	}
}

// TestPrepareRunForwardsUserFlag pins that --user reaches the harness factory as
// run.user, so one run can force a container identity without a config edit.
func TestPrepareRunForwardsUserFlag(t *testing.T) {
	var got config.Run
	deps := NewDeps(app.SystemClock{}, app.RandomIDGen{}, io.Discard, io.Discard, func(string) string { return "" })
	if err := deps.RunBackends.Register("capture", func(cfg config.Run, _ io.Writer) (isolation.IsolationBackend, error) {
		return isofake.New("capture"), nil
	}); err != nil {
		t.Fatalf("register backend: %v", err)
	}
	if err := deps.RunHarnesses.Register("fake", func(cfg config.Run) (harness.Harness, error) {
		got = cfg
		return harnessfake.New("fake"), nil
	}); err != nil {
		t.Fatalf("register harness: %v", err)
	}

	if _, err := deps.prepareRun(&cobra.Command{Use: "run"}, runOptions{
		sandbox:   "capture",
		harness:   "fake",
		user:      "501:20",
		workspace: t.TempDir(),
	}); err != nil {
		t.Fatalf("prepareRun() error = %v", err)
	}
	if want := "501:20"; got.User != want {
		t.Fatalf("harness factory got User = %q, want %q", got.User, want)
	}
}

// TestPrepareRunResolvesSandboxBeforeHarness pins the ordering prepareRun must
// keep: the sandbox is resolved before the harness is built. A local run
// declares no credential (the host binary already carries its own), and that
// rule lives in the harness factory, so a harness built from a not-yet-resolved
// config would re-declare a credential no local backend can attach
// (t-2dqrwegfcc). The assertion is the spec the selected harness builds - what a
// run would actually prepare - not the call order.
func TestPrepareRunResolvesSandboxBeforeHarness(t *testing.T) {
	p := &scriptedPrompter{t: t, inputs: []string{local.Name, "user"}, confirms: []bool{true}}
	deps := newOptInDeps(t, p)
	// The real harnesses, so the credential rule they apply is what the run sees.
	deps.RunHarnesses = runHarnesses()
	deps.Config.Run = config.Run{
		Harness:          opencode.NameV2,
		Model:            "openrouter/x",
		Provider:         "openrouter",
		CredentialEnvVar: "OPENROUTER_API_KEY",
	}

	sel, err := deps.prepareRun(&cobra.Command{Use: "run"}, runOptions{workspace: t.TempDir()})
	if err != nil {
		t.Fatalf("prepareRun() error = %v", err)
	}
	if got := sel.harness.Name(); got != opencode.NameV2 {
		t.Fatalf("harness = %q, want the prompted %q", got, opencode.NameV2)
	}
	spec, err := sel.harness.Spec(harness.Request{Workdir: t.TempDir()})
	if err != nil {
		t.Fatalf("Spec() error = %v", err)
	}
	if len(spec.Credentials) != 0 {
		t.Fatalf("spec.Credentials = %+v, want none: the local selection must be resolved before the harness is built", spec.Credentials)
	}
	if got := sel.backendName; got != local.Name {
		t.Fatalf("resolved backend = %q, want the prompted %q", got, local.Name)
	}
}
