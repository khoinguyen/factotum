package perf

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	perf "github.com/khoinguyen/factotum/pkg/perf"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/jsonfile"
	"github.com/khoinguyen/factotum/pkg/store/memory"
	"github.com/khoinguyen/factotum/pkg/store/sqlite"
)

// Provisioner opens and seeds a backend, then hands back a runner that measures
// the CLI against it. File backends are measured as a real subprocess (so
// process startup is included); memory is measured in-process because it cannot
// cross a process boundary.
type Provisioner struct {
	// Binary is the ft binary to run. It is required for file backends and
	// ignored for memory.
	Binary string
	// WorkDir is the parent directory for per-scale temp stores. Empty uses the
	// system temp directory.
	WorkDir string
}

// Provision implements perf.Provisioner.
func (p Provisioner) Provision(ctx context.Context, backend string, spec perf.Spec) (perf.Runner, func(), error) {
	dir, err := os.MkdirTemp(p.WorkDir, "ftscale-")
	if err != nil {
		return nil, nil, fmt.Errorf("create work dir: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	configPath := filepath.Join(dir, "project.toml")
	if err := os.WriteFile(configPath, []byte("project = \"perf\"\n"), 0o644); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("write project config: %w", err)
	}

	switch backend {
	case "memory":
		be := memory.New()
		if _, err := perf.Seed(ctx, be, spec); err != nil {
			_ = be.Close()
			cleanup()
			return nil, nil, fmt.Errorf("seed memory: %w", err)
		}
		return &inProcessRunner{backend: be, configPath: configPath}, cleanup, nil
	case "jsonfile", "sqlite":
		if p.Binary == "" {
			cleanup()
			return nil, nil, fmt.Errorf("backend %s needs a binary path", backend)
		}
		path := filepath.Join(dir, backend+".db")
		be, err := openBackend(ctx, backend, path)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		if _, err := perf.Seed(ctx, be, spec); err != nil {
			_ = be.Close()
			cleanup()
			return nil, nil, fmt.Errorf("seed %s: %w", backend, err)
		}
		if err := be.Close(); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("close %s: %w", backend, err)
		}
		args := []string{
			"--store", backend,
			"--store-opt", "path=" + path,
			"--config", configPath,
			"--user-config", filepath.Join(dir, "user.toml"),
		}
		return &execRunner{binary: p.Binary, args: args, env: subprocessEnv()}, cleanup, nil
	default:
		cleanup()
		return nil, nil, fmt.Errorf("unknown backend %q", backend)
	}
}

func openBackend(ctx context.Context, backend, path string) (store.Backend, error) {
	cfg := store.Config{Backend: backend, Options: map[string]string{"path": path}}
	switch backend {
	case "jsonfile":
		return jsonfile.Open(ctx, cfg)
	case "sqlite":
		return sqlite.Open(ctx, cfg)
	default:
		return nil, fmt.Errorf("unknown backend %q", backend)
	}
}
