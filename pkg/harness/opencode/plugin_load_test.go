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
// OpenCode and proves it registers the session. It is the regression guard for
// the cmux bug this task fixes: the plugin must default-export server(), not
// setup(), or OpenCode 1.18.35 refuses to load it. Gated on the binary so CI
// without OpenCode skips rather than fails.
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

	// A bogus model fails the turn quickly, so OpenCode can exit before the
	// plugin's asynchronous registration lands. Run a few times and accept the
	// first invocation that registered; a genuine load failure would fail every
	// time (and is detected from the load-error output).
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

		if strings.Contains(string(lastOut), "factotum-msg.js\" error") {
			t.Fatalf("receiver plugin failed to load on opencode:\n%s", lastOut)
		}
		if calls, _ := os.ReadFile(callsPath); strings.Contains(string(calls), "msg agent register") {
			return
		}
	}
	calls, _ := os.ReadFile(callsPath)
	t.Fatalf("receiver plugin did not register the session; calls=%q\nopencode output:\n%s", calls, lastOut)
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
