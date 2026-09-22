// Package perf holds the concrete runners and provisioner that drive the
// end-to-end scale harness in pkg/perf against real backends and the real CLI.
package perf

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/khoinguyen/factotum/internal/cli"
	"github.com/khoinguyen/factotum/pkg/app"
	"github.com/khoinguyen/factotum/pkg/perf"
	"github.com/khoinguyen/factotum/pkg/store"
	"github.com/khoinguyen/factotum/pkg/store/builtins"
)

// execRunner measures a real `ft` process: the duration covers process start,
// command execution, and exit, and the byte count is what the command wrote to
// stdout. Output bounding and hints are disabled so the byte count is the real
// payload rather than a head/tail window.
type execRunner struct {
	binary string
	args   []string
	env    []string
}

func (r *execRunner) Run(ctx context.Context, op perf.Op) (time.Duration, int, error) {
	command := exec.CommandContext(ctx, r.binary, append(append([]string{}, r.args...), op.Args...)...)
	command.Env = r.env
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	start := time.Now()
	err := command.Run()
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, stdout.Len(), fmt.Errorf("ft %s: %w: %s", strings.Join(op.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return elapsed, stdout.Len(), nil
}

// inProcessRunner measures the CLI command tree in the harness process against a
// backend it keeps open. The shared backend is injected as a store factory so
// each invocation resolves to the seeded data. Process startup is not included;
// memory is the only backend exercised this way because it has no file a
// subprocess could reopen.
type inProcessRunner struct {
	backend    store.Backend
	configPath string
}

func (r *inProcessRunner) Run(_ context.Context, op perf.Op) (time.Duration, int, error) {
	var stdout, stderr bytes.Buffer
	deps := cli.NewDeps(app.SystemClock{}, app.RandomIDGen{}, &stdout, &stderr, isolatedEnv)
	builtins.RegisterAll(deps.StoreFactories)
	if err := deps.StoreFactories.Register(sharedBackend, func(context.Context, store.Config) (store.Backend, error) {
		return r.backend, nil
	}); err != nil {
		return 0, 0, err
	}
	root := cli.NewRoot(deps)
	root.SetArgs(append([]string{
		"--store", sharedBackend,
		"--config", r.configPath,
	}, op.Args...))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	start := time.Now()
	err := root.Execute()
	elapsed := time.Since(start)
	if err != nil {
		return elapsed, stdout.Len(), fmt.Errorf("ft %s: %w: %s", strings.Join(op.Args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return elapsed, stdout.Len(), nil
}

// sharedBackend is the store name the in-process runner registers so the CLI
// resolves the already-open memory backend instead of creating an empty one.
const sharedBackend = "perf-shared"

// isolatedEnv is the environment the in-process runner exposes to config
// loading: no machine paths, full output (no bounding), and no hints, so the
// measurement matches the subprocess runner.
func isolatedEnv(key string) string {
	switch key {
	case "FACTOTUM_MAX_OUTPUT":
		return "unlimited"
	case "FACTOTUM_NO_HINTS":
		return "1"
	default:
		return ""
	}
}

// subprocessEnv returns os.Environ with any FACTOTUM_* variable removed and the
// harness's own settings applied, so a developer's environment cannot leak into
// a measurement. The caller supplies explicit --config/--user-config paths.
func subprocessEnv() []string {
	env := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "FACTOTUM_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, "FACTOTUM_MAX_OUTPUT=unlimited", "FACTOTUM_NO_HINTS=1")
}
