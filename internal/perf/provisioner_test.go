package perf

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/khoinguyen/factotum/pkg/core"
	"github.com/khoinguyen/factotum/pkg/perf"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
	"github.com/khoinguyen/factotum/pkg/store/memory"
	"github.com/khoinguyen/factotum/pkg/store/sqlite"
)

// ftBinary is the CLI built once for the integration tests in this package.
var ftBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "ft-perf-bin-")
	if err != nil {
		_, _ = os.Stderr.WriteString("mktemp: " + err.Error() + "\n")
		os.Exit(1)
	}
	bin := filepath.Join(dir, "ft")
	build := exec.Command("go", "build", "-o", bin, "./cmd/factotum")
	build.Dir = filepath.Join("..", "..")
	if out, err := build.CombinedOutput(); err != nil {
		_, _ = os.Stderr.WriteString("build ft: " + err.Error() + "\n" + string(out))
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	ftBinary = bin
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func openSeeded(t *testing.T, backend, path string, spec perf.Spec) store.Backend {
	t.Helper()
	ctx := context.Background()
	var (
		be  store.Backend
		err error
	)
	switch backend {
	case "jsonfile":
		be, err = jsonfile.Open(ctx, store.Config{Backend: backend, Options: map[string]string{"path": path}})
	case "sqlite":
		be, err = sqlite.Open(ctx, store.Config{Backend: backend, Options: map[string]string{"path": path}})
	case "memory":
		be = memory.New()
	default:
		t.Fatalf("unknown backend %q", backend)
	}
	if err != nil {
		t.Fatalf("open %s: %v", backend, err)
	}
	t.Cleanup(func() { _ = be.Close() })
	if _, err := perf.Seed(ctx, be, spec); err != nil {
		t.Fatalf("seed %s: %v", backend, err)
	}
	return be
}

func TestProvisionFileBackendsRunTheRealCLI(t *testing.T) {
	ctx := context.Background()
	for _, backend := range []string{"sqlite", "jsonfile"} {
		t.Run(backend, func(t *testing.T) {
			provisioner := Provisioner{Binary: ftBinary}
			runner, cleanup, err := provisioner.Provision(ctx, backend, perf.Spec{Tasks: 5, Artifacts: 2, Events: 3})
			if err != nil {
				t.Fatalf("Provision() error = %v", err)
			}
			defer cleanup()

			elapsed, written, err := runner.Run(ctx, perf.Op{Name: "task get", Args: []string{"task", "get", string(perf.TaskID(0))}})
			if err != nil {
				t.Fatalf("Run(task get) error = %v", err)
			}
			if elapsed <= 0 {
				t.Fatalf("elapsed = %v, want > 0", elapsed)
			}
			if written == 0 {
				t.Fatal("expected task get output, got none")
			}
		})
	}
}

func TestProvisionMemoryRunsInProcess(t *testing.T) {
	provisioner := Provisioner{}
	runner, cleanup, err := provisioner.Provision(context.Background(), "memory", perf.Spec{Tasks: 5})
	if err != nil {
		t.Fatalf("Provision() error = %v", err)
	}
	defer cleanup()

	_, written, err := runner.Run(context.Background(), perf.Op{Name: "task get", Args: []string{"task", "get", string(perf.TaskID(0))}})
	if err != nil {
		t.Fatalf("Run(task get) error = %v", err)
	}
	if written == 0 {
		t.Fatal("expected task get output, got none")
	}

	if _, _, err := runner.Run(context.Background(), perf.Op{Name: "bogus", Args: []string{"task", "get", "t-999999"}}); err == nil {
		t.Fatal("Run(missing task) error = nil, want not-found error")
	}
}

func TestProvisionUnknownBackendErrors(t *testing.T) {
	provisioner := Provisioner{}
	if _, _, err := provisioner.Provision(context.Background(), "nope", perf.Spec{Tasks: 1}); err == nil {
		t.Fatal("Provision(nope) error = nil, want error")
	}
}

func TestProvisionFileBackendNeedsBinary(t *testing.T) {
	provisioner := Provisioner{}
	if _, _, err := provisioner.Provision(context.Background(), "sqlite", perf.Spec{Tasks: 1}); err == nil {
		t.Fatal("Provision(sqlite) without binary error = nil, want error")
	}
}

func TestSeedWritesReadableStoreForSubprocess(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db.sqlite")
	be := openSeeded(t, "sqlite", path, perf.Spec{Tasks: 12})
	if err := be.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// A fresh open, as the subprocess would do, sees the seeded data.
	reopened, err := sqlite.Open(ctx, store.Config{Backend: "sqlite", Options: map[string]string{"path": path}})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = reopened.Close() }()
	task, err := reopened.Tasks().Get(ctx, core.TaskID("t-000011"))
	if err != nil {
		t.Fatalf("Get(t-000011) error = %v", err)
	}
	if task.Title != "task 11" {
		t.Fatalf("title = %q, want task 11", task.Title)
	}
}

func TestSubprocessEnvStripsFactotumAndSetsHarnessSettings(t *testing.T) {
	env := subprocessEnv()
	joined := strings.Join(env, "\n")
	if !strings.Contains(joined, "FACTOTUM_MAX_OUTPUT=unlimited") {
		t.Fatal("expected FACTOTUM_MAX_OUTPUT=unlimited")
	}
	if !strings.Contains(joined, "FACTOTUM_NO_HINTS=1") {
		t.Fatal("expected FACTOTUM_NO_HINTS=1")
	}
}

func TestInProcessRunnerIsolatesAwayMachineConfig(t *testing.T) {
	if got := isolatedEnv("FACTOTUM_MAX_OUTPUT"); got != "unlimited" {
		t.Fatalf("isolatedEnv(FACTOTUM_MAX_OUTPUT) = %q", got)
	}
	if got := isolatedEnv("FACTOTUM_PROJECT"); got != "" {
		t.Fatalf("isolatedEnv(FACTOTUM_PROJECT) = %q, want empty", got)
	}
}
