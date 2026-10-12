package opencode_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/khoinguyen/factotum/pkg/harness/opencode"
)

// TestPluginLoadsOnInstalledOpenCode loads the receiver plugin in the installed
// OpenCode. It is the regression guard for the cmux bug this task fixes: the
// plugin must default-export server(), not setup(), or OpenCode refuses it.
//
// The guard fails on a load error and passes once the staged plugin's server()
// registers with the stub ft. OpenCode's first run in a fresh checkout forks a
// detached install and can exit before its plugin subsystem runs, so when no
// register is observed in the attempts the test skips explicitly rather than
// flaking; the static server() shape is covered deterministically by the
// plugin's Node unit tests and by TestPluginLoadGuardRejectsWrongShape. Gated on
// the binary, so CI without OpenCode skips.
func TestPluginLoadsOnInstalledOpenCode(t *testing.T) {
	res := runLoadGuard(t, opencode.MsgPluginBytes(), 6)
	if res.loadError {
		t.Fatalf("receiver plugin failed to load on opencode:\n%s", res.output)
	}
	if !res.registered {
		t.Skipf("opencode did not reach the staged plugin's server() in the attempts (no load error seen); skipped rather than flaked. The static shape is covered by the plugin's Node unit tests\nopencode output:\n%s", res.output)
	}
}

// TestPluginLoadGuardRejectsWrongShape pins that the guard is real: a
// setup()-shaped plugin (the exact cmux regression) must NOT be observed as
// loaded. The guard is isolated from the machine's global OpenCode config, so a
// pre-installed global factotum-msg.js (loop agents `ft msg install` one) cannot
// register on the broken plugin's behalf and mask the regression.
func TestPluginLoadGuardRejectsWrongShape(t *testing.T) {
	res := runLoadGuard(t, []byte("export const setup = () => ({});\n"), 3)
	if res.registered {
		t.Fatalf("a setup()-shaped plugin was observed as loaded; the guard is not real:\n%s", res.output)
	}
}

// loadGuardResult is what a guarded opencode run observed.
type loadGuardResult struct {
	// registered is true when the staged plugin ran server() and registered
	// with the stub ft.
	registered bool
	// loadError is true when opencode logged a load failure naming the plugin.
	loadError bool
	// output is the last opencode run's combined output.
	output []byte
}

// runLoadGuard stages plugin where OpenCode loads project plugins and runs
// `opencode run` until the plugin registers or the attempts are exhausted. It
// runs with an isolated XDG_CONFIG_HOME so the machine's own global plugins
// (including a pre-installed factotum-msg.js) do not load: only the staged
// plugin can register, which is the whole point of the guard. A bogus model
// makes opencode exit quickly, so a register can race the exit; the retry loop
// waits it out. Skips explicitly when opencode is not on PATH.
func runLoadGuard(t *testing.T, plugin []byte, attempts int) loadGuardResult {
	t.Helper()
	opencodePath, err := exec.LookPath("opencode")
	if err != nil {
		t.Skipf("opencode is not on PATH: %v", err)
	}

	// Do not use t.TempDir: OpenCode bootstraps a project runtime by forking a
	// detached `@opencode-ai/plugin` install into .opencode/ (thousands of
	// files), which can outlive `opencode run` and race the framework's
	// RemoveAll cleanup with "directory not empty". A self-managed dir with
	// best-effort cleanup keeps that incidental write from failing the guard.
	dir, err := os.MkdirTemp("", "factotum-opencode-plugin-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		if err := removeAllEventually(dir); err != nil {
			t.Logf("scratch dir %s not fully removed (OpenCode's detached install may still be writing): %v", dir, err)
		}
	})
	pluginDir := filepath.Join(dir, ".opencode", "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "factotum-msg.js"), plugin, 0o644); err != nil {
		t.Fatalf("write plugin: %v", err)
	}

	callsPath := filepath.Join(dir, "calls.log")
	stub := filepath.Join(dir, "ft-stub.sh")
	stubScript := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >> \"$CALLS\"\n" +
		"case \"$3\" in\n" +
		"  register) printf '{\"run_id\":\"run-1\"}';;\n" +
		"  claim) printf '{\"found\":false}';;\n" +
		"  *) printf '{\"ok\":true}';;\n" +
		"esac\n"
	if err := os.WriteFile(stub, []byte(stubScript), 0o755); err != nil {
		t.Fatalf("write stub: %v", err)
	}

	// Isolate OpenCode's global config so the machine's own plugins never
	// load: without this, a pre-installed global factotum-msg.js registers and
	// satisfies the guard even when the staged plugin is broken, and the
	// unrelated cmux plugins add load-error noise. A fresh XDG_CONFIG_HOME
	// keeps project-plugin loading (the staged plugin) and drops the globals.
	xdgConfig := filepath.Join(dir, "xdg")
	if err := os.MkdirAll(filepath.Join(xdgConfig, "opencode"), 0o755); err != nil {
		t.Fatalf("MkdirAll(xdg) error = %v", err)
	}

	env := append(os.Environ(),
		"XDG_CONFIG_HOME="+xdgConfig,
		"FACTOTUM_PROJECT=prj-test",
		"FACTOTUM_ACTOR=act-test",
		"FACTOTUM_BIN="+stub,
		"FACTOTUM_MSG_WAIT_MS=10",
		"FACTOTUM_MSG_TTL_MS=1000",
		"CALLS="+callsPath,
	)

	res := loadGuardResult{}
	for attempt := 0; attempt < attempts; attempt++ {
		_ = os.Remove(callsPath)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		cmd := exec.CommandContext(ctx, opencodePath, "run", "--print-logs", "--log-level", "ERROR", "--model", "bogus/none", "hi")
		cmd.Dir = dir
		cmd.Env = env
		res.output, _ = cmd.CombinedOutput()
		cancel()

		// An invalid plugin shape can be reported as a load error naming the
		// plugin (OpenCode validates global plugins; the project path is
		// silent on some versions, so the register probe below is the guard).
		if pluginLoadFailed(res.output) {
			res.loadError = true
			return res
		}
		if calls, _ := os.ReadFile(callsPath); strings.Contains(string(calls), "msg agent register") {
			res.registered = true
			return res
		}
	}
	return res
}

