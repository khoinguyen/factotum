package openshell_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/isolation"
	"github.com/khoinguyen/factotum/pkg/isolation/openshell"
)

// TestLiveOpenShellRun exercises the real OpenShell path end to end against a
// live gateway and logs a transcript: create a hardened sandbox, upload a host
// workspace, run the harness image, download a result, attach a (fake)
// credential and observe the provider placeholder, then delete and confirm no
// provider is left behind. It also asserts the safety properties the policy
// promises (non-root, read-only system paths, deny-by-default egress).
//
// It is opt-in (FACTOTUM_OPENSHELL_TEST=1) and uses a fake key only. Run:
//
//	FACTOTUM_OPENSHELL_TEST=1 go test -count=1 -run TestLiveOpenShellRun -v ./pkg/isolation/openshell/
func TestLiveOpenShellRun(t *testing.T) {
	if os.Getenv("FACTOTUM_OPENSHELL_TEST") == "" {
		t.Skip("set FACTOTUM_OPENSHELL_TEST=1 with a running OpenShell gateway to run the live path")
	}

	const (
		fakeKey = "sk-live-test-not-a-real-key"
		envVar  = "OPENROUTER_API_KEY"
	)
	host := t.TempDir()
	if err := os.WriteFile(filepath.Join(host, "input.txt"), []byte("hello from the throwaway workspace\n"), 0o644); err != nil {
		t.Fatalf("write input: %v", err)
	}
	guestWorkdir := openshell.DefaultWorkdir + "/" + filepath.Base(host)

	be := openshell.New(openshell.Options{
		ReadyTimeout: 3 * time.Minute,
		Credentials: resolverFunc(func(context.Context, isolation.Credential) (string, error) {
			return fakeKey, nil
		}),
	})
	ctx := context.Background()
	h, err := be.Prepare(ctx, isolation.Spec{
		Workdir: host,
		Labels:  map[string]string{"task": "t-6edlc2e3tj"},
	})
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}
	t.Logf("prepared sandbox %s, guest workdir %s", h.ID(), guestWorkdir)

	if err := be.AttachCredential(ctx, h, isolation.Credential{Provider: "openrouter", Ref: "live-test", EnvVar: envVar}); err != nil {
		t.Fatalf("AttachCredential() error = %v", err)
	}
	t.Log("attached a fake openrouter provider")

	run := func(argv ...string) isolation.ExecResult {
		t.Helper()
		ex, err := be.Exec(ctx, h, isolation.Command{Argv: argv})
		if err != nil {
			t.Fatalf("Exec(%v) error = %v", argv, err)
		}
		for range ex.Events() {
		}
		res, err := ex.Wait(ctx)
		if err != nil {
			t.Fatalf("Wait(%v) error = %v", argv, err)
		}
		return res
	}

	t.Run("workspace uploaded", func(t *testing.T) {
		res := run("cat", guestWorkdir+"/input.txt")
		t.Logf("cat input.txt -> %q", strings.TrimSpace(string(res.Stdout)))
		if !strings.Contains(string(res.Stdout), "throwaway workspace") {
			t.Fatalf("uploaded workspace not readable: %q", res.Stdout)
		}
	})

	t.Run("non-root", func(t *testing.T) {
		res := run("id")
		t.Logf("id -> %q", strings.TrimSpace(string(res.Stdout)))
		if !strings.Contains(string(res.Stdout), "uid=1000") {
			t.Fatalf("expected non-root uid 1000, got %q", res.Stdout)
		}
	})

	t.Run("read-only system path", func(t *testing.T) {
		res := run("sh", "-c", "touch /etc/ft-live-probe 2>&1; echo rc=$?")
		t.Logf("touch /etc -> %q", strings.TrimSpace(string(res.Stdout)))
		if !strings.Contains(string(res.Stdout), "rc=1") {
			t.Fatalf("writing /etc was not denied: %q", res.Stdout)
		}
	})

	t.Run("deny-by-default egress", func(t *testing.T) {
		res := run("sh", "-c", "wget -T 5 -q -O - https://example.com 2>&1; echo rc=$?")
		t.Logf("wget example.com -> %q", strings.TrimSpace(string(res.Stdout)))
		if !strings.Contains(string(res.Stdout), "rc=1") {
			t.Fatalf("egress to example.com was not denied: %q", res.Stdout)
		}
	})

	t.Run("credential placeholder", func(t *testing.T) {
		res := run("env")
		out := string(res.Stdout)
		if !strings.Contains(out, envVar+"=openshell:resolve:") {
			t.Fatalf("provider did not inject a placeholder for %s: %q", envVar, out)
		}
		if strings.Contains(out, fakeKey) {
			t.Fatalf("the raw key reached the sandbox env")
		}
		t.Logf("env carries a provider placeholder for %s, not the raw key", envVar)
	})

	t.Run("harness image", func(t *testing.T) {
		res := run("opencode", "--version")
		t.Logf("opencode --version -> %q", strings.TrimSpace(string(res.Stdout)))
		if res.ExitCode != 0 || !strings.Contains(string(res.Stdout), "1.") {
			t.Fatalf("opencode harness not runnable: exit %d stdout %q stderr %q", res.ExitCode, res.Stdout, res.Stderr)
		}
	})

	t.Run("result downloaded", func(t *testing.T) {
		res := run("sh", "-c", "printf 'RESULT_OK\n' > result.txt; mkdir -p out/sub; printf DEEP > out/sub/deep.txt; echo wrote")
		t.Logf("wrote results -> %q", strings.TrimSpace(string(res.Stdout)))
		files, err := be.Download(ctx, h, []string{"result.txt", "out"})
		if err != nil {
			t.Fatalf("Download() error = %v", err)
		}
		byPath := map[string]string{}
		for _, f := range files {
			byPath[f.Path] = string(f.Content)
		}
		if strings.TrimSpace(byPath["result.txt"]) != "RESULT_OK" {
			t.Fatalf("downloaded result = %v, want RESULT_OK", byPath)
		}
		if byPath["out/sub/deep.txt"] != "DEEP" {
			t.Fatalf("downloaded directory = %v, want out/sub/deep.txt=DEEP", byPath)
		}
		t.Logf("downloaded %v", byPath)
	})

	sandbox := h.ID()
	if err := be.Delete(ctx, h); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	t.Log("deleted the sandbox")
	if providers := listProviders(t); strings.Contains(providers, sandbox) {
		t.Fatalf("provider for %s leaked after Delete:\n%s", sandbox, providers)
	}
	t.Log("no provider left behind")
}

// listProviders returns `openshell provider list` output for the leak check.
func listProviders(t *testing.T) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "openshell", "provider", "list").CombinedOutput()
	if err != nil {
		t.Fatalf("openshell provider list: %v: %s", err, out)
	}
	return string(out)
}
