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
// plugin must default-export server(), not setup(), or OpenCode 1.18.35 logs
// `failed to load plugin ... must default export an object with server()` and
// refuses to load it.
//
// The guard is the LOAD error, not registration: `opencode run` with a bogus
// model can exit before its plugin subsystem runs, so registration is observed
// best-effort. When the run does reach plugin loading, the stub ft records a
// register call; when it does not, the test skips rather than flaking (the
// deterministic shape check lives in the plugin's Node unit tests). Gated on the
// binary so CI without OpenCode skips.
func TestPluginLoadsOnInstalledOpenCode(t *testing.T) {
	opencodePath, err := exec.LookPath("opencode")
	if err != nil {
		t.Skipf("opencode is not on PATH: %v", err)
	}

	dir := t.TempDir()
	pluginDir := filepath.Join(dir, ".opencode", "plugin")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "factotum-msg.js"), opencode.MsgPluginBytes(), 0o644); err != nil {
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

	env := append(os.Environ(),
		"FACTOTUM_PROJECT=prj-test",
		"FACTOTUM_ACTOR=act-test",
		"FACTOTUM_BIN="+stub,
		"FACTOTUM_MSG_WAIT_MS=10",
		"FACTOTUM_MSG_TTL_MS=1000",
		"CALLS="+callsPath,
	)
	var lastOut []byte
	for attempt := 0; attempt < 3; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		cmd := exec.CommandContext(ctx, opencodePath, "run", "--print-logs", "--log-level", "ERROR", "--model", "bogus/none", "hi")
		cmd.Dir = dir
		cmd.Env = env
		lastOut, _ = cmd.CombinedOutput()
		cancel()

		// The regression this test exists for: an invalid plugin shape is
		// reported as a load error naming the plugin.
		if pluginLoadFailed(lastOut) {
			t.Fatalf("receiver plugin failed to load on opencode:\n%s", lastOut)
		}
		if calls, _ := os.ReadFile(callsPath); strings.Contains(string(calls), "msg agent register") {
			return // loaded and ran server()
		}
	}
	t.Skipf("opencode did not reach plugin loading in the attempts (no load error seen); skipped rather than flaked\nopencode output:\n%s", lastOut)
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