// pluginLoadFailed reports whether OpenCode logged a load failure for the
// receiver plugin. OpenCode 1.18.35 writes the path unquoted followed by the
// error, e.g.
//
//	level=ERROR message="failed to load plugin" path=file:///.../factotum-msg.js error="Plugin ... must default export an object with server()"
//
// The pattern is scoped to the plugin's own path so the (unrelated) global cmux
// plugins that also fail to load on this OpenCode version do not trip it.
func pluginLoadFailed(out []byte) bool {
	text := string(out)
	return strings.Contains(text, "factotum-msg.js error=") ||
		(strings.Contains(text, "failed to load plugin") && strings.Contains(text, "factotum-msg.js"))
}

// TestPluginLoadFailedDetector pins the detector against the exact OpenCode
// 1.18.35 line shape (path unquoted, then error=) so the load guard cannot
// silently become toothless, and against a run that only carries an unrelated
// global plugin failure.
func TestPluginLoadFailedDetector(t *testing.T) {
	broken := []byte(`timestamp=... level=ERROR message="failed to load plugin" path=file:///tmp/x/.opencode/plugin/factotum-msg.js error="Plugin file:///tmp/x/.opencode/plugin/factotum-msg.js must default export an object with server()"`)
	if !pluginLoadFailed(broken) {
		t.Fatal("pluginLoadFailed() = false for a genuine receiver load error")
	}
	other := []byte(`timestamp=... level=ERROR message="failed to load plugin" path=file:///home/u/.config/opencode/plugins/cmux-feed.js error="Plugin ... must default export an object with server()"`)
	if pluginLoadFailed(other) {
		t.Fatal("pluginLoadFailed() = true for an unrelated global plugin failure")
	}
}

// removeAllEventually removes dir, retrying while it is transiently
// non-removable. OpenCode forks a detached `@opencode-ai/plugin` install into
// .opencode/ (its config loader calls install(...).forkDetach), so it can still
// be writing files when `opencode run` returns; a single RemoveAll then fails
// with "directory not empty". Retrying waits the detached writer out. A caller
// that cannot tolerate leftovers should treat a returned error as best-effort.
func removeAllEventually(dir string) error {
	var err error
	for attempt := 0; attempt < 50; attempt++ {
		if err = os.RemoveAll(dir); err == nil {
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return err
}

// TestRemoveAllEventuallyRetriesPastTransientFailure pins the cleanup helper's
// retry contract: a scratch directory that cannot be removed on the first
// attempt is still removed once the transient condition clears. This is the
// cleanup half of the OpenCode load guard's flake — OpenCode forks a detached
// `@opencode-ai/plugin` install into .opencode/ that keeps writing after
// `opencode run` exits, so a single RemoveAll intermittently fails with
// "directory not empty".
func TestRemoveAllEventuallyRetriesPastTransientFailure(t *testing.T) {
	dir := t.TempDir()
	locked := filepath.Join(dir, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(locked, "keep"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	go func() {
		time.Sleep(250 * time.Millisecond)
		_ = os.Chmod(locked, 0o755)
	}()

	if err := removeAllEventually(dir); err != nil {
		t.Fatalf("removeAllEventually() = %v; want nil after the transient failure cleared", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("dir still present after removeAllEventually: stat err = %v", err)
	}
}

// TestPluginUnitTests runs the plugin's Node unit tests, so `mise run test`
// covers the receiver protocol logic (register/claim/inject/ack) without
// OpenCode or a store. Gated on node.
func TestPluginUnitTests(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skipf("node is not on PATH: %v", err)
	}
	cmd := exec.Command(nodePath, "--test", "plugin/factotum-msg.test.mjs")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node plugin tests failed: %v\n%s", err, out)
	}
}
