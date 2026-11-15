package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/khoinguyen/factotum/internal/config"
	harnesspkg "github.com/khoinguyen/factotum/pkg/harness"
	harnessfake "github.com/khoinguyen/factotum/pkg/harness/fake"
	"github.com/khoinguyen/factotum/pkg/isolation"
	isofake "github.com/khoinguyen/factotum/pkg/isolation/fake"
)

// TestRunInjectsSelectedHarnessEnv proves `ft run` hands the launched session
// the harness it selected (FACTOTUM_HARNESS), so the receiver's claim metadata
// reports that harness instead of the plugin's own default. An opencode2 run
// used to register as opencode.
func TestRunInjectsSelectedHarnessEnv(t *testing.T) {
	r := newRunner(t)
	_, taskID := runContext(t, r)

	backend := isofake.New("sandbox")
	backend.Program(isolation.ExecResult{Stdout: []byte("done\n"), ExitCode: 0})
	r.runBackend = backend
	r.runHarness = harnessfake.New("opencode2")

	r.run("run", taskID, "--sandbox", "fake", "--harness", "fake", "--workspace", t.TempDir())

	specs := backend.Prepared()
	if len(specs) != 1 {
		t.Fatalf("Prepare called %d times, want 1", len(specs))
	}
	if got := specs[0].Env[harnesspkg.EnvHarness]; got != "opencode2" {
		t.Errorf("env %s = %q, want the selected harness %q", harnesspkg.EnvHarness, got, "opencode2")
	}
}

// TestBuiltinHarnessNamesAreRegistrationKeys proves the harness name a run
// exports is the key the caller selected, for every built-in harness. The env
// value comes from Harness.Name(), so a registration whose name drifted from
// its key would silently mislabel a run.
func TestBuiltinHarnessNamesAreRegistrationKeys(t *testing.T) {
	reg := runHarnesses()
	// An isolating sandbox keeps selection off the host `opencode` version
	// probe, so each factory reports its declared variant deterministically.
	cfg := config.Run{Sandbox: "docker", Image: "example/pi:latest"}
	for _, key := range reg.Names() {
		t.Run(key, func(t *testing.T) {
			factory, err := reg.MustLookup(key)
			if err != nil {
				t.Fatalf("lookup %q: %v", key, err)
			}
			h, err := factory(cfg)
			if err != nil {
				t.Fatalf("build %q: %v", key, err)
			}
			if h.Name() != key {
				t.Errorf("harness registered as %q reports Name() = %q; a run would export the wrong %s", key, h.Name(), harnesspkg.EnvHarness)
			}
		})
	}
}

// TestHarnessEnvMatchesLoopAgent proves `ft run` and the chief's loop wrapper
// agree on the harness a V2 agent registers as: the wrapper hardcodes
// FACTOTUM_HARNESS, and the opencode2 harness a run exports must report the same
// value, or the same agent registers differently depending on how it launched.
func TestHarnessEnvMatchesLoopAgent(t *testing.T) {
	factory, err := runHarnesses().MustLookup("opencode2")
	if err != nil {
		t.Fatalf("lookup opencode2: %v", err)
	}
	h, err := factory(config.Run{Sandbox: "docker"})
	if err != nil {
		t.Fatalf("build opencode2: %v", err)
	}

	script := readRepoFile(t, filepath.Join(".agents", "skills", "chief", "scripts", "loop-agent.sh"))
	m := regexp.MustCompile(`(?m)^export FACTOTUM_HARNESS=(\S+)$`).FindStringSubmatch(script)
	if m == nil {
		t.Fatal("loop-agent.sh must export FACTOTUM_HARNESS")
	}
	if m[1] != h.Name() {
		t.Errorf("loop-agent.sh exports %s=%q but an opencode2 run exports %q; the same harness would register differently", harnesspkg.EnvHarness, m[1], h.Name())
	}
}

// readRepoFile reads a committed file relative to the repository root.
func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}
